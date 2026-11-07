// Package sales is the sales example app: customers and sales orders, in the role of the ERPNext sales module.
//
// It shows what an app is made of (DocTypes, hooks, a data migration, background jobs and a schedule) using the
// public forgebase API only. cmd/sales puts it into a binary; the module examples/sales_extension builds on it.
package sales

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tvstore/forgebase-go"
)

// Every file under doctypes/ that ends in .toml is embedded (a test checks that the directory and the embedded
// set are the same).
//
//go:embed doctypes/*.toml
var doctypes embed.FS

// The SQL of the report sales_by_customer (see App).
//
//go:embed reports/sales_by_customer.sql
var salesByCustomerSQL string

// The fixtures (ADR 0022): records that migrate inserts when missing and updates only when the app changes them.
//
//go:embed fixtures/*.json
var fixtures embed.FS

// Data migrations (SQL, append-only): see the comments in the file. The file name is the version and the
// name: 0001_default_customer_groups.sql is version 1, default_customer_groups.
//
//go:embed migrations/*.sql
var migrations embed.FS

// ApprovalLimit is the largest total a Sales User may approve: 10 000.00. Above it the "Approve" transition is
// vetoed and the order needs "Approve as Manager", a transition only a Sales Manager may take.
//
// A variable of the package and not a setting, because hooks have no database access (see also
// sales_extension.MinOrderTotal). It must not be changed.
var ApprovalLimit = forgebase.NewDecimal(1_000_000, 2)

// App is the app. A plain function that returns data: nothing is registered anywhere else.
func App() *forgebase.App {
	return forgebase.NewApp("sales").
		Version("0.1.0").
		DocTypes(doctypes).
		Hooks("sales_order", OrderRules{}).
		// Background jobs are known as <app>.<job>: sales.log_order_event is enqueued by the hooks below,
		// sales.daily_summary by the schedule. `forgebase worker` runs them.
		Job("log_order_event", logOrderEvent).
		Job("daily_summary", dailySummary).
		// Every day at 02:00 UTC. Run `migrate` after changing a schedule: it stores them.
		Schedule("daily_summary", forgebase.DailyAt(2, 0)).
		// An app method (ADR 0019): POST /api/v1/method/sales.order_summary, JSON in and out. It runs as the
		// caller, so the read rules of sales_order decide what it counts. See the guide
		// docs/guide/integration.md.
		Method("order_summary", forgebase.NewMethod(orderSummary).Roles("Sales User", "Sales Manager")).
		// A query report (ADR 0016): SQL compiled into the binary, run by the roles named here in a read-only
		// transaction. Parameters and columns are positional, see the SQL file.
		Report("sales_by_customer",
			forgebase.NewReport(salesByCustomerSQL).
				Label("Sales by customer").
				Roles("Sales Manager").
				Param(forgebase.DateParam("from").Required()).
				Param(forgebase.DateParam("to").Required()).
				Param(forgebase.LinkParam("customer", "customer")).
				Column(forgebase.LinkColumn("customer", "customer")).
				Column(forgebase.IntColumn("orders")).
				Column(forgebase.DecimalColumn("total")).
				Column(forgebase.DateColumn("last_order"))).
		// Data migrations (SQL, append-only): see the comments in the file.
		Migrations(migrations).
		// Fixtures (ADR 0022): records `migrate` inserts when missing and updates only when the app changes
		// them, after the migrations above. This file names Retail and Wholesale too, which the migration made
		// already: the first `migrate` adopts them ("kept") and inserts Online.
		Fixtures(fixtures)
}

// OrderRules are the business rules of a sales order: computed amounts and total, and what a submit needs.
type OrderRules struct{ forgebase.BaseHooks }

// Validate computes amount = qty * rate of every row and total = sum(amount).
//
// The computed values overwrite whatever the client sent: they are not input. Amounts are rounded to 2
// decimal places, half away from zero (2.345 becomes 2.35, -2.345 becomes -2.35). A row without a usable qty
// or rate (missing, null or of another kind: hooks run before the framework's own validation, which then
// reports the problem) gets no amount and adds nothing to the total.
func (OrderRules) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if doc.Tables == nil {
		doc.Tables = map[string][]forgebase.Row{}
	}
	if _, ok := doc.Tables["items"]; !ok {
		doc.Tables["items"] = []forgebase.Row{}
	}
	total := forgebase.DecimalFromInt(0)
	items := doc.Tables["items"]
	for i, row := range items {
		if row == nil { // a row that was sent as null: give it a place for its amount
			row = forgebase.Row{}
			items[i] = row
		}
		amount, ok, err := amountOf(row)
		if err != nil {
			return err
		}
		if !ok {
			row["amount"] = forgebase.Null()
			continue
		}
		sum, fits := total.Add(amount)
		if !fits {
			return forgebase.NewHookError("the order total is too large")
		}
		total = sum
		row["amount"] = forgebase.DecimalValue(amount)
	}
	total, _ = total.Rescale(2) // a sum of amounts of scale 2 fits again at scale 2
	if doc.Values == nil {
		doc.Values = map[string]forgebase.Value{}
	}
	doc.Values["total"] = forgebase.DecimalValue(total)
	return nil
}

// BeforeSubmit refuses to submit an order without items or with a total that is not positive, then asks for
// the job that logs the submit.
//
// Validate runs before this hook (see the pipeline in DocTypeHooks), so total is current here.
func (OrderRules) BeforeSubmit(ctx forgebase.HookContext, doc *forgebase.Document) error {
	if len(doc.Tables["items"]) == 0 {
		return forgebase.NewHookError("a sales order needs at least one item to be submitted")
	}
	if total := doc.Values["total"]; total.Kind != forgebase.KindDecimal || total.Dec.Sign() <= 0 {
		return forgebase.NewHookError("the total of a sales order must be positive to be submitted")
	}
	return enqueueLog(ctx, doc, "submitted")
}

// BeforeTransition applies the approval limit (see the [workflow] of doctypes/sales_order.toml): "Approve" is
// refused for an order whose total is above ApprovalLimit, and for one that has no total.
//
// Why two actions and not a role check. Hooks see the actor's name and the transition, never the actor's roles
// (ADR 0009, by design: a rule that depends on roles belongs in the permission rules). So the role is part of
// the action: "Approve" (Sales User and Sales Manager) is limited here, "Approve as Manager" (Sales Manager
// only, checked by the framework) has no limit and no hook rule. The veto message names the way out.
//
// doc is the stored order, so total is the value Validate computed when it entered Pending Approval (that
// draft-to-draft transition runs Validate). edit_roles on Pending Approval keeps a Sales User from lowering it
// afterwards.
func (OrderRules) BeforeTransition(ctx forgebase.HookContext, doc *forgebase.Document) error {
	if ctx.Transition == nil || ctx.Transition.Action != "Approve" {
		return nil
	}
	total := doc.Values["total"]
	switch {
	case total.Kind == forgebase.KindDecimal && total.Dec.Cmp(ApprovalLimit) > 0:
		return forgebase.NewHookError(fmt.Sprintf("orders above %s need a Sales Manager: use 'Approve as Manager'", ApprovalLimit))
	case total.Kind == forgebase.KindDecimal:
		return nil
	}
	// Defence in depth: Validate always computes total, so an order without one was changed outside the
	// framework. Without a total the limit cannot be applied, so the limited action must not pass.
	return forgebase.NewHookError("the order has no total, so the approval limit cannot be applied: use 'Approve as Manager'")
}

// BeforeCancel asks for the job that logs the cancel.
func (OrderRules) BeforeCancel(ctx forgebase.HookContext, doc *forgebase.Document) error {
	return enqueueLog(ctx, doc, "cancelled")
}

// enqueueLog enqueues sales.log_order_event for the order doc.
//
// This is the transactional outbox: the framework writes the job in the same transaction as the submit or
// cancel, after all hooks have run. If the operation is refused (by a later hook, by validation or by a
// version conflict) nothing is committed and no job exists. A hook cannot write the log itself: hooks have no
// database access, and the log must not exist for an operation that failed.
//
// The job itself (sales.log_order_event, which writes a sales_order_log row) is logOrderEvent below. This call
// runs inside the workflow transitions: a sales order has a workflow, so its submit and cancel are transitions
// (the "Approve" actions and "Cancel").
func enqueueLog(ctx forgebase.HookContext, doc *forgebase.Document, event string) error {
	if doc.Name == "" {
		return forgebase.NewHookError("the sales order has no name")
	}
	return ctx.Enqueue("sales.log_order_event", map[string]string{"order": doc.Name, "event": event})
}

// amountOf is qty * rate rounded to 2 places, or false when either is missing or not a number.
func amountOf(row forgebase.Row) (amount forgebase.Decimal, ok bool, err error) {
	qty, rate := row["qty"], row["rate"]
	if qty.Kind != forgebase.KindInt || rate.Kind != forgebase.KindDecimal {
		return forgebase.Decimal{}, false, nil
	}
	tooLarge := forgebase.NewHookError("an item amount is too large")
	product, fits := forgebase.DecimalFromInt(qty.Int).Mul(rate.Dec)
	if !fits {
		return forgebase.Decimal{}, false, tooLarge
	}
	// Rescale rounds half away from zero.
	if amount, fits = product.Rescale(2); !fits {
		return forgebase.Decimal{}, false, tooLarge
	}
	return amount, true, nil
}

// logOrderEvent writes one sales_order_log row for a lifecycle event of an order (submitted or cancelled). The
// payload is {"order": <name>, "event": <event>}.
//
// Delivery is at-least-once, so the handler must be safe to run twice: the log is keyed by <order>-<event>, and
// a row that already exists means an earlier run did the work.
func logOrderEvent(ctx context.Context, job *forgebase.JobContext) error {
	var payload struct {
		Order string `json:"order"`
		Event string `json:"event"`
	}
	if err := job.DecodePayload(&payload); err != nil || payload.Order == "" || payload.Event == "" {
		// A bad payload stays bad: retrying cannot fix it, so the job goes straight to dead (an operator sees
		// the message in `forgebase job list`).
		return forgebase.NewPermanentJobError(`the payload must be {"order": <name>, "event": <event>}`)
	}
	key := payload.Order + "-" + payload.Event
	documents := job.Documents()

	// Idempotency: the work was done by an earlier run of this job. This check keeps the normal path quiet; the
	// unique key and the re-check after a failed insert (below) cover two deliveries at the same moment.
	if found, err := documents.Get(ctx, "sales_order_log", key); err != nil || found != nil {
		return err
	}
	stored, err := documents.Get(ctx, "sales_order", payload.Order)
	if err != nil {
		return err
	}
	if stored == nil {
		// The order is gone (only a draft can be deleted: a submitted or cancelled order that has a log cannot,
		// because of the Link).
		return forgebase.NewPermanentJobError(fmt.Sprintf("the sales order %s does not exist", payload.Order))
	}
	logRow := forgebase.Document{Values: map[string]forgebase.Value{
		"key":         forgebase.Text(key),
		"sales_order": forgebase.Text(payload.Order),
		"event":       forgebase.Text(payload.Event),
	}}
	if total, ok := stored.Document.Values["total"]; ok {
		logRow.Values["total"] = total
	}
	// Runs as the system context with the actor job:sales.log_order_event: that is the log's owner and
	// modified_by. A refusal here is a permanent job error; a database error is retried.
	if _, err := documents.Insert(ctx, "sales_order_log", logRow); err != nil {
		// Two deliveries at the same moment: the other one committed the same key first and the unique name
		// refused this insert (a unique violation is permanent, like every refusal). The failed insert ran in a
		// savepoint, so the transaction is usable: if the log exists now, the work is done.
		if found, getErr := documents.Get(ctx, "sales_order_log", key); getErr == nil && found != nil {
			return nil
		}
		return err
	}
	return nil
}

// states are the workflow states of sales_order (see its [workflow]), the keys of the by_state object of
// sales.order_summary. A test checks that the DocType file names each of them.
var states = []string{"Draft", "Pending Approval", "Approved", "Cancelled"}

// orderSummary is the method sales.order_summary: how many sales orders there are, their total and how many
// are in each workflow state. The counts come from one query per state, so they are not one snapshot: an order
// that changes state while the method runs may be counted twice or not at all.
//
// Input (a JSON object, every key optional, any other key is a 400):
// {"customer": "<customer name>", "from": "YYYY-MM-DD", "to": "YYYY-MM-DD"}. from and to bound order_date, both
// included.
//
// Output: {"orders": 3, "total": "123.50", "by_state": {"Draft": 1, "Pending Approval": 0, "Approved": 2,
// "Cancelled": 0}}. total is a decimal as a string (never a float), the sum over every counted order whatever
// its state.
//
// As the caller. The orders are read through m.Documents(), which acts as the user who called, so the
// permission rules of sales_order apply: an order the caller may not read is not counted. (sales_order has no
// if_owner rule: a Sales User and a Sales Manager read every order. With such a rule the same code would count
// only the caller's own orders, with no change here.)
//
// No limit. Documents.List returns at most one page of 500 and has no offset, so the handler pages by name (the
// sort ends with it and it is unique): it asks for the orders whose name is greater than the last one it saw.
// The workflow state is a filter, one run per state, because the public StoredDocument does not carry it.
func orderSummary(ctx context.Context, m *forgebase.MethodContext) (any, error) {
	filters, err := summaryFilters(m.Body())
	if err != nil {
		return nil, err
	}
	documents := m.Documents()
	orders := 0
	total := forgebase.DecimalFromInt(0)
	byState := map[string]int{}
	for _, state := range states {
		count, sum, err := stateTotals(ctx, documents, filters, state)
		if err != nil {
			return nil, err
		}
		var fits bool
		if total, fits = total.Add(sum); !fits {
			return nil, forgebase.MethodUnprocessable("the total is too large")
		}
		orders += count
		byState[state] = count
	}
	total, _ = total.Rescale(2) // a sum of amounts of scale 2 fits again at scale 2
	return map[string]any{"orders": orders, "total": total.String(), "by_state": byState}, nil
}

// summaryFilter is a filter of the summary: field, operator, value.
type summaryFilter struct {
	field string
	op    forgebase.FilterOp
	value forgebase.Value
}

// summaryFilters checks the body of sales.order_summary and turns it into list filters. Every problem is a 400
// whose message says what to fix.
func summaryFilters(body json.RawMessage) ([]summaryFilter, error) {
	var input map[string]any
	if err := json.Unmarshal(body, &input); err != nil || input == nil {
		return nil, forgebase.MethodBadRequest("the body must be a JSON object")
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "customer" && key != "from" && key != "to" {
			return nil, forgebase.MethodBadRequest(fmt.Sprintf("unknown key '%s': the keys are customer, from and to", key))
		}
	}
	var filters []summaryFilter
	switch name := input["customer"].(type) {
	case nil:
	case string:
		if strings.TrimSpace(name) == "" {
			return nil, forgebase.MethodBadRequest("customer must be the name of a customer, a non-empty string")
		}
		filters = append(filters, summaryFilter{"customer", forgebase.FilterEq, forgebase.Text(name)})
	default:
		return nil, forgebase.MethodBadRequest("customer must be the name of a customer, a non-empty string")
	}
	var dates []time.Time
	for _, bound := range []struct {
		key string
		op  forgebase.FilterOp
	}{{"from", forgebase.FilterGe}, {"to", forgebase.FilterLe}} {
		notADate := forgebase.MethodBadRequest(bound.key + " must be a date like 2026-10-31")
		switch text := input[bound.key].(type) {
		case nil:
		case string:
			date, ok := parseDate(text)
			if !ok {
				return nil, notADate
			}
			dates = append(dates, date)
			filters = append(filters, summaryFilter{"order_date", bound.op, forgebase.NewDate(date.Year(), date.Month(), date.Day())})
		default:
			return nil, notADate
		}
	}
	if len(dates) == 2 && dates[0].After(dates[1]) {
		return nil, forgebase.MethodBadRequest("from must not be after to")
	}
	return filters, nil
}

// parseDate is a date written YYYY-MM-DD (four, two and two digits) that exists, or false.
func parseDate(text string) (time.Time, bool) {
	if len(text) != 10 || text[4] != '-' || text[7] != '-' {
		return time.Time{}, false
	}
	for i := 0; i < len(text); i++ {
		if i != 4 && i != 7 && (text[i] < '0' || text[i] > '9') {
			return time.Time{}, false
		}
	}
	date, err := time.Parse(time.DateOnly, text)
	return date, err == nil
}

// stateTotals is how many orders in state the caller can read (with the filters) and the sum of their totals,
// paging through the list by name.
func stateTotals(ctx context.Context, documents *forgebase.Documents, filters []summaryFilter, state string) (int, forgebase.Decimal, error) {
	count := 0
	sum := forgebase.DecimalFromInt(0)
	last := ""
	for {
		query := forgebase.NewListQuery().
			Filter("workflow_state", forgebase.FilterEq, forgebase.Text(state)).
			Limit(page)
		for _, f := range filters {
			query = query.Filter(f.field, f.op, f.value)
		}
		if last != "" {
			query = query.Filter("name", forgebase.FilterGt, forgebase.Text(last))
		}
		orders, err := documents.List(ctx, "sales_order", query)
		if err != nil {
			return 0, sum, err
		}
		for _, order := range orders {
			count++
			if amount := order.Document.Values["total"]; amount.Kind == forgebase.KindDecimal {
				var fits bool
				if sum, fits = sum.Add(amount.Dec); !fits {
					return 0, sum, forgebase.MethodUnprocessable("the total is too large")
				}
			}
		}
		if len(orders) < page {
			return count, sum, nil
		}
		last = orders[len(orders)-1].Name
	}
}

// page is the page Documents.List returns at most.
const page = 500

// maxOrdersPerDay is the most orders one daily summary counts (one less than a page, so a full page means
// "more"). An example's limit, see dailySummary.
const maxOrdersPerDay = page - 1

// dailySummary writes the sales_daily_summary of the day before the tick: how many orders were submitted with
// that order_date, and their total. The schedule (DailyAt(2, 0)) gives it the payload {"scheduled_for": "<the
// tick, RFC 3339 UTC>"}.
//
// Idempotent: the summary is named by its day, so a run that finds it does nothing. That also covers a tick
// enqueued twice by an operator (`forgebase job enqueue`). Two runs at the same moment: the unique name refuses
// the second insert, and the handler then finds the summary and succeeds.
//
// The summary is a snapshot: an order submitted later with a past order_date is not counted.
//
// Limit of this example. Documents.List returns one page of at most 500 documents and has no offset, so the
// example counts up to maxOrdersPerDay orders and fails permanently (it never writes a wrong figure) when there
// are more. A real app would sum in SQL (a data migration can create a view; the document API has no
// aggregate), or enqueue one job per page (sort by name and filter on the last name seen).
func dailySummary(ctx context.Context, job *forgebase.JobContext) error {
	var payload struct {
		ScheduledFor string `json:"scheduled_for"`
	}
	day, ok := time.Time{}, false
	if err := job.DecodePayload(&payload); err == nil {
		day, ok = dayBefore(payload.ScheduledFor)
	}
	if !ok {
		return forgebase.NewPermanentJobError(`the payload must be {"scheduled_for": <an RFC 3339 time>}`)
	}
	key := day.Format(time.DateOnly)
	documents := job.Documents()

	if found, err := documents.Get(ctx, "sales_daily_summary", key); err != nil || found != nil {
		return err
	}
	// docstatus is a standard column of a submittable DocType, so the database does the filtering: only
	// submitted orders (1), not drafts (0) or cancelled ones (2).
	query := forgebase.NewListQuery().
		Filter("order_date", forgebase.FilterEq, forgebase.NewDate(day.Year(), day.Month(), day.Day())).
		Filter("docstatus", forgebase.FilterEq, forgebase.Int(1)).
		Limit(page)
	orders, err := documents.List(ctx, "sales_order", query)
	if err != nil {
		return err
	}
	if len(orders) > maxOrdersPerDay {
		return forgebase.NewPermanentJobError(fmt.Sprintf("more than %d orders on %s: this example does not page", maxOrdersPerDay, key))
	}

	total := forgebase.DecimalFromInt(0)
	for _, order := range orders {
		if amount := order.Document.Values["total"]; amount.Kind == forgebase.KindDecimal {
			sum, fits := total.Add(amount.Dec)
			if !fits {
				return forgebase.NewPermanentJobError("the day's total is too large")
			}
			total = sum
		}
	}
	total, _ = total.Rescale(2)

	summary := forgebase.Document{Values: map[string]forgebase.Value{
		"day":    forgebase.Text(key),
		"orders": forgebase.Int(int64(len(orders))),
		"total":  forgebase.DecimalValue(total),
	}}
	if _, err := documents.Insert(ctx, "sales_daily_summary", summary); err != nil {
		// Another delivery committed this day first (the unique name refused the insert, in a savepoint): the
		// summary exists, so the work is done.
		if found, getErr := documents.Get(ctx, "sales_daily_summary", key); getErr == nil && found != nil {
			return nil
		}
		return err
	}
	return nil
}

// dayBefore is the calendar day before the day of an RFC 3339 time such as 2026-10-31T02:00:00Z.
//
// Only the date part is used, and the schedule is UTC, so the text is a UTC day already.
func dayBefore(rfc3339 string) (time.Time, bool) {
	if len(rfc3339) < 10 {
		return time.Time{}, false
	}
	day, err := time.Parse(time.DateOnly, rfc3339[:10])
	if err != nil {
		return time.Time{}, false
	}
	return day.AddDate(0, 0, -1), true
}
