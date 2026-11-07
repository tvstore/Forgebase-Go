package db_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// User permissions (M10.3, ADR 0025) against a real PostgreSQL: a user restricted to some documents of
// a DocType sees, on every path, only those documents and the documents whose Links to it are empty or
// allowed; a hidden document is not found (and not locked); a Link to a hidden target is a missing one.
// Ported from crates/forgebase-db/tests/user_permissions.rs.
//
// The DocTypes: customer_group (the restricted target), customer (two Links to it), sales_order
// (submittable: a Link to the customer and to the group, a table whose rows Link to the group, and a
// Link at field level 1) and ticket (a workflow with a Link to the group).
//
// The tests that mix them with get and the like are ported without them; the visible names, the Rust
// env.names, are those get finds, one document at a time.
// The export part of every_read_shows_only_what_the_restriction_allows and an_import_applies_the_rules_to_every_row
// are ported in P7.2. The summary part and the transition test are ported (P6). The list parts, link titles and
// the auth and management tests are here (P3).

const upCustomerGroup = `
name = "customer_group"
label = "Customer Group"
naming = { field = "title" }
title_field = "title"
[[fields]]
name = "title"
type = "data"
required = true
unique = true
[[permissions]]
role = "Sales"
read = true
write = true
create = true
delete = true
export = true
import = true
[[permissions]]
role = "Mine"
read = true
write = true
`

const upCustomer = `
name = "customer"
label = "Customer"
naming = { field = "title" }
title_field = "title"
[[fields]]
name = "title"
type = "data"
required = true
unique = true
[[fields]]
name = "customer_group"
type = "link"
target = "customer_group"
[[fields]]
name = "billing_group"
type = "link"
target = "customer_group"
[[permissions]]
role = "Sales"
read = true
write = true
create = true
delete = true
export = true
import = true
[[permissions]]
role = "Mine"
read = true
write = true
create = true
delete = true
if_owner = true
`

const upSalesOrder = `
name = "sales_order"
label = "Sales Order"
submittable = true
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "customer"
type = "link"
target = "customer"
[[fields]]
name = "customer_group"
type = "link"
target = "customer_group"
[[fields]]
name = "partner_group"
type = "link"
target = "customer_group"
perm_level = 1
[[fields]]
name = "lines"
type = "table"
target = "order_line"
[[permissions]]
role = "Sales"
read = true
write = true
create = true
delete = true
submit = true
cancel = true
amend = true
export = true
[[permissions]]
role = "Mine"
read = true
write = true
create = true
delete = true
if_owner = true
[[permissions]]
role = "Manager"
level = 1
read = true
write = true
`

const upOrderLine = `
name = "order_line"
label = "Order Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "line_group"
type = "link"
target = "customer_group"
`

const upTicket = `
name = "ticket"
label = "Ticket"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "customer_group"
type = "link"
target = "customer_group"
[[permissions]]
role = "Sales"
read = true
write = true
create = true
[workflow]
[[workflow.states]]
name = "Open"
docstatus = 0
[[workflow.states]]
name = "Done"
docstatus = 0
[[workflow.transitions]]
action = "Finish"
from = "Open"
to = "Done"
roles = ["Sales"]
`

type upEnv struct {
	t   *testing.T
	d   *dbtest.DB
	reg *core.Registry
}

func newUpEnv(t *testing.T) *upEnv {
	t.Helper()
	d, reg := setup(t, upCustomerGroup, upCustomer, upSalesOrder, upOrderLine, upTicket)
	return &upEnv{t: t, d: d, reg: reg}
}

func restrictionsOf(pairs ...string) core.UserRestrictions {
	var p [][2]string
	for i := 0; i < len(pairs); i += 2 {
		p = append(p, [2]string{pairs[i], pairs[i+1]})
	}
	return core.NewUserRestrictions(p)
}

// ann is a salesperson restricted to the customer group Retail.
func ann() core.Caller {
	return user("ann", "Sales").WithRestrictions(restrictionsOf("customer_group", "Retail"))
}

// pat has the same role, with no restriction.
func pat() core.Caller { return user("pat", "Sales") }

func upLine(group string) core.Row {
	r := row("qty", core.Int(1), "line_group", core.Null())
	if group != "" {
		r["line_group"] = text(group)
	}
	return r
}

func nullable(s string) core.Value {
	if s == "" {
		return core.Null()
	}
	return text(s)
}

func upOrderDoc(title, group string, lines ...core.Row) core.Document {
	d := doc("title", text(title), "customer_group", nullable(group))
	if lines == nil {
		lines = []core.Row{}
	}
	d.Tables["lines"] = lines
	return d
}

// put inserts a document as the system and returns it.
func (e *upEnv) put(doctype string, document core.Document) *db.StoredDocument {
	e.t.Helper()
	return mustInsert(e.t, e.d, e.reg, doctype, document, sys("sys"))
}

func (e *upEnv) group(name string) { e.put("customer_group", doc("title", text(name))) }

func (e *upEnv) customer(name, group, billing string) {
	e.put("customer", doc("title", text(name), "customer_group", nullable(group), "billing_group", nullable(billing)))
}

func (e *upEnv) order(title, group string, lines ...core.Row) *db.StoredDocument {
	return e.put("sales_order", upOrderDoc(title, group, lines...))
}

// label is what a test calls a document: its title, or its name.
func label(s *db.StoredDocument) string {
	if v := s.Values["title"]; v.Kind == core.KindText {
		return v.Text
	}
	return s.Name
}

// names are the labels of the documents a caller lists (500 at most), sorted: the random names of the
// orders would make the order of a list vary.
func (e *upEnv) names(doctype string, who core.Caller) []string {
	e.t.Helper()
	out := []string{}
	for _, found := range listDocs(e.t, e.d, e.reg, doctype, who, db.ListQuery{Limit: u32(500)}) {
		out = append(out, label(found))
	}
	slices.Sort(out)
	return out
}

// orderName is the name of the sales order with this title (its names are random).
func (e *upEnv) orderName(title string) string {
	e.t.Helper()
	found := listDocs(e.t, e.d, e.reg, "sales_order", sys("sys"), db.ListQuery{Filters: []db.Filter{f("title", db.Eq(text(title)))}})
	if len(found) != 1 {
		e.t.Fatalf("one order titled %s, got %d", title, len(found))
	}
	return found[0].Name
}

// get is the document; a sales order is given by its title.
func (e *upEnv) get(doctype, name string, who core.Caller) *db.StoredDocument {
	e.t.Helper()
	if doctype == "sales_order" {
		name = e.orderName(name)
	}
	got, err := db.Get(bg, e.d.Pool, e.reg, doctype, name, who)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func isNotFound(err error) bool {
	var e *db.Error
	return errorsAs(err, &e) && e.Kind == db.ErrNotFound
}

func isDeniedRight(err error, right core.Right) bool {
	var e *db.Error
	return errorsAs(err, &e) && e.Kind == db.ErrPermissionDenied && e.Right == right
}

// seed is the data of most tests: two groups, five customers and three orders.
//
// Ann (restricted to Retail) sees: the group Retail; the customers c_retail, c_none and
// c_billing_retail; every order whose own customer_group is empty or Retail.
func (e *upEnv) seed() {
	e.group("Retail")
	e.group("Wholesale")
	e.customer("c_retail", "Retail", "")
	e.customer("c_wholesale", "Wholesale", "")
	e.customer("c_none", "", "")
	e.customer("c_both", "Retail", "Wholesale")
	e.customer("c_billing_retail", "", "Retail")
	e.order("o_retail", "Retail")
	e.order("o_wholesale", "Wholesale")
	e.order("o_none", "")
}

func strs(items ...string) []string { return items }

// ---- reads ---------------------------------------------------------------------------------------

// Rust: user_permissions.rs::every_read_shows_only_what_the_restriction_allows
//
// The get part, the names, the filter, sort and paging of a list, the summary and the export (and its count).
func TestEveryReadShowsOnlyWhatTheRestrictionAllows(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	a, p := ann(), pat()
	// The restricted DocType itself: only the allowed name.
	want(t, env.names("customer_group", a), strs("Retail"), "customer_group ann")
	want(t, env.names("customer_group", p), strs("Retail", "Wholesale"), "customer_group pat")
	// A Link to the target: empty or allowed (both Links must hold, see the next test).
	want(t, env.names("customer", a), strs("c_billing_retail", "c_none", "c_retail"), "customer")
	want(t, env.names("sales_order", a), strs("o_none", "o_retail"), "sales_order")
	if n := len(env.names("sales_order", p)); n != 3 {
		t.Error(n)
	}
	// get: the same.
	for _, c := range []struct {
		doctype, name string
		visible       bool
	}{
		{"customer_group", "Retail", true},
		{"customer_group", "Wholesale", false},
		{"customer", "c_retail", true},
		{"customer", "c_wholesale", false},
		{"customer", "c_none", true},
		{"sales_order", "o_wholesale", false},
		{"sales_order", "o_none", true},
		{"customer", "no such customer", false},
	} {
		if (env.get(c.doctype, c.name, a) != nil) != c.visible {
			t.Errorf("%s %s", c.doctype, c.name)
		}
	}
	// Filters, sorts and pages cannot reach a hidden row either.
	rows := listDocs(t, env.d, env.reg, "customer", a, db.ListQuery{Filters: []db.Filter{f("customer_group", db.Eq(text("Wholesale")))}})
	if len(rows) != 0 {
		t.Error("a filter on the hidden group finds something")
	}
	rows = listDocs(t, env.d, env.reg, "customer", a, db.ListQuery{Sort: []db.Sort{db.Desc("title")}, Limit: u32(1), Offset: u64(1)})
	if len(rows) != 1 || rows[0].Name != "c_none" {
		t.Errorf("paging counts only visible rows: %v", rows)
	}

	// The summary counts only visible rows.
	summaryOf := func(who core.Caller) []string {
		got, err := db.Summary(bg, env.d.Pool, env.reg, "customer", who, db.SummaryQuery{GroupBy: "customer_group"})
		if err != nil {
			t.Fatal(err)
		}
		return groupKeys(got)
	}
	want(t, summaryOf(a), strs("null=2", "Retail=1"), "summary ann")
	all := 0
	for _, g := range summaryOf(p) {
		var n int
		_, _ = fmt.Sscan(g[strings.LastIndex(g, "=")+1:], &n)
		all += n
	}
	if all != 5 {
		t.Errorf("summary pat counts %d", all)
	}

	// Export (and its count): the same documents as the list.
	for _, c := range []struct {
		doctype string
		who     core.Caller
		want    []string
	}{
		{"customer_group", a, strs("Retail")},
		{"customer", a, strs("c_billing_retail", "c_none", "c_retail")},
		{"sales_order", a, strs("o_none", "o_retail")},
	} {
		got, err := env.exported(c.doctype, c.who)
		if err != nil {
			t.Fatal(err)
		}
		want(t, got, c.want, "export "+c.doctype)
	}
	if got, err := env.exported("customer", p); err != nil || len(got) != 5 {
		t.Errorf("export pat: %v %v", got, err)
	}
}

// exported is the labels an export gives, sorted; its count is the count of the list.
func (e *upEnv) exported(doctype string, who core.Caller) ([]string, error) {
	e.t.Helper()
	tx, err := db.BeginExport(bg, e.d.Pool)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	export, err := db.StartExport(bg, tx, e.reg, who, db.ExportQuery{DocType: doctype, Limit: 1000, WithTables: true})
	if err != nil {
		return nil, err
	}
	if n := len(e.names(doctype, who)); export.Count() != uint64(n) {
		e.t.Errorf("the count of the export is %d, the count of the list %d", export.Count(), n)
	}
	names := []string{}
	for {
		page, err := export.NextPage(bg)
		if err != nil {
			return nil, err
		}
		if page == nil {
			break
		}
		for _, d := range page {
			names = append(names, label(d))
		}
	}
	slices.Sort(names)
	return names, nil
}

// Rust: user_permissions.rs::a_hidden_document_is_not_found_by_a_transition_and_a_visible_one_moves
func TestAHiddenDocumentIsNotFoundByATransitionAndAVisibleOneMoves(t *testing.T) {
	env := newUpEnv(t)
	env.group("Retail")
	env.group("Wholesale")
	make := func(title, group string) *db.StoredDocument {
		return env.put("ticket", doc("title", text(title), "customer_group", text(group)))
	}
	hidden := make("t_hidden", "Wholesale")
	visible := make("t_visible", "Retail")
	_, err := db.Transition(bg, env.d.Pool, env.reg, "ticket", hidden.Name, "Finish", hidden.Version, ann())
	wantKind(t, err, db.ErrNotFound)
	// A stale version on a hidden document: not found first.
	_, err = db.Transition(bg, env.d.Pool, env.reg, "ticket", hidden.Name, "Finish", 99, ann())
	wantKind(t, err, db.ErrNotFound)
	_, err = db.AvailableTransitions(bg, env.d.Pool, env.reg, "ticket", hidden.Name, ann())
	wantKind(t, err, db.ErrNotFound)
	done, err := db.Transition(bg, env.d.Pool, env.reg, "ticket", visible.Name, "Finish", visible.Version, ann())
	if err != nil || done.WorkflowState != "Done" {
		t.Fatal(done, err)
	}
	// The hidden ticket is still open.
	still := mustGet(t, env.d, env.reg, "ticket", hidden.Name, pat())
	if still.WorkflowState != "Open" {
		t.Error(still.WorkflowState)
	}
}

// Rust: user_permissions.rs::a_document_with_two_links_to_the_target_needs_both_allowed_or_empty
func TestADocumentWithTwoLinksToTheTargetNeedsBothAllowedOrEmpty(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	a := ann()
	for _, c := range []struct {
		name    string
		visible bool
	}{
		{"c_retail", true},         // group allowed, billing empty
		{"c_both", false},          // group allowed, billing hidden
		{"c_wholesale", false},     // group hidden, billing empty
		{"c_none", true},           // both empty
		{"c_billing_retail", true}, // group empty, billing allowed
	} {
		if (env.get("customer", c.name, a) != nil) != c.visible {
			t.Error(c.name)
		}
	}
	// More than one allowed name: allowed means any of them, per Link.
	two := user("two", "Sales").WithRestrictions(restrictionsOf("customer_group", "Retail", "customer_group", "Wholesale"))
	if n := len(env.names("customer", two)); n != 5 {
		t.Error(n)
	}
}

// Rust: user_permissions.rs::restrictions_on_two_targets_all_apply_and_an_unrelated_one_does_nothing
func TestRestrictionsOnTwoTargetsAllApplyAndAnUnrelatedOneDoesNothing(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// Restricted on the group and on the customer: an order must have an allowed group, and the
	// customers are limited by name as well as by their group.
	who := user("two", "Sales").WithRestrictions(restrictionsOf("customer_group", "Retail", "customer", "c_retail"))
	want(t, env.names("customer", who), strs("c_retail"), "two targets")
	// A restriction on a DocType nothing links to, or that does not exist, changes nothing.
	other := user("other", "Sales").WithRestrictions(restrictionsOf("ticket", "x", "ghost_doctype", "y"))
	if len(env.names("customer", other)) != 5 || len(env.names("customer_group", other)) != 2 {
		t.Error("an unrelated restriction changed something")
	}
	// A name that is no document any more restricts the user to nothing visible.
	gone := user("gone", "Sales").WithRestrictions(restrictionsOf("customer_group", "Deleted"))
	if got := env.names("customer_group", gone); len(got) != 0 {
		t.Error(got)
	}
	want(t, env.names("customer", gone), strs("c_none"), "only documents without a group are left")
}

// Rust: user_permissions.rs::names_with_quotes_commas_braces_and_backslashes_are_bound_not_interpreted
func TestNamesWithQuotesCommasBracesAndBackslashesAreBoundNotInterpreted(t *testing.T) {
	env := newUpEnv(t)
	odd := []string{`Odd "name", {x}\ NULL`, "NULL", "plain", "ünï,cödé", "has space "}
	for _, name := range odd {
		env.group(name)
	}
	env.group("Other")
	var pairs []string
	for _, n := range odd {
		pairs = append(pairs, "customer_group", n)
	}
	who := user("odd", "Sales").WithRestrictions(restrictionsOf(pairs...))
	seen := env.names("customer_group", who)
	expected := slices.Clone(odd)
	slices.Sort(expected)
	want(t, seen, expected, "odd names")
	// A name that merely looks like an array element is not matched.
	sneaky := user("sneaky", "Sales").WithRestrictions(restrictionsOf("customer_group", `Other","plain`))
	if got := env.names("customer_group", sneaky); len(got) != 0 {
		t.Error(got)
	}
}

// ---- locking reads -------------------------------------------------------------------------------

// hiddenResults is what each locking operation of who says about a document: NotFound for a hidden one,
// before the version is compared.
func (e *upEnv) hiddenResults(who core.Caller, name string, version int64) [][2]string {
	e.t.Helper()
	stale := version + 41
	note := func(op string, err error) [2]string {
		if err == nil {
			return [2]string{op, "ok"}
		}
		return [2]string{op, err.Error()}
	}
	var out [][2]string
	_, err := db.Update(bg, e.d.Pool, e.reg, "sales_order", name, stale, changes("title", text("x")), who)
	out = append(out, note("update", err))
	out = append(out, note("delete", db.Delete(bg, e.d.Pool, e.reg, "sales_order", name, stale, who)))
	_, err = db.Submit(bg, e.d.Pool, e.reg, "sales_order", name, stale, who)
	out = append(out, note("submit", err))
	_, err = db.Cancel(bg, e.d.Pool, e.reg, "sales_order", name, stale, who)
	out = append(out, note("cancel", err))
	_, err = db.Amend(bg, e.d.Pool, e.reg, "sales_order", name, stale, who)
	out = append(out, note("amend", err))
	return out
}

// Rust: user_permissions.rs::a_hidden_document_is_not_found_by_every_locking_operation_whatever_the_version
func TestAHiddenDocumentIsNotFoundByEveryLockingOperationWhateverTheVersion(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	hidden := env.get("sales_order", "o_wholesale", pat())
	forHidden := env.hiddenResults(ann(), hidden.Name, hidden.Version)
	forMissing := env.hiddenResults(ann(), "o_missing", hidden.Version)
	// Every operation: not found, with the very message of a name that does not exist, apart from
	// the name itself.
	for i := range forHidden {
		a, b := forHidden[i][1], forMissing[i][1]
		if !strings.Contains(a, "not found") ||
			strings.ReplaceAll(a, hidden.Name, "N") != strings.ReplaceAll(b, "o_missing", "N") {
			t.Errorf("%s: %q vs %q", forHidden[i][0], a, b)
		}
	}
	// A visible document under the same stale version is a conflict instead.
	visible := env.get("sales_order", "o_retail", pat())
	for _, r := range env.hiddenResults(ann(), visible.Name, visible.Version) {
		if !strings.Contains(r[1], "version") {
			t.Errorf("%s: %s", r[0], r[1])
		}
	}
	// Nothing changed.
	still := env.get("sales_order", "o_wholesale", pat())
	if still.Version != hidden.Version || still.Values["title"].Text != "o_wholesale" {
		t.Error(describeDoc(still))
	}
}

// Rust: user_permissions.rs::cancel_and_amend_of_a_hidden_document_are_not_found_too
func TestCancelAndAmendOfAHiddenDocumentAreNotFoundToo(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// A submitted and a cancelled document in the hidden group, made by the system.
	submitted := env.order("o_sub", "Wholesale")
	v := mustSubmit(t, env.d, env.reg, "sales_order", submitted.Name, submitted.Version, sys("sys")).Version
	_, err := db.Cancel(bg, env.d.Pool, env.reg, "sales_order", submitted.Name, v, ann())
	if !isNotFound(err) {
		t.Error(err)
	}
	v = mustCancel(t, env.d, env.reg, "sales_order", submitted.Name, v, sys("sys")).Version
	_, err = db.Amend(bg, env.d.Pool, env.reg, "sales_order", submitted.Name, v, ann())
	if !isNotFound(err) {
		t.Error(err)
	}
	// A visible one goes through the whole cycle for Ann.
	mine := env.order("o_mine", "Retail")
	v = mustSubmit(t, env.d, env.reg, "sales_order", mine.Name, mine.Version, ann()).Version
	v = mustCancel(t, env.d, env.reg, "sales_order", mine.Name, v, ann()).Version
	amended := mustAmend(t, env.d, env.reg, "sales_order", mine.Name, v, ann())
	if amended.Values["customer_group"].Text != "Retail" {
		t.Error(describeDoc(amended))
	}
}

// Rust: user_permissions.rs::a_hidden_row_is_not_even_locked_so_it_never_waits_for_a_lock_on_it
func TestAHiddenRowIsNotEvenLockedSoItNeverWaitsForALockOnIt(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	target := env.orderName("o_wholesale")
	// Another connection holds FOR UPDATE on the row.
	lockConn, err := env.d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Release()
	lockTx, err := lockConn.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(bg, "SELECT 1 FROM doc_sales_order WHERE name = $1 FOR UPDATE", target); err != nil {
		t.Fatal(err)
	}
	// The restricted caller gets NotFound at once, for every operation that takes a lock: the row is
	// outside the statement, so it is neither locked nor waited for.
	done := make(chan []error, 1)
	go func() {
		who := ann()
		_, e1 := db.Update(bg, env.d.Pool, env.reg, "sales_order", target, 1, changes("title", text("x")), who)
		e2 := db.Delete(bg, env.d.Pool, env.reg, "sales_order", target, 1, who)
		_, e3 := db.Submit(bg, env.d.Pool, env.reg, "sales_order", target, 1, who)
		done <- []error{e1, e2, e3}
	}()
	select {
	case results := <-done:
		for _, err := range results {
			if !isNotFound(err) {
				t.Error(err)
			}
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the restricted caller waited for the lock")
	}
	// The control: an unrestricted caller does wait for the same lock, so the lock is real.
	waiting := make(chan error, 1)
	go func() {
		_, err := db.Update(bg, env.d.Pool, env.reg, "sales_order", target, 1, changes("title", text("x")), pat())
		waiting <- err
	}()
	select {
	case err := <-waiting:
		t.Fatalf("the unrestricted update should wait for the lock: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	if err := lockTx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Errorf("the update goes through once the lock is gone: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the update did not finish")
	}
}

// ---- link resolution -----------------------------------------------------------------------------

// Rust: user_permissions.rs::a_link_to_a_hidden_target_is_a_missing_one_in_a_field_and_in_a_row
func TestALinkToAHiddenTargetIsAMissingOneInAFieldAndInARow(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	a := ann()
	// A top-level Link to the restricted target, and to a customer that is hidden by its group.
	for _, c := range []struct{ field, hidden, missing string }{
		{"customer_group", "Wholesale", "Nowhere"},
		{"customer", "c_wholesale", "c_nowhere"},
		{"customer", "c_both", "c_nowhere"},
	} {
		d := upOrderDoc("x", "")
		d.Values[c.field] = text(c.hidden)
		_, err := db.Insert(bg, env.d.Pool, env.reg, "sales_order", d, a)
		d = upOrderDoc("x", "")
		d.Values[c.field] = text(c.missing)
		_, reference := db.Insert(bg, env.d.Pool, env.reg, "sales_order", d, a)
		x, y := linkNotFound(t, err), linkNotFound(t, reference)
		if len(x) != 1 || x[0][0] != y[0][0] {
			t.Errorf("%s: %v %v", c.field, x, y)
		}
		if strings.ReplaceAll(err.Error(), c.hidden, "N") != strings.ReplaceAll(reference.Error(), c.missing, "N") {
			t.Errorf("%s: the same message: %q vs %q", c.field, err, reference)
		}
	}
	// A Link in a row.
	_, err := db.Insert(bg, env.d.Pool, env.reg, "sales_order", upOrderDoc("x", "", upLine("Retail"), upLine("Wholesale")), a)
	want(t, linkNotFound(t, err), [][2]string{{"lines[2].line_group:customer_group", "Wholesale"}}, "row")
	// The allowed target, an empty Link and a visible customer all resolve.
	d := upOrderDoc("fine", "Retail", upLine("Retail"), upLine(""))
	d.Values["customer"] = text("c_billing_retail")
	ok := mustInsert(t, env.d, env.reg, "sales_order", d, a)
	// The same target resolves for the unrestricted user.
	d = upOrderDoc("any", "Wholesale", upLine("Wholesale"))
	d.Values["customer"] = text("c_wholesale")
	mustInsert(t, env.d, env.reg, "sales_order", d, pat())
	// update: a changed value is resolved, clearing a Link is fine.
	_, err = db.Update(bg, env.d.Pool, env.reg, "sales_order", ok.Name, ok.Version, changes("customer_group", text("Wholesale")), a)
	want(t, linkNotFound(t, err), [][2]string{{"customer_group:customer_group", "Wholesale"}}, "update")
	cleared := mustUpdate(t, env.d, env.reg, "sales_order", ok.Name, ok.Version, changes("customer_group", core.Null()), a)
	if cleared.Values["customer_group"].Kind != core.KindNull {
		t.Error(describeDoc(cleared))
	}
}

// Rust: user_permissions.rs::a_stored_value_is_kept_with_full_access_but_a_new_one_is_not_allowed
func TestAStoredValueIsKeptWithFullAccessButANewOneIsNotAllowed(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// The system made an order that Ann can see, with a hidden Link in a row (a child row does not
	// decide the visibility of its parent).
	stored := env.order("o_rows", "Retail", upLine("Wholesale"), upLine("Retail"))
	a := ann()
	seen := mustGet(t, env.d, env.reg, "sales_order", stored.Name, a)
	if len(seen.Tables["lines"]) != 2 {
		t.Error("the hidden row Link is part of a visible document")
	}
	// Updating another field keeps it (the table is not changed).
	updated := mustUpdate(t, env.d, env.reg, "sales_order", stored.Name, stored.Version, changes("title", text("renamed")), a)
	// Re-sending the same rows keeps the stored value, and adding it to a new row is refused.
	keep := core.Changes{Tables: map[string][]core.Row{"lines": {upLine("Wholesale"), upLine("Retail"), upLine("Retail")}}}
	updated = mustUpdate(t, env.d, env.reg, "sales_order", stored.Name, updated.Version, keep, a)
	if len(updated.Tables["lines"]) != 3 {
		t.Error(describeDoc(updated))
	}
	more := core.Changes{Tables: map[string][]core.Row{"lines": {upLine("Wholesale"), upLine("Retail"), upLine("Retail"), upLine("Wholesale")}}}
	// The value is among the stored values of the same field of the same table: kept.
	if _, err := db.Update(bg, env.d.Pool, env.reg, "sales_order", stored.Name, updated.Version, more, a); err != nil {
		t.Errorf("a name already stored in this field is kept: %v", err)
	}
	// A hidden group that is NOT stored in the table is refused in a row.
	env.group("Third")
	fresh := core.Changes{Tables: map[string][]core.Row{"lines": {upLine("Third")}}}
	current := mustGet(t, env.d, env.reg, "sales_order", stored.Name, sys("sys"))
	_, err := db.Update(bg, env.d.Pool, env.reg, "sales_order", stored.Name, current.Version, fresh, a)
	want(t, linkNotFound(t, err), [][2]string{{"lines[1].line_group:customer_group", "Third"}}, "fresh")
	// Submitting keeps the stored hidden Link too, and amend (which never keeps) refuses it.
	current = mustGet(t, env.d, env.reg, "sales_order", stored.Name, sys("sys"))
	submitted, err := db.Submit(bg, env.d.Pool, env.reg, "sales_order", stored.Name, current.Version, a)
	if err != nil {
		t.Fatalf("submit keeps stored Links: %v", err)
	}
	cancelled := mustCancel(t, env.d, env.reg, "sales_order", stored.Name, submitted.Version, a)
	_, err = db.Amend(bg, env.d.Pool, env.reg, "sales_order", stored.Name, cancelled.Version, a)
	if len(linkNotFound(t, err)) == 0 {
		t.Error(err)
	}
}

// Rust: user_permissions.rs::no_write_makes_a_document_the_caller_cannot_see_except_through_a_child_row
func TestNoWriteMakesADocumentTheCallerCannotSeeExceptThroughAChildRow(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	a := ann()
	// Whatever Ann inserts or updates, she can read back.
	for _, group := range []string{"", "Retail"} {
		made := mustInsert(t, env.d, env.reg, "sales_order", upOrderDoc("w", group), a)
		if got, err := db.Get(bg, env.d.Pool, env.reg, "sales_order", made.Name, a); err != nil || got == nil {
			t.Errorf("%v %v", got, err)
		}
		to := "Retail"
		if group != "" {
			to = ""
		}
		changed := mustUpdate(t, env.d, env.reg, "sales_order", made.Name, made.Version, changes("customer_group", nullable(to)), a)
		if got, err := db.Get(bg, env.d.Pool, env.reg, "sales_order", changed.Name, a); err != nil || got == nil {
			t.Errorf("%v %v", got, err)
		}
	}
	// The Link at field level 1 (which Ann cannot read or write) is stored as the system set it, and
	// still decides the visibility.
	withPartner := upOrderDoc("p", "")
	withPartner.Values["partner_group"] = text("Wholesale")
	env.put("sales_order", withPartner)
	if slices.Contains(env.names("sales_order", a), "p") {
		t.Error("the order with a hidden partner group is visible")
	}
}

// ---- creating in a restricted DocType ------------------------------------------------------------

// Rust: user_permissions.rs::a_restricted_user_cannot_create_or_amend_a_document_of_the_restricted_doctype
func TestARestrictedUserCannotCreateOrAmendADocumentOfTheRestrictedDoctype(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// Ann has the create right on customer_group (the same role as Pat, who can create).
	_, err := db.Insert(bg, env.d.Pool, env.reg, "customer_group", doc("title", text("Retail 2")), ann())
	if !isDeniedRight(err, core.RightCreate) {
		t.Error(err)
	}
	mustInsert(t, env.d, env.reg, "customer_group", doc("title", text("Retail 2")), pat())
	// The same for amend: a restriction on the submittable DocType itself.
	order := env.order("o_a", "")
	v := mustSubmit(t, env.d, env.reg, "sales_order", order.Name, order.Version, sys("sys")).Version
	v = mustCancel(t, env.d, env.reg, "sales_order", order.Name, v, sys("sys")).Version
	vic := user("vic", "Sales").WithRestrictions(restrictionsOf("sales_order", order.Name))
	_, err = db.Amend(bg, env.d.Pool, env.reg, "sales_order", order.Name, v, vic)
	if !isDeniedRight(err, core.RightAmend) {
		t.Error(err)
	}
	_, err = db.Insert(bg, env.d.Pool, env.reg, "sales_order", upOrderDoc("n", ""), vic)
	if !isDeniedRight(err, core.RightCreate) {
		t.Error(err)
	}
	// Reading and updating the allowed document is fine, and no other document is visible.
	want(t, env.names("sales_order", vic), strs("o_a"), "visible")
}

// ---- bypass, combinations ------------------------------------------------------------------------

// Rust: user_permissions.rs::an_import_applies_the_rules_to_every_row
func TestAnImportAppliesTheRulesToEveryRow(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	row := func(n int, title, group string) db.ImportRow {
		return db.ImportRow{Row: n, Values: pairs("title", text(title), "customer_group", text(group))}
	}
	rows := []db.ImportRow{row(1, "i_ok", "Retail"), row(2, "i_hidden", "Wholesale"), row(3, "i_missing", "Nowhere")}
	outcome, err := db.Import(bg, env.d.Pool, env.reg, "customer", ann(), rows, db.ImportInsert, false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Inserted != 1 || outcome.ErrorCount != 2 {
		t.Fatalf("%d inserted, %d errors", outcome.Inserted, outcome.ErrorCount)
	}
	m0, m1 := outcome.Errors[0].Err.Error(), outcome.Errors[1].Err.Error()
	if strings.ReplaceAll(m0, "Wholesale", "N") != strings.ReplaceAll(m1, "Nowhere", "N") {
		t.Errorf("a hidden target is not reported as a missing one:\n%s\n%s", m0, m1)
	}
	// An upsert of a hidden document is not found, so it would be an insert that collides (the known limit: a
	// unique name tells that the document exists), and never an update.
	upsert := []db.ImportRow{{Row: 1, Name: "c_wholesale", Values: pairs("title", text("c_wholesale"))}}
	outcome, err = db.Import(bg, env.d.Pool, env.reg, "customer", ann(), upsert, db.ImportUpsert, false)
	if err != nil || outcome.Updated != 0 || outcome.ErrorCount != 1 {
		t.Fatalf("%+v %v", outcome, err)
	}
	// Importing the restricted DocType itself is refused row by row (creating is refused).
	groups := []db.ImportRow{{Row: 1, Values: pairs("title", text("New"))}}
	outcome, err = db.Import(bg, env.d.Pool, env.reg, "customer_group", ann(), groups, db.ImportInsert, false)
	switch {
	case err != nil:
		if !isDeniedRight(err, core.RightCreate) {
			t.Errorf("%v", err)
		}
	case outcome.Inserted != 0 || outcome.ErrorCount != 1 || !isDeniedRight(outcome.Errors[0].Err, core.RightCreate):
		t.Errorf("%+v", outcome)
	}
}

// Rust: user_permissions.rs::administrator_and_system_see_everything_whatever_rows_exist
func TestAdministratorAndSystemSeeEverythingWhateverRowsExist(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	pairs := restrictionsOf("customer_group", "Retail", "customer", "c_retail")
	adminC := user("root", core.Administrator).WithRestrictions(pairs)
	system := sys("sys").WithRestrictions(pairs)
	for _, who := range []core.Caller{adminC, system} {
		if len(env.names("customer_group", who)) != 2 || len(env.names("customer", who)) != 5 || len(env.names("sales_order", who)) != 3 {
			t.Errorf("%+v does not see everything", who)
		}
		if env.get("customer", "c_wholesale", who) == nil {
			t.Error("c_wholesale")
		}
		if got, err := env.exported("customer", adminC); err != nil || len(got) != 5 {
			t.Errorf("export: %v %v", got, err)
		}
	}
	mustInsert(t, env.d, env.reg, "customer_group", doc("title", text("Admin made")), adminC)
	d := upOrderDoc("a", "Wholesale", upLine("Wholesale"))
	d.Values["customer"] = text("c_wholesale")
	mustInsert(t, env.d, env.reg, "sales_order", d, adminC)
}

// Rust: user_permissions.rs::no_user_permissions_means_no_change
func TestNoUserPermissionsMeansNoChange(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// The user has restrictions, but not on anything these DocTypes touch: the same as none.
	plain := user("plain", "Sales")
	elsewhere := user("elsewhere", "Sales").WithRestrictions(restrictionsOf("ticket", "t"))
	for _, doctype := range []string{"customer_group", "customer", "sales_order"} {
		want(t, env.names(doctype, plain), env.names(doctype, elsewhere), doctype)
	}
	if len(env.names("sales_order", plain)) != 3 {
		t.Error("sales_order")
	}
}

// Rust: user_permissions.rs::the_restriction_and_if_owner_both_apply
func TestTheRestrictionAndIfOwnerBothApply(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	// Mia owns two customers: one in Retail, one in Wholesale. Bob owns one in Retail.
	mia := user("mia", "Mine").WithRestrictions(restrictionsOf("customer_group", "Retail"))
	bob := user("bob", "Mine")
	for _, c := range []struct{ title, group string }{
		{"m_wholesale_bob", "Wholesale"}, {"m_retail_bob", "Retail"}, {"m_none_bob", ""},
	} {
		d := doc("title", text(c.title))
		if c.group != "" {
			d.Values["customer_group"] = text(c.group)
		}
		mustInsert(t, env.d, env.reg, "customer", d, bob)
	}
	// Mia (restricted) creates Retail customers of her own; Bob has no restriction.
	mustInsert(t, env.d, env.reg, "customer", doc("title", text("m_retail_mia"), "customer_group", text("Retail")), mia)
	// Mine reads own documents only, so Mia sees her own document, and none of Bob's, even the ones in
	// scope; the group is Mia's group scope on top (she only has an if_owner read).
	want(t, env.names("customer", mia), strs("m_retail_mia"), "mia")
	if env.get("customer", "m_retail_bob", mia) != nil {
		t.Error("bob's document is visible to mia")
	}
	// Bob sees his three (no restriction).
	if n := len(env.names("customer", bob)); n != 3 {
		t.Error(n)
	}
	// A document of her own that left the scope is hidden from her: the system moves it.
	own := mustGet(t, env.d, env.reg, "customer", "m_retail_mia", sys("sys"))
	mustUpdate(t, env.d, env.reg, "customer", "m_retail_mia", own.Version, changes("customer_group", text("Wholesale")), sys("sys"))
	if got := env.names("customer", mia); len(got) != 0 {
		t.Error(got)
	}
}

// Rust: user_permissions.rs::a_caller_without_the_read_right_is_denied_before_any_scope
func TestACallerWithoutTheReadRightIsDeniedBeforeAnyScope(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	nobody := user("nobody", "Stranger").WithRestrictions(restrictionsOf("customer_group", "Retail"))
	_, err := db.Get(bg, env.d.Pool, env.reg, "customer", "c_retail", nobody)
	if !isDeniedRight(err, core.RightRead) {
		t.Error(err)
	}
	_, err = db.List(bg, env.d.Pool, env.reg, "customer", nobody, db.ListQuery{})
	if !isDeniedRight(err, core.RightRead) {
		t.Error(err)
	}
	_, err = db.Update(bg, env.d.Pool, env.reg, "customer", "c_retail", 1, changes("title", text("x")), nobody)
	if !isDeniedRight(err, core.RightWrite) {
		t.Error(err)
	}
}

// Rust: user_permissions.rs::a_hidden_link_field_still_restricts_and_its_value_is_not_revealed
func TestAHiddenLinkFieldStillRestrictsAndItsValueIsNotRevealed(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	hidden := upOrderDoc("lvl_hidden", "")
	hidden.Values["partner_group"] = text("Wholesale")
	env.put("sales_order", hidden)
	allowed := upOrderDoc("lvl_allowed", "")
	allowed.Values["partner_group"] = text("Retail")
	env.put("sales_order", allowed)
	a := ann()
	// Ann cannot read the field (level 1) but it decides: the order of Wholesale is hidden, the order
	// of Retail is shown, without the value.
	names := env.names("sales_order", a)
	if !slices.Contains(names, "lvl_allowed") || slices.Contains(names, "lvl_hidden") {
		t.Error(names)
	}
	shown := env.get("sales_order", "lvl_allowed", a)
	if _, ok := shown.Values["partner_group"]; ok {
		t.Error("the hidden value is revealed")
	}
	// It is unknown to a filter, as any hidden field.
	_, err := db.List(bg, env.d.Pool, env.reg, "sales_order", a, db.ListQuery{Filters: []db.Filter{f("partner_group", db.Eq(text("Retail")))}})
	wantKind(t, err, db.ErrInvalidQuery)
	// A manager with the level reads it, and is restricted all the same.
	manager := user("mgr", "Sales", "Manager").WithRestrictions(restrictionsOf("customer_group", "Retail"))
	shown = env.get("sales_order", "lvl_allowed", manager)
	if shown.Values["partner_group"].Text != "Retail" {
		t.Error(describeDoc(shown))
	}
	if env.get("sales_order", "lvl_hidden", manager) != nil {
		t.Error("lvl_hidden is visible to the manager")
	}
}

// ---- titles --------------------------------------------------------------------------------------

// Rust: user_permissions.rs::link_titles_leave_out_hidden_targets
func TestLinkTitlesLeaveOutHiddenTargets(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	order := upOrderDoc("t", "Retail")
	order.Values["customer"] = text("c_retail")
	visible := env.put("sales_order", order)
	order = upOrderDoc("h", "Wholesale")
	order.Values["customer"] = text("c_wholesale")
	hidden := env.put("sales_order", order)
	docs := []*db.StoredDocument{visible, hidden}
	titles, err := db.LinkTitles(bg, env.d.Pool, env.reg, ann(), "sales_order", docs)
	if err != nil {
		t.Fatal(err)
	}
	want(t, titles["customer_group"], map[string]string{"Retail": "Retail"}, "groups of ann")
	want(t, titles["customer"], map[string]string{"c_retail": "c_retail"}, "customers of ann")
	all, err := db.LinkTitles(bg, env.d.Pool, env.reg, pat(), "sales_order", docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["customer_group"]) != 2 || len(all["customer"]) != 2 {
		t.Error(all)
	}
}

// ---- loading and managing ------------------------------------------------------------------------

func userWith(t *testing.T, env *upEnv, name string, roles ...string) {
	t.Helper()
	if _, err := db.CreateUser(bg, env.d.Pool, name, "correct horse battery", roles); err != nil {
		t.Fatal(err)
	}
}

// callerOf is the caller of a new session of the user (Rust: restrictions_of): what a request gets
// from ValidateSession.
func callerOf(t *testing.T, env *upEnv, name string) core.Caller {
	t.Helper()
	session, err := db.Login(bg, env.d.Pool, name, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.ValidateSession(bg, env.d.Pool, session.Token)
	if err != nil || u == nil {
		t.Fatalf("%v %v", u, err)
	}
	return u.Caller()
}

func mustAddPermission(t *testing.T, env *upEnv, user, doctype, value string, wantAdded bool) {
	t.Helper()
	added, err := db.AddUserPermission(bg, env.d.Pool, env.reg, user, doctype, value)
	if err != nil || added != wantAdded {
		t.Fatalf("add %s %s %s: %v %v", user, doctype, value, added, err)
	}
}

func mustRemovePermission(t *testing.T, env *upEnv, user, doctype, value string, wantRemoved bool) {
	t.Helper()
	removed, err := db.RemoveUserPermission(bg, env.d.Pool, user, doctype, value)
	if err != nil || removed != wantRemoved {
		t.Fatalf("remove %s %s %s: %v %v", user, doctype, value, removed, err)
	}
}

// Rust: user_permissions.rs::the_permissions_are_loaded_with_every_authenticated_request
//
// Also checked here: an API token loads them like a session.
func TestThePermissionsAreLoadedWithEveryAuthenticatedRequest(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	userWith(t, env, "ann", "Sales")
	first := callerOf(t, env, "ann")
	if first.Username != "ann" || !slices.Equal(first.Roles, []string{"Sales"}) || !first.Restrictions.IsEmpty() {
		t.Errorf("no permissions yet: %+v", first)
	}
	mustAddPermission(t, env, "ann", "customer_group", "Retail", true)
	mustAddPermission(t, env, "ann", "customer_group", "Wholesale", true)
	mustAddPermission(t, env, "ann", "customer", "c_none", true)
	caller := callerOf(t, env, "ann")
	want(t, caller, user("ann", "Sales").WithRestrictions(restrictionsOf(
		"customer_group", "Retail", "customer_group", "Wholesale", "customer", "c_none")), "three permissions")
	// It applies at once: the next call as that caller sees the rows.
	want(t, env.names("customer", caller), strs("c_none"), "customers")
	// An API token loads them too.
	api, err := db.CreateAPIToken(bg, env.d.Pool, "ann")
	if err != nil {
		t.Fatal(err)
	}
	viaToken, err := db.ValidateAPIToken(bg, env.d.Pool, api.Key, api.Secret)
	if err != nil || viaToken == nil {
		t.Fatal(err)
	}
	want(t, viaToken.Caller(), caller, "api token")
	mustRemovePermission(t, env, "ann", "customer", "c_none", true)
	mustRemovePermission(t, env, "ann", "customer", "c_none", false)
	caller = callerOf(t, env, "ann")
	if n := len(env.names("customer", caller)); n != 5 {
		t.Error(n)
	}
}

// Rust: user_permissions.rs::adding_checks_the_user_the_doctype_the_value_and_the_document
func TestAddingChecksTheUserTheDoctypeTheValueAndTheDocument(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	userWith(t, env, "ann", "Sales")
	invalid := func(doctype, value string) string {
		t.Helper()
		_, err := db.AddUserPermission(bg, env.d.Pool, env.reg, "ann", doctype, value)
		return authKind(t, err, db.AuthInvalidUserPermission).Detail
	}
	_, err := db.AddUserPermission(bg, env.d.Pool, env.reg, "nobody", "customer_group", "Retail")
	authKind(t, err, db.AuthUserNotFound)
	_, err = db.AddUserPermission(bg, env.d.Pool, env.reg, "bad name", "customer_group", "Retail")
	authKind(t, err, db.AuthInvalidUsername)
	for _, c := range []struct{ doctype, value, needle string }{
		{"ghost", "x", "unknown DocType"},
		{"order_line", "x", "child DocType"},
		{"customer_group", "", "not empty"},
		{"customer_group", "a\x00b", "NUL"},
		{"customer_group", "Nowhere", "no `customer_group` document named `Nowhere`"},
	} {
		if m := invalid(c.doctype, c.value); !strings.Contains(m, c.needle) {
			t.Errorf("%s %q: %s", c.doctype, c.value, m)
		}
	}
	// The exact text of the error, with its prefix.
	_, err = db.AddUserPermission(bg, env.d.Pool, env.reg, "ann", "ghost", "x")
	if err.Error() != "invalid user permission: unknown DocType `ghost`" {
		t.Error(err)
	}
	// Nothing was written, and adding a row twice is not an error.
	listed, err := db.ListUserPermissions(bg, env.d.Pool, nil, nil)
	if err != nil || len(listed) != 0 {
		t.Fatal(listed, err)
	}
	mustAddPermission(t, env, "ann", "customer_group", "Retail", true)
	mustAddPermission(t, env, "ann", "customer_group", "Retail", false)
	if listed, _ := db.ListUserPermissions(bg, env.d.Pool, nil, nil); len(listed) != 1 {
		t.Error(listed)
	}
	// Removing a row of an unknown user is an error; of a document that is gone it works.
	_, err = db.RemoveUserPermission(bg, env.d.Pool, "nobody", "customer_group", "Retail")
	authKind(t, err, db.AuthUserNotFound)
	// A row of a document that is gone can still be removed.
	env.group("Temp")
	mustAddPermission(t, env, "ann", "customer_group", "Temp", true)
	temp := mustGet(t, env.d, env.reg, "customer_group", "Temp", sys("sys"))
	if err := db.Delete(bg, env.d.Pool, env.reg, "customer_group", "Temp", temp.Version, sys("sys")); err != nil {
		t.Fatal(err)
	}
	mustRemovePermission(t, env, "ann", "customer_group", "Temp", true)
}

// Rust: user_permissions.rs::the_list_is_sorted_and_filtered
func TestTheListIsSortedAndFiltered(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	userWith(t, env, "zoe", "Sales")
	userWith(t, env, "ann", "Sales")
	for _, c := range [][3]string{
		{"zoe", "customer_group", "Retail"},
		{"ann", "customer", "c_none"},
		{"ann", "customer_group", "Wholesale"},
		{"ann", "customer_group", "Retail"},
	} {
		mustAddPermission(t, env, c[0], c[1], c[2], true)
	}
	rows := func(user, doctype *string) []string {
		t.Helper()
		listed, err := db.ListUserPermissions(bg, env.d.Pool, user, doctype)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, p := range listed {
			if time.Since(p.CreatedAt) > time.Hour || p.CreatedAt.Location() != time.UTC {
				t.Errorf("created_at %v", p.CreatedAt)
			}
			out = append(out, p.User+" "+p.DocType+" "+p.Value)
		}
		return out
	}
	str := func(s string) *string { return &s }
	want(t, rows(nil, nil), []string{"ann customer c_none", "ann customer_group Retail", "ann customer_group Wholesale", "zoe customer_group Retail"}, "all")
	want(t, rows(str("ann"), str("customer_group")), []string{"ann customer_group Retail", "ann customer_group Wholesale"}, "ann groups")
	want(t, rows(nil, str("customer")), []string{"ann customer c_none"}, "customers")
	_, err := db.ListUserPermissions(bg, env.d.Pool, str("nobody"), nil)
	authKind(t, err, db.AuthUserNotFound)
	// A row for a DocType the site does not have is listed (so it can be removed) and ignored.
	env.d.Exec("INSERT INTO fb_user_permission (user_id, doctype, value) SELECT id, 'old_doctype', 'x' FROM fb_user WHERE username = 'ann'")
	if n := len(rows(str("ann"), nil)); n != 4 {
		t.Error(n)
	}
	mustRemovePermission(t, env, "ann", "old_doctype", "x", true)
}

// Rust: user_permissions.rs::a_user_has_at_most_a_thousand_documents_per_doctype
func TestAUserHasAtMostAThousandDocumentsPerDoctype(t *testing.T) {
	env := newUpEnv(t)
	env.seed()
	userWith(t, env, "ann", "Sales")
	// MaxUserPermissions - 1 rows by SQL (rows for names that need not exist), then the limit is reached
	// by an add and the next one is refused. Another DocType has its own limit.
	env.d.Exec(fmt.Sprintf("INSERT INTO fb_user_permission (user_id, doctype, value) "+
		"SELECT u.id, 'customer_group', 'n' || g FROM fb_user u, generate_series(1, %d) g WHERE u.username = 'ann'",
		core.MaxUserPermissions-1))
	mustAddPermission(t, env, "ann", "customer_group", "Retail", true)
	_, err := db.AddUserPermission(bg, env.d.Pool, env.reg, "ann", "customer_group", "Wholesale")
	message := authKind(t, err, db.AuthInvalidUserPermission).Detail
	if !strings.Contains(message, "1000") || !strings.Contains(message, "customer_group") {
		t.Error(message)
	}
	if message != "user `ann` is restricted to 1000 `customer_group` documents already, the most allowed" {
		t.Error(message)
	}
	// Adding the same row again is still fine, and so is another DocType.
	mustAddPermission(t, env, "ann", "customer_group", "Retail", false)
	mustAddPermission(t, env, "ann", "customer", "c_none", true)
	// The user still works with 1000 names bound.
	caller := callerOf(t, env, "ann")
	want(t, env.names("customer_group", caller), strs("Retail"), "groups")
}

// Rust: user_permissions.rs::a_database_without_the_table_still_serves_its_users
func TestADatabaseWithoutTheTableStillServesItsUsers(t *testing.T) {
	// A binary that is newer than the last migrate: no fb_user_permission yet, so nobody has any
	// permission and every request works as before.
	d := dbtest.Migrated(t)
	d.Exec("DROP TABLE fb_user_permission")
	if _, err := db.CreateUser(bg, d.Pool, "ann", "correct horse battery", []string{"Sales"}); err != nil {
		t.Fatal(err)
	}
	session, err := db.Login(bg, d.Pool, "ann", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !session.User.Restrictions.IsEmpty() {
		t.Error("the login has restrictions")
	}
	u, err := db.ValidateSession(bg, d.Pool, session.Token)
	if err != nil || u == nil || !u.Restrictions.IsEmpty() {
		t.Fatalf("%v %v", u, err)
	}
}

// Rust: user_permissions.rs::two_sites_keep_their_own_permissions
func TestTwoSitesKeepTheirOwnPermissions(t *testing.T) {
	siteA, siteB := newUpEnv(t), newUpEnv(t)
	for _, env := range []*upEnv{siteA, siteB} {
		env.seed()
		if _, err := db.CreateUser(bg, env.d.Pool, "ann", "correct horse battery", []string{"Sales"}); err != nil {
			t.Fatal(err)
		}
	}
	mustAddPermission(t, siteA, "ann", "customer_group", "Retail", true)
	onA := callerOf(t, siteA, "ann")
	onB := callerOf(t, siteB, "ann")
	want(t, siteA.names("customer_group", onA), strs("Retail"), "site A")
	if got := siteB.names("customer_group", onB); len(got) != 2 {
		t.Errorf("site B: %v", got)
	}
}
