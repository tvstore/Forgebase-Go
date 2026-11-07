package sales_extension_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"

	"github.com/tvstore/forgebase-go/examples/sales"
	"github.com/tvstore/forgebase-go/examples/sales_extension"
)

// Tests of the sales_extension app, alone and composed with sales, ported from
// examples/sales_extension/tests/extension.rs. The ones that need PostgreSQL (see README.md) fail, they do
// not skip, when FORGEBASE_TEST_DATABASE_URL is not set.
//
// Ported in P4 (the app model): the hooks, the apps together (hook order, the cross-app Link, the owner of
// each DocType, permissions). Ported in P5 (jobs) and P6 (workflow transitions): the job tests, with the submit
// by the approval workflow of sales. Deferred, with the phase whose feature the Rust test uses:
//
//   - P7 (overlays): the_overlay_field_is_a_field_of_customer,
//     the_overlay_field_exists_only_where_the_extension_app_is_installed.

// binary is the same apps, in the same order, as cmd/sales_extension.
func binary() *forgebase.Binary {
	return forgebase.New().App(sales.App()).App(sales_extension.App())
}

func withTotal(total *forgebase.Value) *forgebase.Document {
	doc := &forgebase.Document{Values: map[string]forgebase.Value{}}
	if total != nil {
		doc.Values["total"] = *total
	}
	return doc
}

func validate(doc *forgebase.Document) error {
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpInsert)
	return (sales_extension.MinimumOrder{}).Validate(ctx, doc)
}

func decimal(t *testing.T, text string) *forgebase.Value {
	t.Helper()
	d, err := forgebase.ParseDecimal(text)
	if err != nil {
		t.Fatal(err)
	}
	v := forgebase.DecimalValue(d)
	return &v
}

// ---- Without a database -----------------------------------------------------------------------

// Rust: extension.rs::every_doctype_file_is_listed
func TestEveryDocTypeFileIsEmbedded(t *testing.T) {
	fbtest.AssertDocTypesListed(t, sales_extension.App(), "doctypes")
}

// Rust: extension.rs::the_apps_fit_together
func TestTheAppsFitTogether(t *testing.T) {
	if err := binary().Check(); err != nil {
		t.Fatal(err)
	}
}

// Rust: extension.rs::the_app_needs_sales_registered_before_it
func TestTheAppNeedsSalesRegisteredBeforeIt(t *testing.T) {
	// Without sales the Link to customer, the hook and DependsOn are all errors.
	if err := forgebase.New().App(sales_extension.App()).Check(); err == nil {
		t.Error("the app alone must be refused")
	}
	if err := forgebase.New().App(sales_extension.App()).App(sales.App()).Check(); err == nil {
		t.Error("apps are never sorted")
	}
}

// Rust: extension.rs::a_total_below_the_minimum_is_refused
func TestATotalBelowTheMinimumIsRefused(t *testing.T) {
	if sales_extension.MinOrderTotal.Cmp(forgebase.DecimalFromInt(10)) != 0 {
		t.Errorf("minimum %s", sales_extension.MinOrderTotal)
	}
	for _, total := range []string{"0.00", "5.00", "9.99", "-1.00"} {
		err := validate(withTotal(decimal(t, total)))
		if err == nil || err.Error() != "a sales order must total at least 10.00" {
			t.Errorf("%s: %v", total, err)
		}
	}
}

// Rust: extension.rs::the_minimum_and_more_are_accepted
func TestTheMinimumAndMoreAreAccepted(t *testing.T) {
	for _, total := range []string{"10.00", "10", "250.50"} {
		if err := validate(withTotal(decimal(t, total))); err != nil {
			t.Errorf("%s: %v", total, err)
		}
	}
}

// Rust: extension.rs::a_missing_or_odd_total_counts_as_zero
func TestAMissingOrOddTotalCountsAsZero(t *testing.T) {
	null, text := forgebase.Null(), forgebase.Text("99")
	for _, total := range []*forgebase.Value{nil, &null, &text} {
		if err := validate(withTotal(total)); err == nil {
			t.Errorf("%v must be refused", total)
		}
	}
}

// Rust: extension.rs::a_submit_enqueues_the_follow_up_job
func TestASubmitEnqueuesTheFollowUpJob(t *testing.T) {
	jobs := forgebase.NewEnqueuedJobs()
	ctx := forgebase.NewHookContext("sales_order", "ann", forgebase.OpSubmit).WithJobs(jobs)
	doc := &forgebase.Document{Name: "SO-2026-00001"}
	if err := (sales_extension.FollowUp{}).BeforeSubmit(ctx, doc); err != nil {
		t.Fatal(err)
	}
	enqueued := jobs.Take()
	if len(enqueued) != 1 || enqueued[0].Name() != "sales_extension.plan_follow_up_visit" {
		t.Fatalf("%+v", enqueued)
	}
	var payload map[string]any
	if err := json.Unmarshal(enqueued[0].Payload(), &payload); err != nil || !reflect.DeepEqual(payload, map[string]any{"order": "SO-2026-00001"}) {
		t.Errorf("%s %v", enqueued[0].Payload(), err)
	}
	// An order without a name cannot be followed up.
	err := (sales_extension.FollowUp{}).BeforeSubmit(ctx, &forgebase.Document{})
	if err == nil || err.Error() != "the sales order has no name" {
		t.Errorf("%v", err)
	}
}

// ---- Against PostgreSQL, both apps composed ---------------------------------------------------

type env struct {
	db       *fbtest.DB
	manager  *fbtest.Client
	user     *fbtest.Client
	outsider *fbtest.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	b := binary()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatal(err)
	}
	db.CreateUser("ann", "Sales User")
	db.CreateUser("max", "Sales Manager")
	db.CreateUser("eve", "Accountant")
	client := db.Client(b)
	// A customer for the orders and visits (only a Sales Manager creates one).
	manager := client.Login("max")
	reply := manager.Post("/api/v1/resource/customer", map[string]any{"email": "ada@example.com", "full_name": "Ada Lovelace"})
	if reply.Status != 201 {
		t.Fatalf("%d %s", reply.Status, reply.Text)
	}
	return &env{db: db, manager: manager, user: client.Login("ann"), outsider: client.Login("eve")}
}

func at(v any, path ...string) any {
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

func pen(qty int, rate string) []any {
	return []any{map[string]any{"description": "Pen", "qty": qty, "rate": rate}}
}

// Rust: extension.rs::the_extension_hook_runs_after_the_sales_hook
func TestTheExtensionHookRunsAfterTheSalesHook(t *testing.T) {
	e := newEnv(t)
	const orders = "/api/v1/resource/sales_order"
	// 1 x 5.00 is computed to a total of 5.00 and refused by the extension.
	small := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": pen(1, "5.00")})
	if small.Status != 422 || small.ErrorCode() != "hook_rejected" || !strings.Contains(small.ErrorMessage(), "a sales order must total at least 10.00") {
		t.Errorf("%d %s", small.Status, small.Text)
	}

	// A client cannot send the total (it is read_only): the order is refused before any hook runs, whatever
	// the value is.
	sent := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "total": "100.00", "items": pen(2, "5.00")})
	if sent.Status != 422 || sent.ErrorCode() != "validation_failed" || !strings.Contains(sent.Text, "field `total` is read-only") {
		t.Errorf("%d %s", sent.Status, sent.Text)
	}

	// Even an order without items is refused: its computed total is 0.
	if empty := e.user.Post(orders, map[string]any{"customer": "ada@example.com"}); empty.Status != 422 {
		t.Errorf("%d %s", empty.Status, empty.Text)
	}

	// 2 x 5.00 = 10.00 is accepted, and keeps the sales rules (computed amount). This proves the hook order:
	// the client sent no total, so if the extension hook ran before the sales hook it would see a total of 0
	// and refuse every order, this one included. Acceptance therefore proves the sales hook ran first.
	fine := e.user.Post(orders, map[string]any{"customer": "ada@example.com", "items": pen(2, "5.00")})
	if fine.Status != 201 || at(fine.Data(), "total") != "10.00" {
		t.Fatalf("%d %s", fine.Status, fine.Text)
	}
	// sales_order has an approval workflow: sending it for approval and approving it is the submit, and runs the
	// same hooks.
	submitted := submit(e, at(fine.Data(), "name").(string))
	if submitted.Status != 200 || at(submitted.Data(), "docstatus") != float64(1) {
		t.Errorf("%d %s", submitted.Status, submitted.Text)
	}
}

// Rust: extension.rs::an_update_below_the_minimum_is_refused_too
func TestAnUpdateBelowTheMinimumIsRefusedToo(t *testing.T) {
	e := newEnv(t)
	created := e.user.Post("/api/v1/resource/sales_order", map[string]any{"customer": "ada@example.com", "items": pen(2, "5.00")})
	if created.Status != 201 {
		t.Fatalf("%d %s", created.Status, created.Text)
	}
	name := at(created.Data(), "name").(string)
	refused := e.user.Patch("/api/v1/resource/sales_order/"+name, map[string]any{"version": 1, "data": map[string]any{"items": pen(1, "5.00")}})
	if refused.Status != 422 {
		t.Errorf("%d %s", refused.Status, refused.Text)
	}
}

// Rust: extension.rs::a_visit_links_to_a_customer_of_the_sales_app
func TestAVisitLinksToACustomerOfTheSalesApp(t *testing.T) {
	e := newEnv(t)
	visit := e.user.Post("/api/v1/resource/customer_visit", map[string]any{
		"customer": "ada@example.com", "visited_on": "2026-10-29", "notes": "Showed the new catalogue"})
	if visit.Status != 201 || at(visit.Data(), "customer") != "ada@example.com" {
		t.Errorf("%d %s", visit.Status, visit.Text)
	}

	// The framework's Link error names the field.
	missing := e.user.Post("/api/v1/resource/customer_visit", map[string]any{"customer": "nobody@example.com", "visited_on": "2026-10-29"})
	if missing.Status != 422 || missing.ErrorCode() != "validation_failed" ||
		!strings.Contains(mustJSON(t, at(missing.Body, "error", "details")), "customer") {
		t.Errorf("%d %s", missing.Status, missing.Text)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Rust: extension.rs::the_metadata_names_the_app_of_every_doctype
func TestTheMetadataNamesTheAppOfEveryDocType(t *testing.T) {
	e := newEnv(t)
	meta := e.user.Get("/api/v1/meta")
	if meta.Status != 200 {
		t.Fatalf("%d %s", meta.Status, meta.Text)
	}
	appOf := func(name string) any {
		for _, d := range meta.DataList() {
			if at(d, "name") == name {
				return at(d, "app")
			}
		}
		t.Errorf("%s is not listed: %s", name, meta.Text)
		return nil
	}
	if appOf("customer_visit") != "sales_extension" || appOf("sales_order") != "sales" || appOf("customer") != "sales" {
		t.Errorf("%s", meta.Text)
	}
}

// Rust: extension.rs::a_user_without_a_sales_role_cannot_create_a_visit
func TestAUserWithoutASalesRoleCannotCreateAVisit(t *testing.T) {
	e := newEnv(t)
	denied := e.outsider.Post("/api/v1/resource/customer_visit", map[string]any{"customer": "ada@example.com", "visited_on": "2026-10-29"})
	if denied.Status != 403 || denied.ErrorCode() != "permission_denied" {
		t.Errorf("%d %s", denied.Status, denied.Text)
	}
}

// ---- Jobs, both apps composed (P5) ------------------------------------------------------------

const ordersPath = "/api/v1/resource/sales_order"

// draft is a draft of 2 x 5.00 (the smallest total MinimumOrder accepts) with an optional order_date; it returns
// its name.
func draft(t *testing.T, e *env, orderDate string) string {
	t.Helper()
	body := map[string]any{"customer": "ada@example.com", "items": pen(2, "5.00")}
	if orderDate != "" {
		body["order_date"] = orderDate
	}
	created := e.user.Post(ordersPath, body)
	if created.Status != 201 {
		t.Fatalf("%d %s", created.Status, created.Text)
	}
	return at(created.Data(), "name").(string)
}

// submit submits the draft name (version 1) as the sales approval workflow wants it: sent for approval, then
// approved. It returns the reply of the first transition that fails, or of the approval (the submit itself).
func submit(e *env, name string) *fbtest.Reply {
	sent := e.user.Post(ordersPath+"/"+name+"/transition", map[string]any{"action": "Submit for Approval", "version": 1})
	if sent.Status != 200 {
		return sent
	}
	return e.user.Post(ordersPath+"/"+name+"/transition", map[string]any{"action": "Approve", "version": 2})
}

func jobNames(e *env) []string {
	var names []string
	for _, r := range e.db.Rows(`SELECT name FROM fb_job ORDER BY name COLLATE "C"`) {
		names = append(names, r[0])
	}
	return names
}

func counts(run fbtest.JobRun) [3]int { return [3]int{run.Done, run.Retried, run.Dead} }

// Rust: extension.rs::a_submitted_order_gets_a_follow_up_visit_and_a_log
func TestASubmittedOrderGetsAFollowUpVisitAndALog(t *testing.T) {
	e := newEnv(t)
	name := draft(t, e, "2026-10-20")
	if submitted := submit(e, name); submitted.Status != 200 {
		t.Fatalf("%d %s", submitted.Status, submitted.Text)
	}
	// One job of each app, both written by the submit.
	if got := jobNames(e); !reflect.DeepEqual(got, []string{"sales.log_order_event", "sales_extension.plan_follow_up_visit"}) {
		t.Errorf("%v", got)
	}

	run := fbtest.RunJobs(binary(), e.db)
	if counts(run) != [3]int{2, 0, 0} {
		t.Fatalf("%+v", run)
	}

	// The visit is a week after the order date and says which order it follows up.
	visits := e.db.Rows("SELECT visited_on::text, notes, source_order, owner FROM doc_customer_visit")
	want := [][]string{{"2026-10-27", "Follow-up of " + name, name, "job:sales_extension.plan_follow_up_visit"}}
	if !reflect.DeepEqual(visits, want) {
		t.Errorf("%v, want %v", visits, want)
	}
	listed := e.user.Get("/api/v1/resource/customer_visit")
	if listed.Status != 200 || at(listed.DataList()[0], "customer") != "ada@example.com" {
		t.Errorf("%d %s", listed.Status, listed.Text)
	}

	// The job of sales ran as well: both apps' jobs are in the composed binary.
	logs := e.db.Rows("SELECT name, owner FROM doc_sales_order_log")
	if want := [][]string{{name + "-submitted", "job:sales.log_order_event"}}; !reflect.DeepEqual(logs, want) {
		t.Errorf("%v, want %v", logs, want)
	}
}

// Rust: extension.rs::a_job_delivered_twice_plans_one_visit
func TestAJobDeliveredTwicePlansOneVisit(t *testing.T) {
	e := newEnv(t)
	name := draft(t, e, "2026-10-20")
	if submitted := submit(e, name); submitted.Status != 200 {
		t.Fatalf("%d %s", submitted.Status, submitted.Text)
	}
	e.db.Exec("INSERT INTO fb_job (id, name, payload, status, enqueued_by) " +
		"SELECT gen_random_uuid(), name, payload, 'queued', 'test' FROM fb_job WHERE name = 'sales_extension.plan_follow_up_visit'")

	run := fbtest.RunJobs(binary(), e.db)
	if counts(run) != [3]int{3, 0, 0} {
		t.Fatalf("%+v", run)
	}
	if got := e.db.Rows("SELECT count(*)::text FROM doc_customer_visit"); !reflect.DeepEqual(got, [][]string{{"1"}}) {
		t.Errorf("%v", got)
	}
}

// Rust: extension.rs::an_order_without_a_date_gets_no_visit
func TestAnOrderWithoutADateGetsNoVisit(t *testing.T) {
	e := newEnv(t)
	name := draft(t, e, "")
	if submitted := submit(e, name); submitted.Status != 200 {
		t.Fatalf("%d %s", submitted.Status, submitted.Text)
	}
	run := fbtest.RunJobs(binary(), e.db)
	if run.Done != 2 || run.Dead != 0 {
		t.Fatalf("%+v", run)
	}
	if got := e.db.Rows("SELECT count(*)::text FROM doc_customer_visit"); !reflect.DeepEqual(got, [][]string{{"0"}}) {
		t.Errorf("%v", got)
	}
}

// Rust: extension.rs::a_refused_submit_leaves_no_job_of_either_app (the refusal happens before any job is
// collected; "collected, then refused" is covered by the framework test of the outbox)
func TestARefusedSubmitLeavesNoJobOfEitherApp(t *testing.T) {
	e := newEnv(t)
	// Every save needs a total of 10.00, so a small order cannot be saved. Make the stored order small behind the
	// API's back (a row edited by hand, as after a bad import): the submit recomputes the total from the items,
	// and MinimumOrder refuses it.
	name := draft(t, e, "2026-10-20")
	e.db.Exec("UPDATE doc_sales_order_item SET qty = 1")
	refused := submit(e, name)
	if refused.Status != 422 || !strings.Contains(refused.ErrorMessage(), "a sales order must total at least 10.00") {
		t.Fatalf("%d %s", refused.Status, refused.Text)
	}
	if got := jobNames(e); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// A follow-up job that is queued by hand (a payload of the shape the hook writes), without a submit.
func TestAFollowUpJobQueuedByHandPlansTheVisit(t *testing.T) {
	e := newEnv(t)
	name := draft(t, e, "2026-10-20")
	e.db.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by) "+
		"VALUES (gen_random_uuid(), 'sales_extension.plan_follow_up_visit', '{\"order\": \"%s\"}', 'queued', 'test')", name))
	if run := fbtest.RunJobs(binary(), e.db); counts(run) != [3]int{1, 0, 0} {
		t.Fatalf("%+v", run)
	}
	if got := e.db.Rows("SELECT visited_on::text FROM doc_customer_visit"); !reflect.DeepEqual(got, [][]string{{"2026-10-27"}}) {
		t.Errorf("%v", got)
	}
}

func TestTheFollowUpJobRefusesABadPayloadAndAMissingOrder(t *testing.T) {
	e := newEnv(t)
	e.db.Exec(`INSERT INTO fb_job (id, name, payload, status, enqueued_by) VALUES
		(gen_random_uuid(), 'sales_extension.plan_follow_up_visit', '{}', 'queued', 'test'),
		(gen_random_uuid(), 'sales_extension.plan_follow_up_visit', '{"order": "SO-0"}', 'queued', 'test')`)
	run := fbtest.RunJobs(binary(), e.db)
	if counts(run) != [3]int{0, 0, 2} {
		t.Fatalf("%+v", run)
	}
	errors := e.db.Rows(`SELECT last_error FROM fb_job ORDER BY last_error COLLATE "C"`)
	want := [][]string{{`the payload must be {"order": <name>}`}, {"the sales order SO-0 does not exist"}}
	if !reflect.DeepEqual(errors, want) {
		t.Errorf("%v", errors)
	}
}
