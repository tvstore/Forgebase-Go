// Package sales is the sales example app: customers and sales orders, in the role of the ERPNext sales module.
//
// It shows what an app is made of (DocTypes, hooks, a data migration) using the public forgebase API only.
// cmd/sales puts it into a binary; the module examples/sales_extension builds on it. The jobs, the schedule,
// the report, the method and the fixture of the Rust example arrive with the phases that port the
// framework features they use (README.md has the list).
package sales

import (
	"embed"
	"fmt"

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
		// Data migrations (SQL, append-only): see the comments in the file.
		Migrations(migrations)
	// Later phases add to this chain, in the order of the Rust example: the jobs log_order_event and
	// daily_summary and the schedule (P5), the report sales_by_customer (P6), the method order_summary
	// and the fixtures (P7).
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
// The job itself (sales.log_order_event, which writes a sales_order_log row) is registered with App.Job in P5;
// until then nothing can reach this call over HTTP or the command line, because a sales order has a workflow
// and its submit and cancel are transitions (P6).
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
