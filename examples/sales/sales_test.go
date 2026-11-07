package sales_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"

	"github.com/tvstore/forgebase-go/examples/sales"
)

// Tests of the sales app, ported from examples/sales/tests/sales.rs of the Rust implementation. The ones that
// need PostgreSQL (see README.md) fail, they do not skip, when FORGEBASE_TEST_DATABASE_URL is not set.
//
// Ported in P4 (the app model): the hooks without a database, the data migration, the computed amounts and
// total over HTTP and the level 1 field credit_limit. Ported in P5 (jobs): the job tests. Ported in P6 (workflow
// and reports): the workflow tests over HTTP (the transitions of the job tests included) and the report tests.
// Deferred, with the phase whose feature the Rust test uses:
//
//   - P7 (methods and fixtures): the_summary_states_are_the_workflow_states and the six order_summary tests
//     (the_summary_*), the_fixture_adopts_the_migrated_groups_and_inserts_online and
//     the_fixture_groups_are_usable_by_customers.

const orders = "/api/v1/resource/sales_order"

func dec(t *testing.T, text string) forgebase.Decimal {
	t.Helper()
	d, err := forgebase.ParseDecimal(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func decValue(t *testing.T, text string) forgebase.Value { return forgebase.DecimalValue(dec(t, text)) }

func item(qty, rate forgebase.Value) forgebase.Row {
	return forgebase.Row{"qty": qty, "rate": rate}
}

func order(items ...forgebase.Row) *forgebase.Document {
	return &forgebase.Document{Tables: map[string][]forgebase.Row{"items": items}}
}

func validate(t *testing.T, doc *forgebase.Document) {
	t.Helper()
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpInsert)
	if err := (sales.OrderRules{}).Validate(ctx, doc); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func total(t *testing.T, doc *forgebase.Document) forgebase.Decimal {
	t.Helper()
	v, ok := doc.Values["total"]
	if !ok || v.Kind != forgebase.KindDecimal {
		t.Fatalf("no total: %+v", v)
	}
	return v.Dec
}

func amount(doc *forgebase.Document, index int) forgebase.Value {
	return doc.Tables["items"][index]["amount"]
}

func equal(t *testing.T, got, want forgebase.Value, label string) {
	t.Helper()
	if !got.Equal(want) || got.Kind == forgebase.KindDecimal && got.Dec.String() != want.Dec.String() {
		t.Errorf("%s: %+v, want %+v", label, got, want)
	}
}

// ---- Without a database -----------------------------------------------------------------------

// Rust: sales.rs::every_doctype_file_is_listed
func TestEveryDocTypeFileIsEmbedded(t *testing.T) {
	fbtest.AssertDocTypesListed(t, sales.App(), "doctypes")
}

// Rust: sales.rs::the_app_fits_together
func TestTheAppFitsTogether(t *testing.T) {
	if err := forgebase.New().App(sales.App()).Check(); err != nil {
		t.Fatal(err)
	}
}

// Rust: sales.rs::amounts_and_total_are_computed
func TestAmountsAndTotalAreComputed(t *testing.T) {
	doc := order(
		item(forgebase.Int(3), decValue(t, "10.50")),
		item(forgebase.Int(2), decValue(t, "0.25")))
	validate(t, doc)
	equal(t, amount(doc, 0), decValue(t, "31.50"), "amount 0")
	equal(t, amount(doc, 1), decValue(t, "0.50"), "amount 1")
	if got := total(t, doc).String(); got != "32.00" {
		t.Errorf("total %s", got)
	}
}

// Rust: sales.rs::computed_values_overwrite_what_the_document_holds
func TestComputedValuesOverwriteWhatTheDocumentHolds(t *testing.T) {
	// The hook alone: a document that already holds values (a client cannot send them, they are read_only,
	// but a copy such as an amendment can carry them) gets them recomputed.
	row := item(forgebase.Int(1), decValue(t, "5.00"))
	row["amount"] = decValue(t, "999.00")
	doc := order(row)
	doc.Values = map[string]forgebase.Value{"total": decValue(t, "999.00")}
	validate(t, doc)
	equal(t, amount(doc, 0), decValue(t, "5.00"), "amount")
	if got := total(t, doc).String(); got != "5.00" {
		t.Errorf("total %s", got)
	}
}

// Rust: sales.rs::amounts_round_half_away_from_zero_to_two_places
func TestAmountsRoundHalfAwayFromZeroToTwoPlaces(t *testing.T) {
	// 3 * 0.005 = 0.015 -> 0.02 (half to even would give 0.02 too), 1 * 0.125 -> 0.13 (even: 0.12),
	// -1 * 0.125 -> -0.13, 1 * 2.344 -> 2.34.
	doc := order(
		item(forgebase.Int(3), decValue(t, "0.005")),
		item(forgebase.Int(1), decValue(t, "0.125")),
		item(forgebase.Int(-1), decValue(t, "0.125")),
		item(forgebase.Int(1), decValue(t, "2.344")))
	validate(t, doc)
	for i, want := range []string{"0.02", "0.13", "-0.13", "2.34"} {
		equal(t, amount(doc, i), decValue(t, want), fmt.Sprintf("amount %d", i))
	}
	if got := total(t, doc).String(); got != "2.36" {
		t.Errorf("total %s", got)
	}
}

// Rust: sales.rs::missing_or_odd_values_are_left_for_the_framework
func TestMissingOrOddValuesAreLeftForTheFramework(t *testing.T) {
	doc := order(
		item(forgebase.Null(), decValue(t, "1.00")),
		item(forgebase.Int(1), forgebase.Text("cheap")),
		forgebase.Row{},
		item(forgebase.Int(2), decValue(t, "4.00")))
	validate(t, doc)
	for i := 0; i < 3; i++ {
		equal(t, amount(doc, i), forgebase.Null(), fmt.Sprintf("row %d", i))
	}
	equal(t, amount(doc, 3), decValue(t, "8.00"), "row 3")
	if got := total(t, doc).String(); got != "8.00" {
		t.Errorf("total %s", got)
	}
}

// Not in Rust: a row that is null (a Rust Row cannot be); it gets its place and no amount.
func TestANilRowGetsNoAmount(t *testing.T) {
	doc := order(nil, item(forgebase.Int(2), decValue(t, "4.00")))
	validate(t, doc)
	equal(t, amount(doc, 0), forgebase.Null(), "nil row")
	equal(t, amount(doc, 1), decValue(t, "8.00"), "row 1")
}

// Rust: sales.rs::an_order_without_items_totals_zero
func TestAnOrderWithoutItemsTotalsZero(t *testing.T) {
	doc := &forgebase.Document{}
	validate(t, doc)
	if got := total(t, doc); got.String() != "0.00" || !got.IsZero() {
		t.Errorf("total %s", got)
	}
	// The items key exists (empty), like Rust's entry().or_default().
	if rows, ok := doc.Tables["items"]; !ok || len(rows) != 0 {
		t.Errorf("tables %v", doc.Tables)
	}
}

// Rust: sales.rs::an_overflowing_amount_is_an_error_not_a_panic
func TestAnOverflowingAmountIsAnErrorNotAPanic(t *testing.T) {
	maxDecimal, err := forgebase.ParseDecimal("79228162514264337593543950335")
	if err != nil {
		t.Fatal(err)
	}
	doc := order(item(forgebase.Int(1<<63-1), forgebase.DecimalValue(maxDecimal)))
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpInsert)
	err = (sales.OrderRules{}).Validate(ctx, doc)
	if err == nil || err.Error() != "an item amount is too large" {
		t.Errorf("%v", err)
	}
	// Amounts that fit but whose sum does not.
	big := forgebase.DecimalValue(dec(t, "39614081257132168796771975168")) // 2^95
	doc = order(item(forgebase.Int(1), big), item(forgebase.Int(1), big))
	err = (sales.OrderRules{}).Validate(ctx, doc)
	if err == nil {
		t.Error("an amount that does not fit at scale 2 is refused")
	}
}

// Rust: sales.rs::submit_is_refused_without_items
func TestSubmitIsRefusedWithoutItems(t *testing.T) {
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpSubmit)
	for _, doc := range []*forgebase.Document{{}, order()} {
		err := (sales.OrderRules{}).BeforeSubmit(ctx, doc)
		if err == nil || err.Error() != "a sales order needs at least one item to be submitted" {
			t.Errorf("%v", err)
		}
	}
}

// Rust: sales.rs::submit_is_refused_when_the_total_is_not_positive
func TestSubmitIsRefusedWhenTheTotalIsNotPositive(t *testing.T) {
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpSubmit)
	rules := sales.OrderRules{}
	for _, total := range []string{"0.00", "-5.00"} {
		doc := order(item(forgebase.Int(1), decValue(t, "1.00")))
		doc.Values = map[string]forgebase.Value{"total": decValue(t, total)}
		if err := rules.BeforeSubmit(ctx, doc); err == nil {
			t.Errorf("%s must be refused", total)
		}
	}
	// No total at all is refused too.
	doc := order(item(forgebase.Int(1), decValue(t, "1.00")))
	if err := rules.BeforeSubmit(ctx, doc); err == nil {
		t.Error("no total must be refused")
	}
	// A positive total passes. The hook also enqueues a job, so it needs a collector and a name.
	doc.Values = map[string]forgebase.Value{"total": decValue(t, "0.01")}
	doc.Name = "SO-2026-00001"
	jobs := forgebase.NewEnqueuedJobs()
	if err := rules.BeforeSubmit(ctx.WithJobs(jobs), doc); err != nil {
		t.Errorf("positive: %v", err)
	}
	// Without a collector the hook cannot enqueue, and says so.
	if err := rules.BeforeSubmit(ctx, doc); err == nil || !strings.Contains(err.Error(), "no job collector") {
		t.Errorf("%v", err)
	}
}

// Rust: sales.rs::submit_and_cancel_enqueue_the_log_job
func TestSubmitAndCancelEnqueueTheLogJob(t *testing.T) {
	jobs := forgebase.NewEnqueuedJobs()
	doc := order(item(forgebase.Int(1), decValue(t, "1.00")))
	doc.Name = "SO-2026-00001"
	validate(t, doc)

	rules := sales.OrderRules{}
	submit := forgebase.NewHookContext("sales_order", "ann", forgebase.OpSubmit).WithJobs(jobs)
	if err := rules.BeforeSubmit(submit, doc); err != nil {
		t.Fatalf("submit: %v", err)
	}
	cancel := forgebase.NewHookContext("sales_order", "max", forgebase.OpCancel).WithJobs(jobs)
	if err := rules.BeforeCancel(cancel, doc); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	enqueued := jobs.Take()
	if len(enqueued) != 2 {
		t.Fatalf("%d jobs", len(enqueued))
	}
	for i, event := range []string{"submitted", "cancelled"} {
		if enqueued[i].Name() != "sales.log_order_event" {
			t.Errorf("job %d is %s", i, enqueued[i].Name())
		}
		var payload map[string]any
		if err := json.Unmarshal(enqueued[i].Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{"order": "SO-2026-00001", "event": event}; !reflect.DeepEqual(payload, want) {
			t.Errorf("job %d payload %v", i, payload)
		}
	}
}

// Rust: sales.rs::a_refused_submit_enqueues_nothing
func TestARefusedSubmitEnqueuesNothing(t *testing.T) {
	jobs := forgebase.NewEnqueuedJobs()
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpSubmit).WithJobs(jobs)
	doc := order()
	doc.Name = "SO-2026-00001"
	if err := (sales.OrderRules{}).BeforeSubmit(ctx, doc); err == nil {
		t.Error("an order without items must be refused")
	}
	if got := jobs.Take(); len(got) != 0 {
		t.Errorf("%d jobs", len(got))
	}
}

func approveHook(t *testing.T, action, total string) error {
	t.Helper()
	info := forgebase.NewTransitionInfo(action, "Pending Approval", "Approved")
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpTransition).WithTransition(info)
	doc := &forgebase.Document{Values: map[string]forgebase.Value{"total": decValue(t, total)}}
	return (sales.OrderRules{}).BeforeTransition(ctx, doc)
}

// Rust: sales.rs::approve_is_limited_to_the_approval_limit_and_the_limit_itself_passes
func TestApproveIsLimitedToTheApprovalLimitAndTheLimitItselfPasses(t *testing.T) {
	if !sales.ApprovalLimit.Equal(dec(t, "10000.00")) || sales.ApprovalLimit.String() != "10000.00" {
		t.Errorf("limit %s", sales.ApprovalLimit)
	}
	if err := approveHook(t, "Approve", "10000.00"); err != nil {
		t.Errorf("the limit itself is allowed: %v", err)
	}
	err := approveHook(t, "Approve", "10000.01")
	if err == nil || err.Error() != "orders above 10000.00 need a Sales Manager: use 'Approve as Manager'" {
		t.Errorf("above the limit: %v", err)
	}
}

// Rust: sales.rs::only_the_approve_action_is_limited
func TestOnlyTheApproveActionIsLimited(t *testing.T) {
	for _, action := range []string{"Approve as Manager", "Reject", "Submit for Approval", "Cancel"} {
		if err := approveHook(t, action, "99999.00"); err != nil {
			t.Errorf("%s: %v", action, err)
		}
	}
}

// Not in Rust: the guard against an order that has no total, and the context without a transition (the
// Rust tests reach them through the workflow tests of P6).
func TestApproveWithoutATotalOrWithoutATransitionIsHandled(t *testing.T) {
	rules := sales.OrderRules{}
	info := forgebase.NewTransitionInfo("Approve", "Pending Approval", "Approved")
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpTransition).WithTransition(info)
	err := rules.BeforeTransition(ctx, &forgebase.Document{})
	if err == nil || err.Error() != "the order has no total, so the approval limit cannot be applied: use 'Approve as Manager'" {
		t.Errorf("%v", err)
	}
	// Another action never needs the total, and no transition at all is not a limited action.
	other := forgebase.NewHookContext("sales_order", "ann", forgebase.OpTransition).WithTransition(forgebase.NewTransitionInfo("Reject", "a", "b"))
	if err := rules.BeforeTransition(other, &forgebase.Document{}); err != nil {
		t.Error(err)
	}
	if err := rules.BeforeTransition(forgebase.NewHookContext("sales_order", "ann", forgebase.OpUpdate), &forgebase.Document{}); err != nil {
		t.Error(err)
	}
}

// ---- Against PostgreSQL -----------------------------------------------------------------------

type env struct {
	db       *fbtest.DB
	anon     *fbtest.Client
	user     *fbtest.Client
	manager  *fbtest.Client
	outsider *fbtest.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	binary := forgebase.New().App(sales.App())
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	db.CreateUser("ann", "Sales User")
	db.CreateUser("max", "Sales Manager")
	db.CreateUser("eve", "Accountant")
	client := db.Client(binary)
	return &env{db: db, anon: client, user: client.Login("ann"), manager: client.Login("max"), outsider: client.Login("eve")}
}

// at reads a JSON value by a path of object keys and array indexes; nil when it is not there.
func at(v any, path ...any) any {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[s]
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				return nil
			}
			v = l[s]
		}
	}
	return v
}

func expectStatus(t *testing.T, r *fbtest.Reply, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Text)
	}
}

func customer(t *testing.T, client *fbtest.Client) {
	t.Helper()
	// Only a Sales Manager creates customers; the caller passes the manager's client.
	expectStatus(t, client.Post("/api/v1/resource/customer", map[string]any{"email": "ada@example.com", "full_name": "Ada Lovelace"}), 201)
}

func twoItems() []any {
	return []any{
		map[string]any{"description": "Widget", "qty": 3, "rate": "10.50"},
		map[string]any{"description": "Gadget", "qty": 2, "rate": "0.25"},
	}
}

// Rust: sales.rs::the_migration_creates_the_groups_once (without the fixture, which arrives in P7: the
// Rust test also sees Online, owned by fixture:sales)
func TestTheMigrationCreatesTheGroupsOnce(t *testing.T) {
	binary := forgebase.New().App(sales.App())
	db := fbtest.NewDB(t)
	first, err := db.Migrate(binary, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []fbtest.AppliedMigration{{App: "sales", Version: 1, Name: "default_customer_groups"}}; !reflect.DeepEqual(first.AppMigrations, want) {
		t.Errorf("%+v", first.AppMigrations)
	}
	groups := db.Rows(`SELECT name, title, owner, version::text FROM doc_customer_group ORDER BY name COLLATE "C"`)
	if want := [][]string{{"Retail", "Retail", "system", "1"}, {"Wholesale", "Wholesale", "system", "1"}}; !reflect.DeepEqual(groups, want) {
		t.Errorf("%v", groups)
	}

	// A second migrate is a no-op, and the migration is recorded once.
	second, err := db.Migrate(binary, false)
	if err != nil || len(second.AppMigrations) != 0 {
		t.Fatalf("%+v %v", second, err)
	}
	if got, want := db.Rows("SELECT app, version::text, name FROM fb_app_migration"), [][]string{{"sales", "1", "default_customer_groups"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got := db.Rows("SELECT count(*)::text FROM doc_customer_group"); got[0][0] != "2" {
		t.Errorf("%v", got)
	}
}

// Rust: sales.rs::the_groups_are_usable_and_the_sql_is_idempotent (two groups here, three with the fixture)
func TestTheGroupsAreUsableAndTheSQLIsIdempotent(t *testing.T) {
	e := newEnv(t)
	// The statement tolerates a group that exists (here: run again by hand).
	sql, err := os.ReadFile("migrations/0001_default_customer_groups.sql")
	if err != nil {
		t.Fatal(err)
	}
	e.db.Exec(string(sql))
	if got := e.db.Rows("SELECT count(*)::text FROM doc_customer_group"); got[0][0] != "2" {
		t.Errorf("%v", got)
	}

	// A row written by the migration is a real document: a customer can link to it.
	reply := e.manager.Post("/api/v1/resource/customer", map[string]any{"email": "ada@example.com", "full_name": "Ada", "customer_group": "Retail"})
	expectStatus(t, reply, 201)
	if at(reply.Data(), "customer_group") != "Retail" {
		t.Errorf("%s", reply.Text)
	}
}

// Rust: sales.rs::an_order_gets_its_amounts_and_total_and_an_update_recomputes_them
func TestAnOrderGetsItsAmountsAndTotalAndAnUpdateRecomputesThem(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)

	// A client cannot send a total: it is read_only and refused (422), not silently replaced.
	refused := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": twoItems(), "total": "1.00"})
	expectStatus(t, refused, 422)
	if refused.ErrorCode() != "validation_failed" {
		t.Errorf("%s", refused.Text)
	}
	// Nor an amount in a row.
	rows := twoItems()
	rows[0].(map[string]any)["amount"] = "1.00"
	expectStatus(t, e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": rows}), 422)

	// Without them, the amounts and the total are computed.
	created := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": twoItems()})
	expectStatus(t, created, 201)
	data := created.Data()
	if at(data, "total") != "32.00" || at(data, "items", 0, "amount") != "31.50" || at(data, "items", 1, "amount") != "0.50" {
		t.Errorf("%s", created.Text)
	}
	name := at(data, "name").(string)

	// A new quantity changes the amount and the total.
	updated := e.user.Patch(orders+"/"+name, map[string]any{"version": 1, "data": map[string]any{"items": []any{
		map[string]any{"description": "Widget", "qty": 4, "rate": "10.50"},
	}}})
	expectStatus(t, updated, 200)
	if at(updated.Data(), "total") != "42.00" || at(updated.Data(), "items", 0, "amount") != "42.00" {
		t.Errorf("%s", updated.Text)
	}
}

// transition takes the transition action on the order name (at version) as client.
func transition(client *fbtest.Client, name, action string, version int) *fbtest.Reply {
	return client.Post(orders+"/"+name+"/transition", map[string]any{"action": action, "version": version})
}

// approved sends the draft name (version 1) for approval and approves it, both as client: the order is Approved
// (submitted) at version 3.
func approved(t *testing.T, client *fbtest.Client, name string) {
	t.Helper()
	sent := transition(client, name, "Submit for Approval", 1)
	expectStatus(t, sent, 200)
	done := transition(client, name, "Approve", 2)
	expectStatus(t, done, 200)
	if at(done.Data(), "workflow_state") != "Approved" || at(done.Data(), "docstatus") != float64(1) {
		t.Fatalf("%s", done.Text)
	}
}

// Rust: sales.rs::a_draft_may_have_no_items_but_cannot_be_submitted
func TestADraftMayHaveNoItemsButCannotBeSubmitted(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	created := e.user.Post(orders, map[string]any{"customer": "ada@example.com"})
	expectStatus(t, created, 201)
	if at(created.Data(), "total") != "0.00" {
		t.Errorf("%s", created.Text)
	}
	name := at(created.Data(), "name").(string)

	// Sending the order for approval is a draft-to-draft move and needs no items. Approving submits it, and the
	// framework reports the hook error as 422 hook_rejected.
	expectStatus(t, transition(e.user, name, "Submit for Approval", 1), 200)
	refused := transition(e.user, name, "Approve", 2)
	expectStatus(t, refused, 422)
	if refused.ErrorCode() != "hook_rejected" || !strings.Contains(refused.ErrorMessage(), "a sales order needs at least one item to be submitted") {
		t.Errorf("%s", refused.Text)
	}
	// Nothing changed: still pending approval and a draft, same version.
	read := e.user.Get(orders + "/" + name)
	if at(read.Data(), "docstatus") != float64(0) || at(read.Data(), "workflow_state") != "Pending Approval" || at(read.Data(), "version") != float64(2) {
		t.Errorf("%s", read.Text)
	}
}

// Rust: sales.rs::an_order_with_items_can_be_submitted
func TestAnOrderWithItemsCanBeSubmitted(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	created := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": twoItems()})
	name := at(created.Data(), "name").(string)

	approved(t, e.user, name)
	read := e.user.Get(orders + "/" + name)
	if at(read.Data(), "docstatus") != float64(1) || at(read.Data(), "total") != "32.00" {
		t.Errorf("%s", read.Text)
	}
}

// Rust: sales.rs::permissions_are_enforced
func TestPermissionsAreEnforced(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	created := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": twoItems()})
	name := at(created.Data(), "name").(string)
	approved(t, e.user, name)

	// A Sales User cannot cancel (no right, no role); a Sales Manager can.
	denied := transition(e.user, name, "Cancel", 3)
	expectStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%s", denied.Text)
	}
	cancelled := transition(e.manager, name, "Cancel", 3)
	expectStatus(t, cancelled, 200)
	if at(cancelled.Data(), "docstatus") != float64(2) {
		t.Errorf("%s", cancelled.Text)
	}

	// A user without a sales role cannot read orders, neither one nor the list.
	expectStatus(t, e.outsider.Get(orders+"/"+name), 403)
	expectStatus(t, e.outsider.Get(orders), 403)
}

// ---- credit_limit is a level 1 field (M10.2, ADR 0024) ----------------------------------------

const customers = "/api/v1/resource/customer"

// customerWithLimit makes a customer with a credit limit, by the manager; the name is the email.
func customerWithLimit(t *testing.T, e *env) {
	t.Helper()
	created := e.manager.Post(customers, map[string]any{"email": "ada@example.com", "full_name": "Ada Lovelace", "credit_limit": "1250.50"})
	expectStatus(t, created, 201)
	if at(created.Data(), "credit_limit") != "1250.50" {
		t.Fatalf("%s", created.Text)
	}
}

func fieldNames(meta *fbtest.Reply) []string {
	var names []string
	for _, f := range at(meta.Data(), "fields").([]any) {
		names = append(names, f.(map[string]any)["name"].(string))
	}
	return names
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Rust: sales.rs::a_sales_user_does_not_see_the_credit_limit_anywhere
func TestASalesUserDoesNotSeeTheCreditLimitAnywhere(t *testing.T) {
	e := newEnv(t)
	customerWithLimit(t, e)

	got := e.user.Get(customers + "/ada@example.com")
	expectStatus(t, got, 200)
	if _, present := got.DataObject()["credit_limit"]; present || at(got.Data(), "full_name") != "Ada Lovelace" {
		t.Errorf("%s", got.Text)
	}

	list := e.user.Get(customers)
	expectStatus(t, list, 200)
	if len(list.DataList()) != 1 || strings.Contains(list.Text, "credit_limit") || strings.Contains(list.Text, "1250") {
		t.Errorf("%s", list.Text)
	}

	meta := e.user.Get("/api/v1/meta/customer")
	expectStatus(t, meta, 200)
	if has(fieldNames(meta), "credit_limit") || !has(fieldNames(meta), "loyalty_points") {
		t.Errorf("%v", fieldNames(meta))
	}
}

func filterQuery(field string) string {
	filters, _ := json.Marshal([]map[string]string{{"field": field, "op": ">", "value": "1"}})
	return "?filters=" + url.QueryEscape(string(filters))
}

// Rust: sales.rs::a_sales_user_cannot_filter_by_the_credit_limit
func TestASalesUserCannotFilterByTheCreditLimit(t *testing.T) {
	e := newEnv(t)
	customerWithLimit(t, e)
	refused := e.user.Get(customers + filterQuery("credit_limit"))
	expectStatus(t, refused, 400)
	if refused.ErrorCode() != "invalid_query" {
		t.Errorf("%s", refused.Text)
	}
	// Refused like a name that is no field at all: no oracle for the hidden field.
	other := e.user.Get(customers + filterQuery("nonsense"))
	expectStatus(t, other, 400)
	if strings.ReplaceAll(refused.ErrorMessage(), "credit_limit", "nonsense") != other.ErrorMessage() {
		t.Errorf("%q and %q", refused.ErrorMessage(), other.ErrorMessage())
	}
	// The manager may.
	allowed := e.manager.Get(customers + filterQuery("credit_limit"))
	expectStatus(t, allowed, 200)
	if len(allowed.DataList()) != 1 {
		t.Errorf("%s", allowed.Text)
	}
}

// Rust: sales.rs::a_sales_user_cannot_send_the_credit_limit
func TestASalesUserCannotSendTheCreditLimit(t *testing.T) {
	e := newEnv(t)
	customerWithLimit(t, e)
	// A Sales User has no write right on customer at all, so the right is refused first (403) and the field
	// is never looked at: sending credit_limit changes nothing, and neither does any other field.
	for _, data := range []map[string]any{{"credit_limit": "1.00"}, {"loyalty_points": 9}} {
		denied := e.user.Patch(customers+"/ada@example.com", map[string]any{"version": 1, "data": data})
		expectStatus(t, denied, 403)
		if denied.ErrorCode() != "permission_denied" {
			t.Errorf("%s", denied.Text)
		}
	}
	read := e.manager.Get(customers + "/ada@example.com")
	if at(read.Data(), "credit_limit") != "1250.50" || at(read.Data(), "version") != float64(1) {
		t.Errorf("%s", read.Text)
	}
}

// Rust: sales.rs::a_sales_manager_reads_and_writes_the_credit_limit
func TestASalesManagerReadsAndWritesTheCreditLimit(t *testing.T) {
	e := newEnv(t)
	customerWithLimit(t, e)
	got := e.manager.Get(customers + "/ada@example.com")
	if at(got.Data(), "credit_limit") != "1250.50" {
		t.Errorf("%s", got.Text)
	}
	meta := e.manager.Get("/api/v1/meta/customer")
	if !has(fieldNames(meta), "credit_limit") {
		t.Errorf("%v", fieldNames(meta))
	}
	for _, f := range at(meta.Data(), "fields").([]any) {
		if f.(map[string]any)["name"] == "credit_limit" && f.(map[string]any)["read_only"] != false {
			t.Errorf("%v", f)
		}
	}
	updated := e.manager.Patch(customers+"/ada@example.com", map[string]any{"version": 1, "data": map[string]any{"credit_limit": "3000.00"}})
	expectStatus(t, updated, 200)
	if at(updated.Data(), "credit_limit") != "3000.00" {
		t.Errorf("%s", updated.Text)
	}
}

// Rust: sales.rs::an_update_without_the_credit_limit_keeps_the_stored_value
func TestAnUpdateWithoutTheCreditLimitKeepsTheStoredValue(t *testing.T) {
	e := newEnv(t)
	customerWithLimit(t, e)
	// The manager changes another field: the limit stays.
	updated := e.manager.Patch(customers+"/ada@example.com", map[string]any{"version": 1, "data": map[string]any{"loyalty_points": 5}})
	expectStatus(t, updated, 200)
	if at(updated.Data(), "credit_limit") != "1250.50" {
		t.Errorf("%s", updated.Text)
	}
	read := e.manager.Get(customers + "/ada@example.com")
	if at(read.Data(), "credit_limit") != "1250.50" || at(read.Data(), "loyalty_points") != float64(5) {
		t.Errorf("%s", read.Text)
	}
}

// ---- The approval workflow --------------------------------------------------------------------

// logOf is the fb_workflow_log rows of an order as "action: from -> to by actor", oldest first.
func logOf(e *env, name string) []string {
	var out []string
	for _, r := range e.db.Rows(fmt.Sprintf(`SELECT action || ': ' || from_state || ' -> ' || to_state || ' by ' || actor
		FROM fb_workflow_log WHERE doctype = 'sales_order' AND name = '%s' ORDER BY id`, name)) {
		out = append(out, r[0])
	}
	return out
}

func sameStrings(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 10 000.01: one cent above ApprovalLimit.
func bigItems() []any {
	return []any{map[string]any{"description": "Machine", "qty": 1, "rate": "10000.01"}}
}

// Rust: sales.rs::an_order_goes_from_draft_to_cancelled_through_the_workflow
func TestAnOrderGoesFromDraftToCancelledThroughTheWorkflow(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	read := e.user.Get(orders + "/" + name)
	if at(read.Data(), "workflow_state") != "Draft" || at(read.Data(), "docstatus") != float64(0) {
		t.Fatalf("%s", read.Text)
	}

	// What the Sales User may do now.
	offered := e.user.Get(orders + "/" + name + "/transitions")
	expectStatus(t, offered, 200)
	if !reflect.DeepEqual(at(offered.Data(), "transitions"), []any{map[string]any{"action": "Submit for Approval", "to": "Pending Approval"}}) {
		t.Errorf("%s", offered.Text)
	}

	sent := transition(e.user, name, "Submit for Approval", 1)
	expectStatus(t, sent, 200)
	if at(sent.Data(), "workflow_state") != "Pending Approval" || at(sent.Data(), "docstatus") != float64(0) {
		t.Errorf("%s", sent.Text)
	}
	done := transition(e.user, name, "Approve", 2)
	expectStatus(t, done, 200)
	if at(done.Data(), "workflow_state") != "Approved" || at(done.Data(), "docstatus") != float64(1) {
		t.Errorf("%s", done.Text)
	}
	cancelled := transition(e.manager, name, "Cancel", 3)
	expectStatus(t, cancelled, 200)
	if at(cancelled.Data(), "workflow_state") != "Cancelled" || at(cancelled.Data(), "docstatus") != float64(2) {
		t.Errorf("%s", cancelled.Text)
	}

	// Every move left a log row, written with the transition; the API shows the same history.
	sameStrings(t, logOf(e, name),
		"Submit for Approval: Draft -> Pending Approval by ann",
		"Approve: Pending Approval -> Approved by ann",
		"Cancel: Approved -> Cancelled by max")
	history := e.user.Get(orders + "/" + name + "/history")
	if len(at(history.Data(), "history").([]any)) != 3 {
		t.Errorf("%s", history.Text)
	}

	// The hooks of the submit and the cancel ran inside the transitions: both jobs exist.
	jobs := e.db.Rows("SELECT payload->>'event' FROM fb_job WHERE name = 'sales.log_order_event' ORDER BY 1")
	if !reflect.DeepEqual(jobs, [][]string{{"cancelled"}, {"submitted"}}) {
		t.Errorf("%v", jobs)
	}
}

// Rust: sales.rs::plain_submit_and_cancel_are_refused_on_an_order
func TestPlainSubmitAndCancelAreRefusedOnAnOrder(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	refused := e.user.Post(orders+"/"+name+"/submit", map[string]any{"version": 1})
	expectStatus(t, refused, 409)
	if refused.ErrorCode() != "workflow_required" {
		t.Errorf("%s", refused.Text)
	}

	approved(t, e.user, name)
	refused = e.manager.Post(orders+"/"+name+"/cancel", map[string]any{"version": 3})
	expectStatus(t, refused, 409)
	if refused.ErrorCode() != "workflow_required" {
		t.Errorf("%s", refused.Text)
	}
	if read := e.user.Get(orders + "/" + name); at(read.Data(), "docstatus") != float64(1) {
		t.Errorf("%s", read.Text)
	}
}

// Rust: sales.rs::a_sales_user_approves_up_to_the_limit_and_a_manager_above_it
func TestASalesUserApprovesUpToTheLimitAndAManagerAboveIt(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", bigItems())
	sent := transition(e.user, name, "Submit for Approval", 1)
	expectStatus(t, sent, 200)
	if at(sent.Data(), "total") != "10000.01" {
		t.Errorf("%s", sent.Text)
	}

	// The hook vetoes: the message names the way out, and nothing changed (state, version, log, jobs).
	vetoed := transition(e.user, name, "Approve", 2)
	expectStatus(t, vetoed, 422)
	if vetoed.ErrorCode() != "hook_rejected" || !strings.Contains(vetoed.ErrorMessage(), "need a Sales Manager: use 'Approve as Manager'") {
		t.Errorf("%s", vetoed.Text)
	}
	read := e.user.Get(orders + "/" + name)
	if at(read.Data(), "workflow_state") != "Pending Approval" || at(read.Data(), "version") != float64(2) {
		t.Errorf("%s", read.Text)
	}
	sameStrings(t, logOf(e, name), "Submit for Approval: Draft -> Pending Approval by ann")
	if n := scalar(e, "SELECT count(*)::text FROM fb_job"); n != "0" {
		t.Errorf("%s jobs", n)
	}

	// The manager's action is not open to a Sales User: it needs the role.
	denied := transition(e.user, name, "Approve as Manager", 2)
	expectStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%s", denied.Text)
	}

	// The Sales Manager approves the large order with the action that has no limit.
	done := transition(e.manager, name, "Approve as Manager", 2)
	expectStatus(t, done, 200)
	if at(done.Data(), "workflow_state") != "Approved" || at(done.Data(), "docstatus") != float64(1) {
		t.Errorf("%s", done.Text)
	}
	log := logOf(e, name)
	if log[len(log)-1] != "Approve as Manager: Pending Approval -> Approved by max" {
		t.Errorf("%q", log)
	}

	// An order of exactly the limit is a Sales User's to approve.
	exact := draft(t, e, "", []any{map[string]any{"description": "Machine", "qty": 1, "rate": "10000.00"}})
	approved(t, e.user, exact)
}

// Rust: sales.rs::a_rejected_order_is_a_draft_again_and_editable
func TestARejectedOrderIsADraftAgainAndEditable(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	transition(e.user, name, "Submit for Approval", 1)

	// While it waits for approval only a Sales Manager may edit it (edit_roles): a Sales User is refused, for
	// the remarks and for the items alike.
	denied := e.user.Patch(orders+"/"+name, map[string]any{"version": 2, "data": map[string]any{"remarks": "ring me"}})
	expectStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%s", denied.Text)
	}
	denied = e.user.Patch(orders+"/"+name, map[string]any{"version": 2, "data": map[string]any{
		"items": []any{map[string]any{"description": "Cheaper", "qty": 1, "rate": "1.00"}}}})
	expectStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%s", denied.Text)
	}

	// A Sales Manager may edit it (version 2 to 3).
	edited := e.manager.Patch(orders+"/"+name, map[string]any{"version": 2, "data": map[string]any{"remarks": "checked by the manager"}})
	expectStatus(t, edited, 200)
	if at(edited.Data(), "remarks") != "checked by the manager" || at(edited.Data(), "workflow_state") != "Pending Approval" {
		t.Errorf("%s", edited.Text)
	}

	// Only a Sales Manager rejects.
	denied = transition(e.user, name, "Reject", 3)
	expectStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%s", denied.Text)
	}
	rejected := transition(e.manager, name, "Reject", 3)
	expectStatus(t, rejected, 200)
	if at(rejected.Data(), "workflow_state") != "Draft" || at(rejected.Data(), "docstatus") != float64(0) {
		t.Errorf("%s", rejected.Text)
	}

	// Back in Draft the Sales User edits it again.
	updated := e.user.Patch(orders+"/"+name, map[string]any{"version": 4, "data": map[string]any{"remarks": "ring me"}})
	expectStatus(t, updated, 200)
	if at(updated.Data(), "remarks") != "ring me" {
		t.Errorf("%s", updated.Text)
	}
}

// Rust: sales.rs::an_order_without_a_total_cannot_be_approved_with_the_limited_action
func TestAnOrderWithoutATotalCannotBeApprovedWithTheLimitedAction(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	expectStatus(t, transition(e.user, name, "Submit for Approval", 1), 200)
	// Validate always computes the total; an order without one was changed outside the framework (here: in
	// SQL).
	e.db.Exec(fmt.Sprintf("UPDATE doc_sales_order SET total = NULL WHERE name = '%s'", name))
	vetoed := transition(e.user, name, "Approve", 2)
	expectStatus(t, vetoed, 422)
	if vetoed.ErrorCode() != "hook_rejected" || !strings.Contains(vetoed.ErrorMessage(), "the order has no total") {
		t.Errorf("%s", vetoed.Text)
	}
	if read := e.user.Get(orders + "/" + name); at(read.Data(), "workflow_state") != "Pending Approval" {
		t.Errorf("%s", read.Text)
	}
}

// Rust: sales.rs::a_wrong_state_and_an_unknown_action_are_refused
func TestAWrongStateAndAnUnknownActionAreRefused(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	// "Approve" is not a transition from Draft; "Fly" is no action at all.
	for _, action := range []string{"Approve", "Fly"} {
		refused := transition(e.user, name, action, 1)
		expectStatus(t, refused, 409)
		if refused.ErrorCode() != "unknown_transition" {
			t.Errorf("%s", refused.Text)
		}
	}
	// A user without a sales role cannot even see the order.
	expectStatus(t, transition(e.outsider, name, "Submit for Approval", 1), 403)
}

// ---- The report "Sales by customer" ------------------------------------------------------------

const report = "/api/v1/report/sales.sales_by_customer"

// orderOf makes an order of customer on date with one item of 1 x rate, approved when approve.
func orderOf(t *testing.T, e *env, customer, date, rate string, approve bool) string {
	t.Helper()
	created := e.user.Post(orders, map[string]any{"customer": customer, "order_date": date,
		"items": []any{map[string]any{"description": "Item", "qty": 1, "rate": rate}}})
	expectStatus(t, created, 201)
	name := at(created.Data(), "name").(string)
	if approve {
		approved(t, e.user, name)
	}
	return name
}

// Rust: sales.rs::the_report_sql_fits_its_declaration
func TestTheReportSQLFitsItsDeclaration(t *testing.T) {
	e := newEnv(t)
	if problems := fbtest.CheckReports(binary(), e.db); len(problems) != 0 {
		t.Errorf("%v", problems)
	}
}

// Rust: sales.rs::sales_by_customer_counts_the_approved_orders_of_the_period
func TestSalesByCustomerCountsTheApprovedOrdersOfThePeriod(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	expectStatus(t, e.manager.Post("/api/v1/resource/customer", map[string]any{"email": "bob@example.com", "full_name": "Bob"}), 201)

	// Counted: two orders of ada (12.00 and 5.00) and one of bob (40.00) in October.
	orderOf(t, e, "ada@example.com", "2026-10-01", "12.00", true)
	orderOf(t, e, "ada@example.com", "2026-10-05", "5.00", true)
	orderOf(t, e, "bob@example.com", "2026-10-03", "40.00", true)
	// Not counted: a draft, a pending order, a cancelled order, an order outside the period.
	orderOf(t, e, "ada@example.com", "2026-10-06", "100.00", false)
	pending := orderOf(t, e, "ada@example.com", "2026-10-07", "100.00", false)
	expectStatus(t, transition(e.user, pending, "Submit for Approval", 1), 200)
	cancelled := orderOf(t, e, "ada@example.com", "2026-10-08", "100.00", true)
	expectStatus(t, transition(e.manager, cancelled, "Cancel", 3), 200)
	orderOf(t, e, "ada@example.com", "2026-12-01", "100.00", true)

	// Biggest customer first.
	run := e.manager.Post(report, map[string]any{"params": map[string]any{"from": "2026-10-01", "to": "2026-10-31"}})
	expectStatus(t, run, 200)
	var columns []string
	for _, c := range at(run.Data(), "columns").([]any) {
		columns = append(columns, c.(map[string]any)["name"].(string))
	}
	sameStrings(t, columns, "customer", "orders", "total", "last_order")
	want := []any{
		[]any{"bob@example.com", float64(1), "40.00", "2026-10-03"},
		[]any{"ada@example.com", float64(2), "17.00", "2026-10-05"},
	}
	if !reflect.DeepEqual(at(run.Data(), "rows"), want) || at(run.Data(), "truncated") != false {
		t.Errorf("%s", run.Text)
	}

	// The optional customer narrows it; the dates are required.
	one := e.manager.Post(report, map[string]any{"params": map[string]any{"from": "2026-10-01", "to": "2026-10-31", "customer": "bob@example.com"}})
	if !reflect.DeepEqual(at(one.Data(), "rows"), []any{want[0]}) {
		t.Errorf("%s", one.Text)
	}
	expectStatus(t, e.manager.Post(report, map[string]any{"params": map[string]any{"from": "2026-10-01"}}), 422)
}

// Rust: sales.rs::a_sales_user_cannot_run_or_see_the_report
func TestASalesUserCannotRunOrSeeTheReport(t *testing.T) {
	e := newEnv(t)
	params := map[string]any{"params": map[string]any{"from": "2026-10-01", "to": "2026-10-31"}}
	// Not allowed looks like not existing (404), for the Sales User and for a stranger.
	for _, client := range []*fbtest.Client{e.user, e.outsider} {
		expectStatus(t, client.Post(report, params), 404)
		listed := client.Get("/api/v1/report")
		expectStatus(t, listed, 200)
		if len(listed.DataList()) != 0 {
			t.Errorf("%s", listed.Text)
		}
	}
	listed := e.manager.Get("/api/v1/report")
	if at(listed.Data(), 0, "name") != "sales.sales_by_customer" || at(listed.Data(), 0, "label") != "Sales by customer" {
		t.Errorf("%s", listed.Text)
	}
}

// ---- Jobs and the schedule (P5) ---------------------------------------------------------------

func binary() *forgebase.Binary { return forgebase.New().App(sales.App()) }

// draft creates an order of items (and order_date when given) as the Sales User and returns its name. The first
// item is 3 x 10.50 and the second 2 x 0.25, so twoItems totals 32.00.
func draft(t *testing.T, e *env, orderDate string, items []any) string {
	t.Helper()
	body := map[string]any{"customer": "ada@example.com", "items": items}
	if orderDate != "" {
		body["order_date"] = orderDate
	}
	created := e.user.Post(orders, body)
	expectStatus(t, created, 201)
	return at(created.Data(), "name").(string)
}

// queueJob queues a job as the test.
func queueJob(e *env, name, payload string) {
	e.db.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by) "+
		"VALUES (gen_random_uuid(), '%s', '%s', 'queued', 'test')", name, payload))
}

// duplicateJobs copies the job rows named name into new queued rows: the same job delivered twice.
func duplicateJobs(e *env, name string) {
	e.db.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by) "+
		"SELECT gen_random_uuid(), name, payload, 'queued', 'test' FROM fb_job WHERE name = '%s'", name))
}

func scalar(e *env, sql string) string { return e.db.Rows(sql)[0][0] }

func run(counts fbtest.JobRun) [3]int { return [3]int{counts.Done, counts.Retried, counts.Dead} }

// Rust: sales.rs::a_submit_and_a_cancel_are_logged_by_a_job
func TestASubmitAndACancelAreLoggedByAJob(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	approved(t, e.user, name)

	// The outbox: the job exists, queued, as soon as the submit committed.
	if queued := e.db.Rows("SELECT name, status, enqueued_by FROM fb_job"); !reflect.DeepEqual(queued, [][]string{{"sales.log_order_event", "queued", "ann"}}) {
		t.Errorf("%v", queued)
	}

	got := fbtest.RunJobs(binary(), e.db)
	if run(got) != [3]int{1, 0, 0} {
		t.Fatalf("%+v", got)
	}
	logs := e.db.Rows("SELECT name, event, total::text, owner, modified_by FROM doc_sales_order_log")
	want := [][]string{{name + "-submitted", "submitted", "32.00", "job:sales.log_order_event", "job:sales.log_order_event"}}
	if !reflect.DeepEqual(logs, want) {
		t.Errorf("%v, want %v", logs, want)
	}
	// The log links to the order, and a Sales User can read it but not change it.
	read := e.user.Get("/api/v1/resource/sales_order_log/" + name + "-submitted")
	expectStatus(t, read, 200)
	if at(read.Data(), "sales_order") != name {
		t.Errorf("%s", read.Text)
	}
	expectStatus(t, e.user.Post("/api/v1/resource/sales_order_log", map[string]any{"key": "x", "sales_order": name}), 403)

	expectStatus(t, transition(e.manager, name, "Cancel", 3), 200)
	if got := fbtest.RunJobs(binary(), e.db); got.Done != 1 {
		t.Fatalf("%+v", got)
	}
	events := e.db.Rows(`SELECT event FROM doc_sales_order_log ORDER BY event COLLATE "C"`)
	if !reflect.DeepEqual(events, [][]string{{"cancelled"}, {"submitted"}}) {
		t.Errorf("%v", events)
	}
}

// Rust: sales.rs::a_job_delivered_twice_logs_once
func TestAJobDeliveredTwiceLogsOnce(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	name := draft(t, e, "", twoItems())
	approved(t, e.user, name)
	// At-least-once delivery: the same job twice, as after a worker crash.
	duplicateJobs(e, "sales.log_order_event")

	got := fbtest.RunJobs(binary(), e.db)
	if run(got) != [3]int{2, 0, 0} {
		t.Fatalf("%+v", got)
	}
	if n := scalar(e, "SELECT count(*)::text FROM doc_sales_order_log"); n != "1" {
		t.Errorf("%s log rows", n)
	}
	if status := e.db.Rows("SELECT status, count(*)::text FROM fb_job GROUP BY status"); !reflect.DeepEqual(status, [][]string{{"done", "2"}}) {
		t.Errorf("%v", status)
	}
}

// Rust: sales.rs::a_refused_submit_leaves_no_job (the refusal happens before any job is collected; "collected,
// then refused" is covered by the framework test of the outbox)
func TestARefusedSubmitLeavesNoJob(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)
	// The rule "an order needs items" refuses the approval, which is the submit.
	name := draft(t, e, "", []any{})
	expectStatus(t, transition(e.user, name, "Submit for Approval", 1), 200)
	expectStatus(t, transition(e.user, name, "Approve", 2), 422)
	if n := scalar(e, "SELECT count(*)::text FROM fb_job WHERE name = 'sales.log_order_event'"); n != "0" {
		t.Errorf("%s jobs", n)
	}
}

// Rust: sales.rs::a_job_for_a_missing_order_is_dead_at_once
func TestAJobForAMissingOrderIsDeadAtOnce(t *testing.T) {
	e := newEnv(t)
	e.db.Exec(`INSERT INTO fb_job (id, name, payload, status, enqueued_by)
		VALUES (gen_random_uuid(), 'sales.log_order_event', '{"order": "SO-0", "event": "submitted"}', 'queued', 'test'),
		       (gen_random_uuid(), 'sales.log_order_event', '{}', 'queued', 'test')`)
	got := fbtest.RunJobs(binary(), e.db)
	if run(got) != [3]int{0, 0, 2} {
		t.Fatalf("%+v", got)
	}
	errors := e.db.Rows(`SELECT attempts::text, last_error FROM fb_job ORDER BY last_error COLLATE "C"`)
	if len(errors) != 2 || errors[0][0] != "1" || errors[1][0] != "1" {
		t.Fatalf("%v", errors)
	}
	if !strings.HasPrefix(errors[0][1], "the payload must be") {
		t.Errorf("%v", errors)
	}
	if errors[1][1] != "the sales order SO-0 does not exist" {
		t.Errorf("%v", errors)
	}
}

// Rust: sales.rs::the_daily_summary_counts_the_submitted_orders_of_the_day_before_the_tick
func TestTheDailySummaryCountsTheSubmittedOrdersOfTheDayBeforeTheTick(t *testing.T) {
	e := newEnv(t)
	customer(t, e.manager)

	// migrate stored the schedule: due at the next 02:00 UTC. The summary is of the day before that tick. Both
	// are read from the database, so the test does not depend on when it runs.
	due := e.db.Rows(`SELECT ((next_run_at AT TIME ZONE 'UTC')::date - 1)::text, extract(epoch FROM next_run_at)::bigint::text
		FROM fb_schedule WHERE name = 'sales.daily_summary'`)[0]
	day := due[0]
	epoch, err := strconv.ParseInt(due[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Unix(epoch, 0).UTC()

	// Two submitted orders on the day (32.00 and 5.00) are counted.
	for _, items := range [][]any{
		twoItems(),
		{map[string]any{"description": "Pen", "qty": 1, "rate": "5.00"}},
	} {
		approved(t, e.user, draft(t, e, day, items))
	}
	// Not counted: a draft of the day, a cancelled order of the day, a submitted order of another day.
	draft(t, e, day, twoItems())
	cancelled := draft(t, e, day, twoItems())
	approved(t, e.user, cancelled)
	expectStatus(t, transition(e.manager, cancelled, "Cancel", 3), 200)
	approved(t, e.user, draft(t, e, "2000-01-01", twoItems()))

	// The tick: the schedule enqueues the job once, and a second call for the same time does not enqueue it
	// again.
	if n := fbtest.RunSchedulesAt(binary(), e.db, tick); n < 1 {
		t.Fatalf("%d", n)
	}
	if n := fbtest.RunSchedulesAt(binary(), e.db, tick); n != 0 {
		t.Fatalf("enqueued once: %d", n)
	}
	queued := e.db.Rows("SELECT count(*)::text, min(payload->>'scheduled_for') FROM fb_job WHERE name = 'sales.daily_summary'")
	if queued[0][0] != "1" || !strings.HasPrefix(queued[0][1], tick.Format(time.DateOnly)+"T02:00:00") {
		t.Errorf("%v", queued)
	}

	if got := fbtest.RunJobs(binary(), e.db); got.Dead != 0 {
		t.Fatalf("%+v", got)
	}
	summary := e.db.Rows("SELECT name, day, orders::text, total::text, owner FROM doc_sales_daily_summary")
	if want := [][]string{{day, day, "2", "37.00", "job:sales.daily_summary"}}; !reflect.DeepEqual(summary, want) {
		t.Errorf("%v, want %v", summary, want)
	}

	// The same tick delivered again (an operator enqueued it): the handler finds the summary and does nothing,
	// and the job still ends done.
	duplicateJobs(e, "sales.daily_summary")
	if got := fbtest.RunJobs(binary(), e.db); got.Done != 1 || got.Dead != 0 {
		t.Fatalf("%+v", got)
	}
	if n := scalar(e, "SELECT count(*)::text FROM doc_sales_daily_summary"); n != "1" {
		t.Errorf("%s summaries", n)
	}
}

func TestADailySummaryNeedsAScheduledForTime(t *testing.T) {
	e := newEnv(t)
	for _, payload := range []string{`{}`, `{"scheduled_for": "tomorrow"}`, `{"scheduled_for": 5}`} {
		queueJob(e, "sales.daily_summary", payload)
	}
	got := fbtest.RunJobs(binary(), e.db)
	if run(got) != [3]int{0, 0, 3} {
		t.Fatalf("%+v", got)
	}
	if n := scalar(e, "SELECT count(*)::text FROM fb_job WHERE last_error LIKE 'the payload must be {\"scheduled_for\"%'"); n != "3" {
		t.Errorf("%s jobs with the message", n)
	}
}
