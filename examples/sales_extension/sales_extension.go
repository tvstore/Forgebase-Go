// Package sales_extension is the sales_extension example app: a custom app on top of sales.
//
// It adds a DocType of its own (customer_visit, with a Link to sales' customer) and two hooks on a DocType of
// sales (sales_order), without changing anything in sales. The field it adds to customer (an overlay,
// P7) and the background job that plans the visit (P5) arrive with the phases that port those features.
package sales_extension

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
		Hooks("sales_order", FollowUp{})
	// Later phases add to this chain, in the order of the Rust example: the overlay
	// ExtendDocType("customer", ..) (P7) and the job plan_follow_up_visit (P5).
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
// The job sales_extension.plan_follow_up_visit is registered with App.Job in P5; until then nothing can reach
// this hook over HTTP or the command line, because a sales order has a workflow and its submit is a
// transition (P6).
type FollowUp struct{ forgebase.BaseHooks }

// BeforeSubmit enqueues the job with the name of the order.
func (FollowUp) BeforeSubmit(ctx forgebase.HookContext, doc *forgebase.Document) error {
	if doc.Name == "" {
		return forgebase.NewHookError("the sales order has no name")
	}
	return ctx.Enqueue("sales_extension.plan_follow_up_visit", map[string]string{"order": doc.Name})
}
