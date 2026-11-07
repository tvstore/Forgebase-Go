// Package sales_extension is the sales_extension example app: a custom app on top of sales.
//
// It adds a DocType of its own (customer_visit, with a Link to sales' customer) and two hooks on a DocType of
// sales (sales_order), without changing anything in sales, and a background job that works on sales documents
// (sales_extension.plan_follow_up_visit). The field it adds to customer (an overlay, P7) arrives with the phase
// that ports that feature.
package sales_extension

import (
	"context"
	"embed"
	"fmt"

	"github.com/tvstore/forgebase-go"
)

// Every file under doctypes/ that ends in .toml is embedded (a test checks that the directory and the embedded
// set are the same).
//
//go:embed doctypes/*.toml
var doctypes embed.FS

// MinOrderTotal is the smallest total of a sales order that may be saved: 10.00.
//
// A variable of the package and not a setting, because hooks have no database access: they cannot read a
// setting. A real deployment that needs a configurable minimum waits for the hook capabilities of a later
// milestone, or changes the value and rebuilds. It must not be changed at run time.
var MinOrderTotal = forgebase.NewDecimal(1000, 2)

// App is the app. It depends on sales, which must be added before it in main.
func App() *forgebase.App {
	return forgebase.NewApp("sales_extension").
		Version("0.1.0").
		DependsOn("sales").
		DocTypes(doctypes).
		// Hooks on a DocType of sales: allowed because we depend on it. They run after the hooks of sales, in
		// this order.
		Hooks("sales_order", MinimumOrder{}).
		Hooks("sales_order", FollowUp{}).
		// A job of this app, known as sales_extension.plan_follow_up_visit. The binary's worker runs it next to
		// the jobs of sales.
		Job("plan_follow_up_visit", planFollowUpVisit)
	// Later phases add to this chain, in the order of the Rust example: the overlay
	// ExtendDocType("customer", ..) (P7).
}

// MinimumOrder refuses a sales order whose total is below MinOrderTotal.
//
// In the composed binary, every save of a sales order (draft included) needs a total of at least 10.00. A
// draft without items (total 0) can therefore no longer be saved, unlike with sales alone.
type MinimumOrder struct{ forgebase.BaseHooks }

// Validate runs after the Validate hook of sales (hooks of one DocType run in app order), which computes
// total from the items. A missing or odd total counts as 0.
func (MinimumOrder) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	total := forgebase.DecimalFromInt(0)
	if v := doc.Values["total"]; v.Kind == forgebase.KindDecimal {
		total = v.Dec
	}
	if total.Cmp(MinOrderTotal) < 0 {
		return forgebase.NewHookError(fmt.Sprintf("a sales order must total at least %s", MinOrderTotal))
	}
	return nil
}

// FollowUp asks for a follow-up visit when a sales order is submitted.
//
// It runs after the BeforeSubmit of sales, which may refuse the submit; then this hook does not run and no
// job exists. The job is written in the submit's own transaction (the outbox), so it exists only if the
// submit committed. A second hook set of the same app for the same DocType is fine: hooks run in
// registration order.
//
// The job is planFollowUpVisit below. This hook runs inside the workflow transition that submits an order (the
// "Approve" actions of sales), because a sales order has a workflow.
type FollowUp struct{ forgebase.BaseHooks }

// BeforeSubmit enqueues the job with the name of the order.
func (FollowUp) BeforeSubmit(ctx forgebase.HookContext, doc *forgebase.Document) error {
	if doc.Name == "" {
		return forgebase.NewHookError("the sales order has no name")
	}
	return ctx.Enqueue("sales_extension.plan_follow_up_visit", map[string]string{"order": doc.Name})
}

// followUpDays is how many days after the order date the follow-up visit is planned.
const followUpDays = 7

// planFollowUpVisit plans a customer_visit for the customer of a submitted order, a week after its order_date.
// The payload is {"order": <name>}.
//
// It reads a DocType of sales through the documents handle, which is allowed for the apps of a binary (the
// handle is the system context: no permission rules).
//
//   - The date. A job has no clock rule, so the visit date comes from the order alone: the same order always
//     gives the same date, however late the job runs. An order without an order_date gets no visit: the job
//     ends done having done nothing.
//   - Idempotent. Delivery is at-least-once, so the visit remembers its order in source_order (unique), and the
//     job looks for it first. If two copies of the job ran at the very same moment, the unique field refuses
//     the second insert; the job then looks again, finds the other's visit and succeeds, so there are never two
//     visits. The look-up before the insert only keeps the normal path quiet.
func planFollowUpVisit(ctx context.Context, job *forgebase.JobContext) error {
	var payload struct {
		Order string `json:"order"`
	}
	if err := job.DecodePayload(&payload); err != nil || payload.Order == "" {
		return forgebase.NewPermanentJobError(`the payload must be {"order": <name>}`)
	}
	order := payload.Order
	documents := job.Documents()

	stored, err := documents.Get(ctx, "sales_order", order)
	if err != nil {
		return err
	}
	if stored == nil {
		return forgebase.NewPermanentJobError(fmt.Sprintf("the sales order %s does not exist", order))
	}
	customer, orderDate := stored.Document.Values["customer"], stored.Document.Values["order_date"]
	if customer.Kind != forgebase.KindText || orderDate.Kind != forgebase.KindDate {
		// No order date (or, impossible for a saved order, no customer): nothing to plan.
		return nil
	}
	visitedOn := orderDate.Time.AddDate(0, 0, followUpDays)

	// Idempotency: this order already has its visit.
	existing := forgebase.NewListQuery().Filter("source_order", forgebase.FilterEq, forgebase.Text(order)).Limit(1)
	if found, err := documents.List(ctx, "customer_visit", existing); err != nil || len(found) > 0 {
		return err
	}

	visit := forgebase.Document{Values: map[string]forgebase.Value{
		"customer":     customer,
		"visited_on":   forgebase.NewDate(visitedOn.Year(), visitedOn.Month(), visitedOn.Day()),
		"notes":        forgebase.Text("Follow-up of " + order),
		"source_order": forgebase.Text(order),
	}}
	if _, err := documents.Insert(ctx, "customer_visit", visit); err != nil {
		// Two deliveries at the same moment: the other committed this order's visit first and the unique
		// source_order refused this insert (in a savepoint, so the transaction is usable). Look again: if the
		// visit exists, the work is done.
		if found, listErr := documents.List(ctx, "customer_visit", existing); listErr == nil && len(found) > 0 {
			return nil
		}
		return err
	}
	return nil
}
