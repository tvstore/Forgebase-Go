package db_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The lifecycle pipeline and hooks (M2.3) against a real PostgreSQL: the order of the hooks and the
// framework steps, what a hook may change, and the rollback on a hook error. Ported from
// crates/forgebase-db/tests/lifecycle.rs.

const lifecycleInvoice = `
name = "invoice"
label = "Invoice"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "amount"
type = "int"
[[fields]]
name = "items"
type = "table"
target = "invoice_item"
`

const lifecycleItem = `
name = "invoice_item"
label = "Invoice Item"
is_child = true
[[fields]]
name = "qty"
type = "int"
required = true
[[fields]]
name = "description"
type = "data"
`

// log is what the hooks of a test write, safe for concurrent use.
type log struct {
	mu      sync.Mutex
	entries []string
}

func (l *log) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, s)
}

// taken returns the entries and empties the log.
func (l *log) taken() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.entries
	l.entries = nil
	return out
}

func opName(op core.Operation) string {
	return map[core.Operation]string{
		core.OpInsert: "Insert", core.OpUpdate: "Update", core.OpDelete: "Delete", core.OpSubmit: "Submit",
		core.OpCancel: "Cancel", core.OpAmend: "Amend", core.OpTransition: "Transition",
	}[op]
}

// failAt names the one hook (and operation) a recorder fails at.
type failAt struct {
	hook string
	op   core.Operation
}

// recorder writes <id>:<hook>:<operation> to the log; fails at the one hook it was told to.
type recorder struct {
	core.BaseHooks
	id     string
	log    *log
	failAt *failAt
}

func (r *recorder) record(hook string, ctx core.HookContext) error {
	r.log.add(fmt.Sprintf("%s:%s:%s", r.id, hook, opName(ctx.Operation)))
	if r.failAt != nil && r.failAt.hook == hook && r.failAt.op == ctx.Operation {
		return core.NewHookError(r.id + " says no")
	}
	return nil
}

func (r *recorder) BeforeInsert(ctx core.HookContext, _ *core.Document) error {
	return r.record("before_insert", ctx)
}
func (r *recorder) Validate(ctx core.HookContext, _ *core.Document) error {
	return r.record("validate", ctx)
}
func (r *recorder) BeforeDelete(ctx core.HookContext, _ *core.Document) error {
	return r.record("before_delete", ctx)
}

// recording is a registry of the invoice DocTypes with two recorders (A registered first) and
// their log.
func recording(t *testing.T, fail *failAt) (*core.Registry, *log) {
	t.Helper()
	l := &log{}
	reg := registry(t, lifecycleInvoice, lifecycleItem)
	for _, id := range []string{"A", "B"} {
		r := &recorder{id: id, log: l}
		if id == "A" {
			r.failAt = fail
		}
		if err := reg.AddHooks("invoice", r); err != nil {
			t.Fatal(err)
		}
	}
	return reg, l
}

func lifecycleSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	return setup(t, lifecycleInvoice, lifecycleItem, namedAccount)
}

func set(field string, v core.Value) core.Changes { return changes(field, v) }

func itemIDs(d *dbtest.DB) []string {
	var out []string
	for _, r := range d.Rows("SELECT id::text FROM doc_invoice_item ORDER BY idx") {
		out = append(out, r[0])
	}
	return out
}

func sameRows(t *testing.T, got, want []core.Row, label string) {
	t.Helper()
	a := describeDoc(&db.StoredDocument{Tables: map[string][]core.Row{"x": got}})
	b := describeDoc(&db.StoredDocument{Tables: map[string][]core.Row{"x": want}})
	if a != b {
		t.Errorf("%s:\n got %s\nwant %s", label, a, b)
	}
}

// Rust: lifecycle.rs::insert_update_and_delete_run_the_hooks_in_the_documented_order
func TestInsertUpdateAndDeleteRunTheHooksInTheDocumentedOrder(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg, l := recording(t, nil)
	stored := mustInsert(t, d, reg, "invoice", invoice("a"), sys("ann"))
	// Per hook point in registration order: A, then B; before_insert before validate.
	want(t, l.taken(), []string{"A:before_insert:Insert", "B:before_insert:Insert", "A:validate:Insert", "B:validate:Insert"}, "insert")
	mustUpdate(t, d, reg, "invoice", stored.Name, 1, set("note", text("n")), sys("ann"))
	want(t, l.taken(), []string{"A:validate:Update", "B:validate:Update"}, "update")
	if err := db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 2, sys("ann")); err != nil {
		t.Fatal(err)
	}
	want(t, l.taken(), []string{"A:before_delete:Delete", "B:before_delete:Delete"}, "delete")
	if count(t, d, "doc_invoice") != 0 {
		t.Error("not deleted")
	}
}

// Rust: lifecycle.rs::hooks_run_before_framework_validation_and_it_runs_again_after_them
func TestHooksRunBeforeFrameworkValidationAndItRunsAgainAfterThem(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg, l := recording(t, nil)
	// No title: the hooks still run (they see the input as given) ...
	_, err := db.Insert(bg, d.Pool, reg, "invoice", core.Document{}, sys("ann"))
	if n := len(l.taken()); n != 4 {
		t.Error(n)
	}
	// ... and then the framework reports the problem.
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrRequired, Field: "title"}}, "errors")
	if count(t, d, "doc_invoice") != 0 {
		t.Error("written")
	}
}

// call is what a hook was called with: doctype, actor, operation and the document.
type call struct {
	doctype, actor string
	op             core.Operation
	doc            core.Document
}

// spy keeps what it was called with.
type spy struct {
	core.BaseHooks
	mu    sync.Mutex
	calls []call
}

func (s *spy) see(ctx core.HookContext, doc *core.Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call{ctx.DocType, ctx.Actor, ctx.Operation, cloneDoc(*doc)})
}

func cloneDoc(d core.Document) core.Document {
	out := core.Document{Name: d.Name, Values: map[string]core.Value{}, Tables: map[string][]core.Row{}}
	for k, v := range d.Values {
		out.Values[k] = v
	}
	for k, rows := range d.Tables {
		for _, r := range rows {
			c := core.Row{}
			for f, v := range r {
				c[f] = v
			}
			out.Tables[k] = append(out.Tables[k], c)
		}
		if len(rows) == 0 {
			out.Tables[k] = []core.Row{}
		}
	}
	return out
}

func (s *spy) Validate(ctx core.HookContext, doc *core.Document) error {
	s.see(ctx, doc)
	return nil
}

func (s *spy) BeforeDelete(ctx core.HookContext, doc *core.Document) error {
	s.see(ctx, doc)
	return nil
}

// Rust: lifecycle.rs::hooks_get_the_context_and_see_the_whole_document_on_update
func TestHooksGetTheContextAndSeeTheWholeDocumentOnUpdate(t *testing.T) {
	d, _ := lifecycleSetup(t)
	s := &spy{}
	reg := registry(t, lifecycleInvoice, lifecycleItem, namedAccount)
	if err := reg.AddHooks("invoice", s); err != nil {
		t.Fatal(err)
	}
	stored := mustInsert(t, d, reg, "invoice", invoice("one", item(1, "a"), item(2, "b")), sys("ann"))
	// The changes name only the title: the hook still sees the stored note, amount and rows.
	c := set("title", text("two"))
	c.Values["note"] = text("kept")
	mustUpdate(t, d, reg, "invoice", stored.Name, 1, c, sys("bob"))
	mustUpdate(t, d, reg, "invoice", stored.Name, 2, set("title", text("three")), sys("cy"))
	if err := db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 3, sys("dee")); err != nil {
		t.Fatal(err)
	}
	seen := s.calls
	if len(seen) != 4 {
		t.Fatalf("%d calls", len(seen))
	}
	if seen[0].doctype != "invoice" || seen[0].actor != "ann" || seen[0].op != core.OpInsert || seen[0].doc.Name != "" {
		t.Errorf("%+v", seen[0])
	}
	if seen[1].actor != "bob" || seen[1].op != core.OpUpdate || seen[1].doc.Name != stored.Name ||
		seen[1].doc.Values["title"].Text != "two" || seen[1].doc.Values["note"].Text != "kept" ||
		seen[1].doc.Values["amount"].Kind != core.KindNull || len(seen[1].doc.Tables["items"]) != 2 {
		t.Errorf("%+v", seen[1])
	}
	if seen[2].doc.Values["title"].Text != "three" || seen[2].doc.Values["note"].Text != "kept" || len(seen[2].doc.Tables["items"]) != 2 {
		t.Errorf("stored values and rows are merged in: %+v", seen[2])
	}
	// before_delete: the stored document with its rows; the actor is the one given to delete.
	if seen[3].actor != "dee" || seen[3].op != core.OpDelete || seen[3].doc.Values["title"].Text != "three" {
		t.Errorf("%+v", seen[3])
	}
	sameRows(t, seen[3].doc.Tables["items"], []core.Row{item(1, "a"), item(2, "b")}, "delete rows")
}

// mutator sets amount before insert and note on every validate; adds a row for the title grow.
type mutator struct{ core.BaseHooks }

func (mutator) BeforeInsert(_ core.HookContext, doc *core.Document) error {
	doc.Values["amount"] = core.Int(7)
	return nil
}

func (mutator) Validate(_ core.HookContext, doc *core.Document) error {
	doc.Values["note"] = text("hooked")
	if v, ok := doc.Values["title"]; ok && v.Equal(text("grow")) {
		doc.Tables["items"] = append(doc.Tables["items"], item(9, "from hook"))
	}
	return nil
}

// Rust: lifecycle.rs::changes_made_by_hooks_are_persisted_on_insert_and_update
func TestChangesMadeByHooksArePersistedOnInsertAndUpdate(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg := registry(t, lifecycleInvoice, lifecycleItem, namedAccount)
	if err := reg.AddHooks("invoice", mutator{}); err != nil {
		t.Fatal(err)
	}
	stored := mustInsert(t, d, reg, "invoice", invoice("plain", item(1, "a"), item(2, "b")), sys("ann"))
	if stored.Values["amount"].Int != 7 || stored.Values["note"].Text != "hooked" {
		t.Error(describeDoc(stored))
	}
	sameDocument(t, mustGet(t, d, reg, "invoice", stored.Name, sys("cli")), stored, "again")
	ids := itemIDs(d)
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	// The hook wins over the caller's value, for a field the changes name ...
	updated := mustUpdate(t, d, reg, "invoice", stored.Name, 1, set("note", text("mine")), sys("ann"))
	if updated.Version != 2 || updated.Values["note"].Text != "hooked" {
		t.Error(describeDoc(updated))
	}
	// ... and a table the changes do not name, and the hook does not touch, is left alone.
	want(t, itemIDs(d), ids, "ids")
	// A hook that adds a row to a table the changes do not name: the rows are replaced.
	updated = mustUpdate(t, d, reg, "invoice", stored.Name, 2, set("title", text("grow")), sys("ann"))
	if updated.Version != 3 {
		t.Error(updated.Version)
	}
	sameRows(t, updated.Tables["items"], []core.Row{item(1, "a"), item(2, "b"), item(9, "from hook")}, "rows")
	newIDs := itemIDs(d)
	if len(newIDs) != 3 {
		t.Error(newIDs)
	}
	for _, id := range ids {
		if slices.Contains(newIDs, id) {
			t.Error("rows were not rewritten")
		}
	}
	if count(t, d, "doc_invoice_item") != 3 {
		t.Error("count")
	}
	// An explicit save that changes nothing still counts: the version is bumped.
	same := mustUpdate(t, d, reg, "invoice", stored.Name, 3, set("note", text("hooked")), sys("ann"))
	if same.Version != 4 {
		t.Error(same.Version)
	}
	sameDocument(t, &db.StoredDocument{Values: same.Values}, &db.StoredDocument{Values: updated.Values}, "values")
}

// breaker makes the document invalid after the fact, depending on the note.
type breaker struct{ core.BaseHooks }

func (breaker) Validate(_ core.HookContext, doc *core.Document) error {
	switch doc.Values["note"].Text {
	case "clear title":
		doc.Values["title"] = core.Null()
	case "bad row":
		doc.Tables["items"] = append(doc.Tables["items"], core.Row{})
	case "unknown":
		doc.Values["bogus"] = core.Int(1)
	}
	return nil
}

// Rust: lifecycle.rs::a_hook_cannot_leave_an_invalid_document
func TestAHookCannotLeaveAnInvalidDocument(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg := registry(t, lifecycleInvoice, lifecycleItem, namedAccount)
	if err := reg.AddHooks("invoice", breaker{}); err != nil {
		t.Fatal(err)
	}
	withNote := func(note string) core.Document {
		document := invoice("t", item(1, "a"))
		document.Values["note"] = text(note)
		return document
	}
	for _, c := range []struct {
		note     string
		expected core.ValidationError
	}{
		{"clear title", core.ValidationError{Kind: core.ErrRequired, Field: "title"}},
		{"bad row", inRow2(2, core.ValidationError{Kind: core.ErrRequired, Field: "qty"})},
		{"unknown", core.ValidationError{Kind: core.ErrUnknownField, Field: "bogus"}},
	} {
		_, err := db.Insert(bg, d.Pool, reg, "invoice", withNote(c.note), sys("ann"))
		deepWant(t, validationOf(t, err), []core.ValidationError{c.expected}, c.note)
		if count(t, d, "doc_invoice") != 0 {
			t.Errorf("%s: written", c.note)
		}
	}
	// The same on update; the stored document stays as it was.
	stored := mustInsert(t, d, reg, "invoice", withNote("fine"), sys("ann"))
	for _, note := range []string{"clear title", "bad row", "unknown"} {
		_, err := db.Update(bg, d.Pool, reg, "invoice", stored.Name, 1, set("note", text(note)), sys("ann"))
		if errs := validationOf(t, err); len(errs) != 1 {
			t.Errorf("%s: %v", note, errs)
		}
	}
	sameDocument(t, mustGet(t, d, reg, "invoice", stored.Name, sys("cli")), stored, "after")
}

// inRow2 is a problem in a row of the items table.
func inRow2(row int, e core.ValidationError) core.ValidationError { return inRow(row, e) }

// Rust: lifecycle.rs::a_failing_hook_rejects_the_operation_and_persists_nothing
func TestAFailingHookRejectsTheOperationAndPersistsNothing(t *testing.T) {
	d, plain := lifecycleSetup(t)
	stored := mustInsert(t, d, plain, "invoice", invoice("kept", item(1, "a")), sys("ann"))
	ids := itemIDs(d)
	// (hook, operation): A fails, so B never runs for that point.
	for _, c := range []struct {
		hook string
		op   core.Operation
		log  []string
	}{
		{"before_insert", core.OpInsert, []string{"A:before_insert:Insert"}},
		{"validate", core.OpInsert, []string{"A:before_insert:Insert", "B:before_insert:Insert", "A:validate:Insert"}},
		{"validate", core.OpUpdate, []string{"A:validate:Update"}},
		{"before_delete", core.OpDelete, []string{"A:before_delete:Delete"}},
	} {
		label := c.hook + " " + opName(c.op)
		reg, l := recording(t, &failAt{c.hook, c.op})
		var err error
		switch c.op {
		case core.OpInsert:
			_, err = db.Insert(bg, d.Pool, reg, "invoice", invoice("new", item(5, "x")), sys("ann"))
		case core.OpUpdate:
			_, err = db.Update(bg, d.Pool, reg, "invoice", stored.Name, 1, core.Changes{
				Values: pairs("title", text("changed")), Tables: map[string][]core.Row{"items": {item(8, "y")}}}, sys("ann"))
		default:
			err = db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 1, sys("ann"))
		}
		e := wantKind(t, err, db.ErrHook)
		if e.DocType != "invoice" || e.Hook != c.hook || e.Message != "A says no" {
			t.Errorf("%s: %+v", label, e)
		}
		want(t, l.taken(), c.log, label)
		// Nothing changed: counts, version, values and row ids.
		if count(t, d, "doc_invoice") != 1 || count(t, d, "doc_invoice_item") != 1 {
			t.Errorf("%s: counts", label)
		}
		want(t, itemIDs(d), ids, label+" ids")
		now := mustGet(t, d, plain, "invoice", stored.Name, sys("cli"))
		sameDocument(t, now, stored, label)
		if now.Version != 1 {
			t.Error(now.Version)
		}
	}
}

// Rust: lifecycle.rs::a_hook_error_leaves_the_callers_transaction_usable
func TestAHookErrorLeavesTheCallersTransactionUsable(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg, _ := recording(t, &failAt{"validate", core.OpInsert})
	okReg, _ := recording(t, nil)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, okReg, "invoice", invoice("kept", item(1, "a")), sys("ann")); err != nil {
		t.Fatal(err)
	}
	_, err = db.Insert(bg, tx, reg, "invoice", invoice("lost"), sys("ann"))
	wantKind(t, err, db.ErrHook)
	// The transaction still holds the first insert and accepts more work.
	if _, err := db.Insert(bg, tx, okReg, "invoice", invoice("more"), sys("ann")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_invoice") != 2 || count(t, d, "doc_invoice_item") != 1 {
		t.Error("counts")
	}
}

// Rust: lifecycle.rs::update_and_delete_hook_errors_leave_the_callers_transaction_usable
func TestUpdateAndDeleteHookErrorsLeaveTheCallersTransactionUsable(t *testing.T) {
	d, _ := lifecycleSetup(t)
	okReg, _ := recording(t, nil)
	badUpdate, _ := recording(t, &failAt{"validate", core.OpUpdate})
	badDelete, _ := recording(t, &failAt{"before_delete", core.OpDelete})
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.Insert(bg, tx, okReg, "invoice", invoice("kept", item(1, "a")), sys("ann"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Update(bg, tx, badUpdate, "invoice", stored.Name, 1, set("title", text("lost")), sys("ann"))
	wantKind(t, err, db.ErrHook)
	wantKind(t, db.Delete(bg, tx, badDelete, "invoice", stored.Name, 1, sys("ann")), db.ErrHook)
	// The transaction still holds the insert, unchanged, and accepts more work.
	still, err := db.Get(bg, tx, okReg, "invoice", stored.Name, sys("cli"))
	if err != nil || still == nil || still.Version != 1 || still.Values["title"].Text != "kept" {
		t.Fatalf("%v %v", still, err)
	}
	updated, err := db.Update(bg, tx, okReg, "invoice", stored.Name, 1, set("title", text("changed")), sys("ann"))
	if err != nil || updated.Version != 2 {
		t.Fatalf("%v %v", updated, err)
	}
	if err := db.Delete(bg, tx, okReg, "invoice", stored.Name, 2, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_invoice") != 0 || count(t, d, "doc_invoice_item") != 0 {
		t.Error("counts")
	}
}

const noteLinkingAccount = `
name = "note"
label = "Note"
[[fields]]
name = "account"
type = "link"
target = "account"
`

// Rust: lifecycle.rs::an_update_in_progress_does_not_block_a_concurrent_link_to_the_document
func TestAnUpdateInProgressDoesNotBlockAConcurrentLinkToTheDocument(t *testing.T) {
	d, reg := setup(t, namedAccount, noteLinkingAccount)
	account := mustInsert(t, d, reg, "account", doc("email", text("a@x.y")), sys("ann"))
	first, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err := second.Exec(bg, "SET lock_timeout = '300ms'"); err != nil {
		t.Fatal(err)
	}
	// The first transaction updates the account and keeps its row lock (not committed).
	tx, err := first.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update(bg, tx, reg, "account", account.Name, 1, set("points", core.Int(5)), sys("ann")); err != nil {
		t.Fatal(err)
	}
	// A concurrent insert of a document that links to the account is not blocked: the lookup takes
	// FOR KEY SHARE, which FOR NO KEY UPDATE does not conflict with.
	start := time.Now()
	if _, err := db.Insert(bg, second, reg, "note", doc("account", text("a@x.y")), sys("bob")); err != nil {
		t.Fatalf("the insert should not wait for the update: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the insert waited")
	}
	if count(t, d, "doc_note") != 1 {
		t.Error("count")
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
}

// Rust: lifecycle.rs::an_update_keeps_the_row_ids_of_a_table_with_identical_rows_and_replaces_changed_ones
func TestAnUpdateKeepsTheRowIDsOfATableWithIdenticalRowsAndReplacesChangedOnes(t *testing.T) {
	d, reg := lifecycleSetup(t)
	rows := []core.Row{item(1, "a"), item(2, "b")}
	document := mustInsert(t, d, reg, "invoice", invoice("t", rows...), sys("ann"))
	ids := itemIDs(d)
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	// The changes name the table with the rows it already has: same ids, version still bumped.
	same := core.Changes{Values: pairs("title", text("t2")), Tables: map[string][]core.Row{"items": rows}}
	v2 := mustUpdate(t, d, reg, "invoice", document.Name, 1, same, sys("ann"))
	if v2.Version != 2 {
		t.Error(v2.Version)
	}
	want(t, itemIDs(d), ids, "ids")
	// Changed rows replace the old ones: new ids.
	changed := core.Changes{Tables: map[string][]core.Row{"items": {item(1, "a"), item(3, "c")}}}
	v3 := mustUpdate(t, d, reg, "invoice", document.Name, 2, changed, sys("ann"))
	if len(v3.Tables["items"]) != 2 {
		t.Error(describeDoc(v3))
	}
	newIDs := itemIDs(d)
	if len(newIDs) != 2 {
		t.Error(newIDs)
	}
	for _, id := range newIDs {
		if slices.Contains(ids, id) {
			t.Error("a changed row kept its id")
		}
	}
}

// Rust: lifecycle.rs::not_found_and_conflict_are_reported_before_any_hook_runs
func TestNotFoundAndConflictAreReportedBeforeAnyHookRuns(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg, l := recording(t, nil)
	stored := mustInsert(t, d, reg, "invoice", invoice("a"), sys("ann"))
	l.taken()

	_, err := db.Update(bg, d.Pool, reg, "invoice", stored.Name, 5, set("note", text("n")), sys("ann"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 5 || e.CurrentVersion != 1 {
		t.Errorf("%+v", e)
	}
	_, err = db.Update(bg, d.Pool, reg, "invoice", "nope", 1, set("note", text("n")), sys("ann"))
	wantKind(t, err, db.ErrNotFound)
	wantKind(t, db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 5, sys("ann")), db.ErrConflict)
	wantKind(t, db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 1, sys("")), db.ErrInvalidActor)
	wantKind(t, db.Delete(bg, d.Pool, reg, "invoice", "nope", 1, sys("ann")), db.ErrNotFound)
	if got := l.taken(); len(got) != 0 {
		t.Errorf("a hook was called: %v", got)
	}
}

// Rust: lifecycle.rs::update_validates_the_whole_document
func TestUpdateValidatesTheWholeDocument(t *testing.T) {
	d, reg := lifecycleSetup(t)
	stored := mustInsert(t, d, reg, "invoice", invoice("a"), sys("ann"))
	// Another writer left a required field empty (the column is nullable).
	d.Exec("UPDATE doc_invoice SET title = NULL")
	// The changes do not touch the title, but the document as a whole is checked.
	_, err := db.Update(bg, d.Pool, reg, "invoice", stored.Name, 1, set("note", text("n")), sys("ann"))
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrRequired, Field: "title"}}, "errors")
	// Fixing it in the same update works.
	fixed := mustUpdate(t, d, reg, "invoice", stored.Name, 1, set("title", text("fixed")), sys("ann"))
	if fixed.Values["title"].Text != "fixed" || fixed.Values["note"].Kind != core.KindNull || fixed.Version != 2 {
		t.Error(describeDoc(fixed))
	}
}

// renamer changes the naming field on update.
type renamer struct{ core.BaseHooks }

func (renamer) Validate(ctx core.HookContext, doc *core.Document) error {
	if ctx.Operation == core.OpUpdate {
		doc.Values["email"] = text("other")
	}
	return nil
}

// Rust: lifecycle.rs::a_hook_cannot_rename_a_document
func TestAHookCannotRenameADocument(t *testing.T) {
	d, _ := lifecycleSetup(t)
	reg := registry(t, lifecycleInvoice, lifecycleItem, namedAccount)
	if err := reg.AddHooks("account", renamer{}); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, d, reg, "account", doc("email", text("old")), sys("ann"))
	_, err := db.Update(bg, d.Pool, reg, "account", "old", 1, set("points", core.Int(1)), sys("ann"))
	wantKind(t, err, db.ErrNaming)
	if count(t, d, "doc_account") != 1 {
		t.Error("count")
	}
	stored := mustGet(t, d, reg, "account", "old", sys("cli"))
	if stored.Version != 1 || stored.Values["email"].Text != "old" {
		t.Error(describeDoc(stored))
	}
}

// Rust: lifecycle.rs::child_doctypes_have_no_hooks_and_other_doctypes_are_not_affected
func TestChildDoctypesHaveNoHooksAndOtherDoctypesAreNotAffected(t *testing.T) {
	d, _ := lifecycleSetup(t)
	l := &log{}
	reg := registry(t, lifecycleInvoice, lifecycleItem, namedAccount)
	if err := reg.AddHooks("invoice_item", &recorder{id: "A", log: l}); err == nil {
		t.Error("hooks on a child DocType were accepted")
	}
	if err := reg.AddHooks("invoice", &recorder{id: "A", log: l, failAt: &failAt{"before_insert", core.OpInsert}}); err != nil {
		t.Fatal(err)
	}
	// account has no hooks: the failing invoice hooks are not called for it.
	mustInsert(t, d, reg, "account", doc("email", text("x")), sys("ann"))
	if got := l.taken(); len(got) != 0 {
		t.Error(got)
	}
}
