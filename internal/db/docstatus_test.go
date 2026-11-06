package db_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// docstatus, submit, cancel and amend (M2.4) against a real PostgreSQL: the transition matrix, the
// locks on a submitted document, the hook order and the rollbacks. Ported from
// crates/forgebase-db/tests/docstatus.rs.

const quoteDoc = `
name = "quote"
label = "Quote"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "total"
type = "int"
[[fields]]
name = "remarks"
type = "text"
allow_on_submit = true
[[fields]]
name = "lines"
type = "table"
target = "quote_line"
`

const quoteLine = `
name = "quote_line"
label = "Quote Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
required = true
[[fields]]
name = "note"
type = "data"
`

// plainNote is not submittable.
const plainNote = `
name = "note"
label = "Note"
[[fields]]
name = "body"
type = "text"
`

func line(qty int64, note string) core.Row { return row("qty", core.Int(qty), "note", text(note)) }

func quote(title string, lines ...core.Row) core.Document {
	d := doc("title", text(title))
	if lines == nil {
		lines = []core.Row{}
	}
	d.Tables["lines"] = lines
	return d
}

// submitRecorder is the recorder of lifecycle_test.go with the submit and cancel hooks too.
type submitRecorder struct{ recorder }

func (r *submitRecorder) BeforeSubmit(ctx core.HookContext, _ *core.Document) error {
	return r.record("before_submit", ctx)
}

func (r *submitRecorder) BeforeCancel(ctx core.HookContext, _ *core.Document) error {
	return r.record("before_cancel", ctx)
}

// quoteRecording is the quote DocTypes with two recorders (A registered first) and their log.
func quoteRecording(t *testing.T, fail *failAt) (*core.Registry, *log) {
	t.Helper()
	l := &log{}
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	for _, id := range []string{"A", "B"} {
		r := &submitRecorder{recorder{id: id, log: l}}
		if id == "A" {
			r.failAt = fail
		}
		if err := reg.AddHooks("quote", r); err != nil {
			t.Fatal(err)
		}
	}
	return reg, l
}

func quoteSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	return setup(t, quoteDoc, quoteLine, plainNote)
}

// makeQuote is a fresh quote with two lines, brought to the given state.
func makeQuote(t *testing.T, d *dbtest.DB, reg *core.Registry, status core.DocStatus) *db.StoredDocument {
	t.Helper()
	stored := mustInsert(t, d, reg, "quote", quote("q", line(1, "a"), line(2, "b")), sys("ann"))
	if status != core.Draft {
		stored = mustSubmit(t, d, reg, "quote", stored.Name, stored.Version, sys("ann"))
	}
	if status == core.Cancelled {
		stored = mustCancel(t, d, reg, "quote", stored.Name, stored.Version, sys("ann"))
	}
	if stored.DocStatus == nil || *stored.DocStatus != status {
		t.Fatalf("status %v, want %v", stored.DocStatus, status)
	}
	return stored
}

// reread is the quote with this name, or nil.
func reread(t *testing.T, d *dbtest.DB, reg *core.Registry, name string) *db.StoredDocument {
	t.Helper()
	got, err := db.Get(bg, d.Pool, reg, "quote", name, sys("cli"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func statusOf(s *db.StoredDocument) string {
	if s == nil || s.DocStatus == nil {
		return "none"
	}
	return s.DocStatus.String()
}

// Rust: docstatus.rs::a_new_document_is_a_draft_and_users_cannot_set_docstatus_or_amended_from
func TestANewDocumentIsADraftAndUsersCannotSetDocstatusOrAmendedFrom(t *testing.T) {
	d, reg := quoteSetup(t)
	stored := mustInsert(t, d, reg, "quote", quote("a"), sys("ann"))
	if statusOf(stored) != "draft" || stored.AmendedFrom != "" || stored.Version != 1 {
		t.Error(describeDoc(stored))
	}
	for _, field := range []string{"docstatus", "amended_from"} {
		bad := quote("b")
		bad.Values[field] = core.Int(1)
		_, err := db.Insert(bg, d.Pool, reg, "quote", bad, sys("ann"))
		if !hasProblem(validationOf(t, err), core.ValidationError{Kind: core.ErrUnknownField, Field: field}) {
			t.Errorf("%s: %v", field, err)
		}
		_, err = db.Update(bg, d.Pool, reg, "quote", stored.Name, 1, set(field, core.Int(1)), sys("ann"))
		if !hasProblem(validationOf(t, err), core.ValidationError{Kind: core.ErrUnknownField, Field: field}) {
			t.Errorf("%s: %v", field, err)
		}
	}
	if count(t, d, "doc_quote") != 1 {
		t.Error("count")
	}
	// A plain DocType has no docstatus at all.
	note := mustInsert(t, d, reg, "note", core.Document{}, sys("ann"))
	if note.DocStatus != nil || note.AmendedFrom != "" {
		t.Error(describeDoc(note))
	}
}

// Rust: docstatus.rs::submit_cancel_and_amend_on_a_plain_doctype_are_refused
func TestSubmitCancelAndAmendOnAPlainDoctypeAreRefused(t *testing.T) {
	d, reg := quoteSetup(t)
	note := mustInsert(t, d, reg, "note", core.Document{}, sys("ann"))
	_, e1 := db.Submit(bg, d.Pool, reg, "note", note.Name, 1, sys("ann"))
	_, e2 := db.Cancel(bg, d.Pool, reg, "note", note.Name, 1, sys("ann"))
	_, e3 := db.Amend(bg, d.Pool, reg, "note", note.Name, 1, sys("ann"))
	for operation, err := range map[string]error{"submit": e1, "cancel": e2, "amend": e3} {
		e := wantKind(t, err, db.ErrNotSubmittable)
		if e.DocType != "note" || e.Operation != operation || !strings.Contains(err.Error(), "not submittable") {
			t.Errorf("%s: %v", operation, err)
		}
	}
	// Not even a missing document is looked for first.
	_, err := db.Submit(bg, d.Pool, reg, "note", "nope", 1, sys("ann"))
	wantKind(t, err, db.ErrNotSubmittable)
}

// Rust: docstatus.rs::the_docstatus_transition_matrix
func TestTheDocstatusTransitionMatrix(t *testing.T) {
	d, reg := quoteSetup(t)
	// What each operation does to a document in each docstatus; success leaves the state changed as
	// the operation says, an error leaves everything as it was.
	for _, c := range []struct {
		operation string
		state     core.DocStatus
		allowed   bool
	}{
		{"update", core.Draft, true},
		{"update", core.Submitted, true}, // only an allow_on_submit field, see below
		{"update", core.Cancelled, false},
		{"submit", core.Draft, true},
		{"submit", core.Submitted, false},
		{"submit", core.Cancelled, false},
		{"cancel", core.Draft, false},
		{"cancel", core.Submitted, true},
		{"cancel", core.Cancelled, false},
		{"amend", core.Draft, false},
		{"amend", core.Submitted, false},
		{"amend", core.Cancelled, true},
		{"delete", core.Draft, true},
		{"delete", core.Submitted, false},
		{"delete", core.Cancelled, true},
	} {
		before := makeQuote(t, d, reg, c.state)
		name, version := before.Name, before.Version
		var after *db.StoredDocument
		var err error
		switch c.operation {
		case "update":
			after, err = db.Update(bg, d.Pool, reg, "quote", name, version, set("remarks", text("r")), sys("bob"))
		case "submit":
			after, err = db.Submit(bg, d.Pool, reg, "quote", name, version, sys("bob"))
		case "cancel":
			after, err = db.Cancel(bg, d.Pool, reg, "quote", name, version, sys("bob"))
		case "amend":
			after, err = db.Amend(bg, d.Pool, reg, "quote", name, version, sys("bob"))
		default:
			err = db.Delete(bg, d.Pool, reg, "quote", name, version, sys("bob"))
		}
		label := fmt.Sprintf("%s on a %s document", c.operation, c.state)
		if c.allowed {
			if err != nil {
				t.Errorf("%s: %v", label, err)
				continue
			}
			expected := map[string]string{"update": c.state.String(), "submit": "submitted", "cancel": "cancelled", "amend": "draft", "delete": "none"}[c.operation]
			if got := statusOf(after); got != expected {
				t.Errorf("%s: %s, want %s", label, got, expected)
			}
			continue
		}
		e := wantKind(t, err, db.ErrInvalidDocStatus)
		if e.DocType != "quote" || e.Name != name || e.DocStatus != c.state || e.Operation != c.operation {
			t.Errorf("%s: %+v", label, e)
		}
		now := reread(t, d, reg, name)
		if now == nil || statusOf(now) != c.state.String() || now.Version != version {
			t.Errorf("%s: changed", label)
		}
		if now != nil {
			sameDocument(t, &db.StoredDocument{Values: now.Values}, &db.StoredDocument{Values: before.Values}, label+": unchanged")
		}
	}
	// Messages say what was refused and why.
	submitted := makeQuote(t, d, reg, core.Submitted)
	err := db.Delete(bg, d.Pool, reg, "quote", submitted.Name, submitted.Version, sys("ann"))
	want(t, err.Error(), fmt.Sprintf("cannot delete quote `%s`: it is submitted", submitted.Name), "message")
}

// Rust: docstatus.rs::submit_and_cancel_bump_the_version_and_record_the_actor
func TestSubmitAndCancelBumpTheVersionAndRecordTheActor(t *testing.T) {
	d, reg := quoteSetup(t)
	draft := mustInsert(t, d, reg, "quote", quote("a", line(1, "x")), sys("ann"))
	submitted := mustSubmit(t, d, reg, "quote", draft.Name, 1, sys("bob"))
	if statusOf(submitted) != "submitted" || submitted.Version != 2 || submitted.Owner != "ann" || submitted.ModifiedBy != "bob" ||
		submitted.ModifiedAt.Before(draft.ModifiedAt) {
		t.Error(describeDoc(submitted))
	}
	sameDocument(t, &db.StoredDocument{Values: submitted.Values, Tables: submitted.Tables}, &db.StoredDocument{Values: draft.Values, Tables: draft.Tables}, "values and rows are untouched")
	// Row ids stay too: a submit with no hook changes does not replace rows.
	cancelled := mustCancel(t, d, reg, "quote", draft.Name, 2, sys("cy"))
	if statusOf(cancelled) != "cancelled" || cancelled.Version != 3 || cancelled.ModifiedBy != "cy" {
		t.Error(describeDoc(cancelled))
	}
	sameDocument(t, &db.StoredDocument{Tables: cancelled.Tables}, &db.StoredDocument{Tables: draft.Tables}, "rows")
}

// Rust: docstatus.rs::stale_versions_and_unknown_names_are_reported_first
func TestStaleVersionsAndUnknownNamesAreReportedFirst(t *testing.T) {
	d, _ := quoteSetup(t)
	reg, l := quoteRecording(t, nil)
	draft := mustInsert(t, d, reg, "quote", quote("a"), sys("ann"))
	l.taken()
	_, err := db.Submit(bg, d.Pool, reg, "quote", draft.Name, 7, sys("ann"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 7 || e.CurrentVersion != 1 {
		t.Errorf("%+v", e)
	}
	_, err = db.Cancel(bg, d.Pool, reg, "quote", "nope", 1, sys("ann"))
	wantKind(t, err, db.ErrNotFound)
	_, err = db.Amend(bg, d.Pool, reg, "quote", draft.Name, 9, sys("ann"))
	wantKind(t, err, db.ErrConflict)
	// Not found and conflict come before any hook runs.
	if got := l.taken(); len(got) != 0 {
		t.Errorf("a hook was called: %v", got)
	}
	_, err = db.Submit(bg, d.Pool, reg, "quote", draft.Name, 1, sys(""))
	wantKind(t, err, db.ErrInvalidActor)
}

// Rust: docstatus.rs::a_submitted_document_only_changes_allow_on_submit_fields
func TestASubmittedDocumentOnlyChangesAllowOnSubmitFields(t *testing.T) {
	d, reg := quoteSetup(t)
	submitted := makeQuote(t, d, reg, core.Submitted)
	name, version := submitted.Name, submitted.Version

	// A locked field, a table and a mix are refused, and nothing changes.
	mixed := set("remarks", text("sneaky"))
	mixed.Values["title"] = text("changed")
	tableOnly := core.Changes{Tables: map[string][]core.Row{"lines": {line(9, "z")}}}
	for _, c := range []struct {
		changes core.Changes
		field   string
	}{
		{set("title", text("changed")), "title"},
		{set("total", core.Int(5)), "total"},
		{mixed, "title"},
		{tableOnly, "lines"},
	} {
		_, err := db.Update(bg, d.Pool, reg, "quote", name, version, c.changes, sys("bob"))
		if e := wantKind(t, err, db.ErrFieldLocked); e.DocType != "quote" || e.Name != name || e.Field != c.field {
			t.Errorf("%s: %+v", c.field, e)
		}
	}
	sameDocument(t, reread(t, d, reg, name), submitted, "a refused update changed nothing")

	// An allow_on_submit field may change; the version moves, the status stays.
	updated := mustUpdate(t, d, reg, "quote", name, version, set("remarks", text("ok")), sys("bob"))
	if statusOf(updated) != "submitted" || updated.Version != version+1 || updated.Values["remarks"].Text != "ok" ||
		!updated.Values["title"].Equal(submitted.Values["title"]) || updated.ModifiedBy != "bob" {
		t.Error(describeDoc(updated))
	}
	sameDocument(t, &db.StoredDocument{Tables: updated.Tables}, &db.StoredDocument{Tables: submitted.Tables}, "tables")

	// Repeating a locked field's stored value is no change, so it is allowed with a real one.
	same := set("title", text("q"))
	same.Values["remarks"] = text("again")
	same.Tables = map[string][]core.Row{"lines": submitted.Tables["lines"]}
	updated = mustUpdate(t, d, reg, "quote", name, version+1, same, sys("bob"))
	if updated.Values["remarks"].Text != "again" {
		t.Error(describeDoc(updated))
	}
	sameDocument(t, &db.StoredDocument{Tables: updated.Tables}, &db.StoredDocument{Tables: submitted.Tables}, "tables")

	// Clearing an allow_on_submit field works; validation still applies to the whole document.
	updated = mustUpdate(t, d, reg, "quote", name, version+2, set("remarks", core.Null()), sys("bob"))
	if updated.Values["remarks"].Kind != core.KindNull {
		t.Error(describeDoc(updated))
	}
	_, err := db.Update(bg, d.Pool, reg, "quote", name, version+3, set("remarks", core.Int(1)), sys("bob"))
	wantKind(t, err, db.ErrValidation)
}

// sneaky counts the updates in total (locked once submitted) in validate, on update only.
type sneaky struct{ core.BaseHooks }

func (sneaky) Validate(ctx core.HookContext, doc *core.Document) error {
	if ctx.Operation == core.OpUpdate {
		var n int64
		if v := doc.Values["total"]; v.Kind == core.KindInt {
			n = v.Int
		}
		doc.Values["total"] = core.Int(n + 1)
	}
	return nil
}

// Rust: docstatus.rs::a_hook_cannot_change_a_locked_field_of_a_submitted_document
func TestAHookCannotChangeALockedFieldOfASubmittedDocument(t *testing.T) {
	d, plain := quoteSetup(t)
	hooked := registry(t, quoteDoc, quoteLine, plainNote)
	if err := hooked.AddHooks("quote", sneaky{}); err != nil {
		t.Fatal(err)
	}
	draft := makeQuote(t, d, plain, core.Draft)
	// On a draft the hook may change the total ...
	updated := mustUpdate(t, d, hooked, "quote", draft.Name, 1, set("remarks", text("r")), sys("ann"))
	if updated.Values["total"].Int != 1 {
		t.Error(describeDoc(updated))
	}
	// ... on a submitted document it may not (the total is locked).
	submitted := mustSubmit(t, d, plain, "quote", draft.Name, 2, sys("ann"))
	_, err := db.Update(bg, d.Pool, hooked, "quote", draft.Name, 3, set("remarks", text("r2")), sys("ann"))
	if e := wantKind(t, err, db.ErrFieldLocked); e.Field != "total" {
		t.Errorf("%+v", e)
	}
	// The hook's change was refused: the document is as it was after the submit.
	sameDocument(t, reread(t, d, plain, draft.Name), submitted, "after the refused update")
}

// Rust: docstatus.rs::hooks_run_in_the_documented_order_for_submit_cancel_amend_and_delete
func TestHooksRunInTheDocumentedOrderForSubmitCancelAmendAndDelete(t *testing.T) {
	d, _ := quoteSetup(t)
	reg, l := quoteRecording(t, nil)
	draft := mustInsert(t, d, reg, "quote", quote("a"), sys("ann"))
	l.taken()

	mustSubmit(t, d, reg, "quote", draft.Name, 1, sys("ann"))
	want(t, l.taken(), []string{"A:validate:Submit", "B:validate:Submit", "A:before_submit:Submit", "B:before_submit:Submit"}, "submit")
	mustCancel(t, d, reg, "quote", draft.Name, 2, sys("ann"))
	want(t, l.taken(), []string{"A:before_cancel:Cancel", "B:before_cancel:Cancel"}, "cancel")
	amended := mustAmend(t, d, reg, "quote", draft.Name, 3, sys("ann"))
	// The insert pipeline, told apart by the operation.
	want(t, l.taken(), []string{"A:before_insert:Amend", "B:before_insert:Amend", "A:validate:Amend", "B:validate:Amend"}, "amend")
	if err := db.Delete(bg, d.Pool, reg, "quote", amended.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	want(t, l.taken(), []string{"A:before_delete:Delete", "B:before_delete:Delete"}, "delete")
	// A refused transition runs no hook.
	_, err := db.Cancel(bg, d.Pool, reg, "quote", draft.Name, 3, sys("ann"))
	wantKind(t, err, db.ErrInvalidDocStatus)
	if got := l.taken(); len(got) != 0 {
		t.Error(got)
	}
}

// finalize sets total and changes a row in before_submit.
type finalize struct{ core.BaseHooks }

func (finalize) BeforeSubmit(_ core.HookContext, doc *core.Document) error {
	var qty int64
	for _, r := range doc.Tables["lines"] {
		if r["qty"].Kind == core.KindInt {
			qty += r["qty"].Int
		}
	}
	doc.Values["total"] = core.Int(qty)
	if rows := doc.Tables["lines"]; len(rows) > 0 {
		rows[0]["note"] = text("finalized")
	}
	return nil
}

// Rust: docstatus.rs::changes_a_submit_hook_makes_are_persisted_with_the_docstatus
func TestChangesASubmitHookMakesArePersistedWithTheDocstatus(t *testing.T) {
	d, _ := quoteSetup(t)
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	if err := reg.AddHooks("quote", finalize{}); err != nil {
		t.Fatal(err)
	}
	draft := mustInsert(t, d, reg, "quote", quote("a", line(2, "x"), line(3, "y")), sys("ann"))
	if draft.Values["total"].Kind != core.KindNull {
		t.Error(describeDoc(draft))
	}
	submitted := mustSubmit(t, d, reg, "quote", draft.Name, 1, sys("ann"))
	if submitted.Values["total"].Int != 5 || submitted.Tables["lines"][0]["note"].Text != "finalized" ||
		submitted.Tables["lines"][1]["note"].Text != "y" || submitted.Version != 2 {
		t.Error(describeDoc(submitted))
	}
	// What was returned is what is stored.
	sameDocument(t, reread(t, d, reg, draft.Name), submitted, "stored")
}

// breakIt makes the document invalid in before_submit.
type breakIt struct{ core.BaseHooks }

func (breakIt) BeforeSubmit(_ core.HookContext, doc *core.Document) error {
	delete(doc.Values, "title")
	return nil
}

// Rust: docstatus.rs::submit_validates_the_result_of_the_hooks_and_changes_nothing_when_invalid
func TestSubmitValidatesTheResultOfTheHooksAndChangesNothingWhenInvalid(t *testing.T) {
	d, _ := quoteSetup(t)
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	if err := reg.AddHooks("quote", breakIt{}); err != nil {
		t.Fatal(err)
	}
	draft := makeQuote(t, d, reg, core.Draft)
	_, err := db.Submit(bg, d.Pool, reg, "quote", draft.Name, 1, sys("ann"))
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrRequired, Field: "title"}}, "errors")
	sameDocument(t, reread(t, d, reg, draft.Name), draft, "unchanged")
}

// Rust: docstatus.rs::a_hook_failure_at_each_new_point_leaves_nothing_changed
func TestAHookFailureAtEachNewPointLeavesNothingChanged(t *testing.T) {
	d, _ := quoteSetup(t)
	for _, c := range []struct {
		hook  string
		op    core.Operation
		state core.DocStatus
	}{
		{"validate", core.OpSubmit, core.Draft},
		{"before_submit", core.OpSubmit, core.Draft},
		{"before_cancel", core.OpCancel, core.Submitted},
		{"before_insert", core.OpAmend, core.Cancelled},
		{"validate", core.OpAmend, core.Cancelled},
	} {
		plain := registry(t, quoteDoc, quoteLine, plainNote)
		document := makeQuote(t, d, plain, c.state)
		rowsBefore, quotesBefore := count(t, d, "doc_quote_line"), count(t, d, "doc_quote")
		reg, l := quoteRecording(t, &failAt{c.hook, c.op})
		var err error
		switch c.op {
		case core.OpSubmit:
			_, err = db.Submit(bg, d.Pool, reg, "quote", document.Name, document.Version, sys("bob"))
		case core.OpCancel:
			_, err = db.Cancel(bg, d.Pool, reg, "quote", document.Name, document.Version, sys("bob"))
		default:
			_, err = db.Amend(bg, d.Pool, reg, "quote", document.Name, document.Version, sys("bob"))
		}
		label := fmt.Sprintf("%s for %s", c.hook, opName(c.op))
		e := wantKind(t, err, db.ErrHook)
		if e.DocType != "quote" || e.Hook != c.hook || e.Message != "A says no" {
			t.Errorf("%s: %+v", label, e)
		}
		// The failing hook is the first one, so the second never ran.
		if entries := l.taken(); len(entries) == 0 || entries[len(entries)-1][:2] != "A:" {
			t.Errorf("%s: %v", label, entries)
		}
		sameDocument(t, reread(t, d, plain, document.Name), document, label)
		if count(t, d, "doc_quote") != quotesBefore {
			t.Errorf("%s: a new document", label)
		}
		if count(t, d, "doc_quote_line") != rowsBefore {
			t.Errorf("%s: new rows", label)
		}
	}
}

// Rust: docstatus.rs::an_operation_in_a_caller_transaction_rolls_back_with_it
func TestAnOperationInACallerTransactionRollsBackWithIt(t *testing.T) {
	d, reg := quoteSetup(t)
	draft := makeQuote(t, d, reg, core.Draft)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := db.Submit(bg, tx, reg, "quote", draft.Name, 1, sys("ann"))
	if err != nil || statusOf(submitted) != "submitted" {
		t.Fatalf("%v %v", submitted, err)
	}
	// A failed call inside the transaction leaves it usable (it ran in a savepoint).
	_, err = db.Submit(bg, tx, reg, "quote", draft.Name, 2, sys("ann"))
	wantKind(t, err, db.ErrInvalidDocStatus)
	if _, err := db.Cancel(bg, tx, reg, "quote", draft.Name, 2, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	sameDocument(t, reread(t, d, reg, draft.Name), draft, "after the rollback")
}

// Rust: docstatus.rs::amending_copies_the_document_into_a_new_draft_named_after_the_original
func TestAmendingCopiesTheDocumentIntoANewDraftNamedAfterTheOriginal(t *testing.T) {
	d, reg := quoteSetup(t)
	first := mustInsert(t, d, reg, "quote", quote("a", line(1, "x"), line(2, "y")), sys("ann"))
	first = mustUpdate(t, d, reg, "quote", first.Name, 1, set("total", core.Int(3)), sys("ann"))
	first = mustSubmit(t, d, reg, "quote", first.Name, 2, sys("ann"))
	first = mustUpdate(t, d, reg, "quote", first.Name, 3, set("remarks", text("r")), sys("ann"))
	first = mustCancel(t, d, reg, "quote", first.Name, 4, sys("ann"))

	second := mustAmend(t, d, reg, "quote", first.Name, first.Version, sys("bob"))
	if second.Name != first.Name+"-1" || second.AmendedFrom != first.Name || statusOf(second) != "draft" ||
		second.Version != 1 || second.Owner != "bob" || second.ModifiedBy != "bob" || second.ID == first.ID {
		t.Error(describeDoc(second))
	}
	// Values and rows are copies.
	sameDocument(t, &db.StoredDocument{Values: second.Values, Tables: second.Tables}, &db.StoredDocument{Values: first.Values, Tables: first.Tables}, "copy")
	if second.Values["total"].Int != 3 {
		t.Error(describeDoc(second))
	}
	// The cancelled document is untouched (same version, still cancelled).
	sameDocument(t, reread(t, d, reg, first.Name), first, "original")
	if count(t, d, "doc_quote_line") != 4 {
		t.Error("count")
	}
	// The chain: X -> X-1 -> X-2 -> X-3, the root name stays.
	second = mustSubmit(t, d, reg, "quote", second.Name, 1, sys("bob"))
	second = mustCancel(t, d, reg, "quote", second.Name, 2, sys("bob"))
	third := mustAmend(t, d, reg, "quote", second.Name, 3, sys("bob"))
	if third.Name != first.Name+"-2" || third.AmendedFrom != second.Name {
		t.Error(describeDoc(third))
	}
	third = mustSubmit(t, d, reg, "quote", third.Name, 1, sys("bob"))
	third = mustCancel(t, d, reg, "quote", third.Name, 2, sys("bob"))
	fourth := mustAmend(t, d, reg, "quote", third.Name, 3, sys("bob"))
	if fourth.Name != first.Name+"-3" || fourth.AmendedFrom != third.Name {
		t.Error(describeDoc(fourth))
	}
}

// Rust: docstatus.rs::a_cancelled_document_can_only_be_amended_once
func TestACancelledDocumentCanOnlyBeAmendedOnce(t *testing.T) {
	d, reg := quoteSetup(t)
	cancelled := makeQuote(t, d, reg, core.Cancelled)
	first := mustAmend(t, d, reg, "quote", cancelled.Name, cancelled.Version, sys("ann"))
	_, err := db.Amend(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("ann"))
	if e := wantKind(t, err, db.ErrAlreadyAmended); e.DocType != "quote" || e.Name != cancelled.Name || e.AmendedBy != first.Name {
		t.Errorf("%+v", e)
	}
	want(t, err.Error(), fmt.Sprintf("quote `%s` was already amended by `%s`", cancelled.Name, first.Name), "message")
	if count(t, d, "doc_quote") != 2 {
		t.Error("count")
	}
	// Once the amendment is deleted the document can be amended again.
	if err := db.Delete(bg, d.Pool, reg, "quote", first.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	again := mustAmend(t, d, reg, "quote", cancelled.Name, cancelled.Version, sys("ann"))
	if again.Name != first.Name {
		t.Error(again.Name)
	}
}

// race runs a and b at the same time and returns their errors.
func race(a, b func() error) [2]error {
	var out [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); out[0] = a() }()
	go func() { defer wg.Done(); out[1] = b() }()
	wg.Wait()
	return out
}

func countErrors(outcomes [2]error, match func(error) bool) int {
	n := 0
	for _, e := range outcomes {
		if match(e) {
			n++
		}
	}
	return n
}

// Rust: docstatus.rs::racing_amendments_of_one_document_succeed_exactly_once
func TestRacingAmendmentsOfOneDocumentSucceedExactlyOnce(t *testing.T) {
	d, reg := quoteSetup(t)
	cancelled := makeQuote(t, d, reg, core.Cancelled)
	outcomes := race(
		func() error {
			_, err := db.Amend(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("a"))
			return err
		},
		func() error {
			_, err := db.Amend(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("b"))
			return err
		})
	if n := countErrors(outcomes, func(e error) bool { return e == nil }); n != 1 {
		t.Errorf("%v", outcomes)
	}
	if n := countErrors(outcomes, func(e error) bool {
		var de *db.Error
		return e != nil && errorsAs(e, &de) && de.Kind == db.ErrAlreadyAmended
	}); n != 1 {
		t.Errorf("%v", outcomes)
	}
	if count(t, d, "doc_quote") != 2 {
		t.Error("count")
	}
}

// Rust: docstatus.rs::racing_submits_with_the_same_version_succeed_exactly_once
func TestRacingSubmitsWithTheSameVersionSucceedExactlyOnce(t *testing.T) {
	d, reg := quoteSetup(t)
	draft := makeQuote(t, d, reg, core.Draft)
	outcomes := race(
		func() error { _, err := db.Submit(bg, d.Pool, reg, "quote", draft.Name, 1, sys("a")); return err },
		func() error { _, err := db.Submit(bg, d.Pool, reg, "quote", draft.Name, 1, sys("b")); return err })
	if n := countErrors(outcomes, func(e error) bool { return e == nil }); n != 1 {
		t.Errorf("%v", outcomes)
	}
	if n := countErrors(outcomes, func(e error) bool {
		var de *db.Error
		return e != nil && errorsAs(e, &de) && de.Kind == db.ErrConflict && de.CurrentVersion == 2
	}); n != 1 {
		t.Errorf("%v", outcomes)
	}
	if got := reread(t, d, reg, draft.Name).Version; got != 2 {
		t.Error(got)
	}
}

// Rust: docstatus.rs::delete_rules_by_docstatus
func TestDeleteRulesByDocstatus(t *testing.T) {
	d, reg := quoteSetup(t)
	// A draft and a cancelled document can be deleted, with their rows; a submitted one cannot.
	draft := makeQuote(t, d, reg, core.Draft)
	if err := db.Delete(bg, d.Pool, reg, "quote", draft.Name, draft.Version, sys("ann")); err != nil {
		t.Fatal(err)
	}
	cancelled := makeQuote(t, d, reg, core.Cancelled)
	if err := db.Delete(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_quote") != 0 || count(t, d, "doc_quote_line") != 0 {
		t.Error("counts")
	}
	submitted := makeQuote(t, d, reg, core.Submitted)
	err := db.Delete(bg, d.Pool, reg, "quote", submitted.Name, submitted.Version, sys("ann"))
	if e := wantKind(t, err, db.ErrInvalidDocStatus); e.DocStatus != core.Submitted || e.Operation != "delete" {
		t.Errorf("%+v", e)
	}
	if count(t, d, "doc_quote") != 1 || count(t, d, "doc_quote_line") != 2 {
		t.Error("counts")
	}
	// Cancel it and it can go.
	cancelled = mustCancel(t, d, reg, "quote", submitted.Name, submitted.Version, sys("ann"))
	if err := db.Delete(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_quote") != 0 {
		t.Error("count")
	}
}

// amendSpy keeps what before_insert saw.
type amendSpy struct {
	core.BaseHooks
	mu   sync.Mutex
	seen []string
}

func (s *amendSpy) BeforeInsert(ctx core.HookContext, doc *core.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, fmt.Sprintf("%s|%s|%s", opName(ctx.Operation), doc.Name, doc.Values["title"].Text))
	return nil
}

// Rust: docstatus.rs::amended_documents_see_the_hooks_context_and_the_new_name
func TestAmendedDocumentsSeeTheHooksContextAndTheNewName(t *testing.T) {
	d, plain := quoteSetup(t)
	cancelled := makeQuote(t, d, plain, core.Cancelled)
	spy := &amendSpy{}
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	if err := reg.AddHooks("quote", spy); err != nil {
		t.Fatal(err)
	}
	amended := mustAmend(t, d, reg, "quote", cancelled.Name, cancelled.Version, sys("ann"))
	want(t, spy.seen, []string{"Amend|" + amended.Name + "|q"}, "seen")
}

// rename renames the document it is amending.
type rename struct{ core.BaseHooks }

func (rename) BeforeInsert(ctx core.HookContext, doc *core.Document) error {
	if ctx.Operation == core.OpAmend {
		doc.Name = "other"
	}
	return nil
}

// Rust: docstatus.rs::a_hook_cannot_rename_an_amendment
func TestAHookCannotRenameAnAmendment(t *testing.T) {
	d, plain := quoteSetup(t)
	cancelled := makeQuote(t, d, plain, core.Cancelled)
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	if err := reg.AddHooks("quote", rename{}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Amend(bg, d.Pool, reg, "quote", cancelled.Name, cancelled.Version, sys("ann"))
	wantKind(t, err, db.ErrNaming)
	if count(t, d, "doc_quote") != 1 {
		t.Error("count")
	}
}

const ticketDoc = `
name = "ticket"
label = "Ticket"
submittable = true
[[fields]]
name = "code"
type = "data"
unique = true
`

// newCode gives an amendment a new value for the unique code field.
type newCode struct{ core.BaseHooks }

func (newCode) BeforeInsert(ctx core.HookContext, doc *core.Document) error {
	if ctx.Operation == core.OpAmend {
		doc.Values["code"] = text("T-2")
	}
	return nil
}

// Rust: docstatus.rs::amending_a_unique_field_needs_a_hook_that_changes_the_value
func TestAmendingAUniqueFieldNeedsAHookThatChangesTheValue(t *testing.T) {
	d, plain := setup(t, ticketDoc)
	stored := mustInsert(t, d, plain, "ticket", doc("code", text("T-1")), sys("ann"))
	stored = mustSubmit(t, d, plain, "ticket", stored.Name, stored.Version, sys("ann"))
	cancelled := mustCancel(t, d, plain, "ticket", stored.Name, stored.Version, sys("ann"))
	// The copy repeats code, which the cancelled original still holds.
	_, err := db.Amend(bg, d.Pool, plain, "ticket", cancelled.Name, cancelled.Version, sys("ann"))
	wantKind(t, err, db.ErrUniqueViolation)
	if count(t, d, "doc_ticket") != 1 {
		t.Error("count")
	}
	// A hook that checks core.OpAmend and changes the value makes it work.
	hooked := registry(t, ticketDoc)
	if err := hooked.AddHooks("ticket", newCode{}); err != nil {
		t.Fatal(err)
	}
	amended := mustAmend(t, d, hooked, "ticket", cancelled.Name, cancelled.Version, sys("ann"))
	if amended.Values["code"].Text != "T-2" || amended.AmendedFrom != cancelled.Name {
		t.Error(describeDoc(amended))
	}
	if count(t, d, "doc_ticket") != 2 {
		t.Error("count")
	}
}
