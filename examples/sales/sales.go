// Package sales is the sales example app: customers and sales orders, in the role of the ERPNext sales module.
//
// It shows what an app is made of (DocTypes, hooks, a data migration, background jobs and a schedule) using the
// public forgebase API only. cmd/sales puts it into a binary; the module examples/sales_extension builds on it.
// The report, the method and the fixture of the Rust example arrive with the phases that port the framework
// features they use (README.md has the list).
package sales

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/tvstore/forgebase-go"
)

// Every file under doctypes/ that ends in .toml is embedded (a test checks that the directory and the embedded
// set are the same).
//
//go:embed doctypes/*.toml
var doctypes embed.FS

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
		// Data migrations (SQL, append-only): see the comments in the file.
		Migrations(migrations)
	// Later phases add to this chain, in the order of the Rust example: the report sales_by_customer (P6), the
	// method order_summary and the fixtures (P7).
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
// The job itself (sales.log_order_event, which writes a sales_order_log row) is logOrderEvent below. Nothing
// can reach this call over HTTP or the command line before P6, because a sales order has a workflow and its
// submit and cancel are transitions.
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
