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

// Field permission levels (M10.2, ADR 0024) against a real PostgreSQL: what each kind of caller reads
// and writes, that a hidden field is unknown (identical errors, checked against a registry where the
// field does not exist at all), that hidden data survives an update by a lower-level user, hooks,
// if_owner and the leak review. Ported from crates/forgebase-db/tests/field_levels.rs.
//
// Deferred, with the phase that has what they need: every call to list, summary, export and the saved
// views (P3 and P6, tests hidden_fields_are_unknown_to_filters_sort_summary_and_export_with_identical_errors,
// export_leaves_out_hidden_fields_and_refuses_them_as_unknown and
// a_saved_view_on_a_hidden_field_is_unknown_and_hidden_from_the_list) and the transition (P6, test
// a_transition_returns_a_redacted_document_and_takes_no_field_input). The tests that mix them with
// get, insert or update are ported without the deferred calls; each says so.

// flAccount is the account: credit_limit (level 1), vault_code (level 2, no rule names it), a table
// ledger at level 1, a unique internal_ref and a Link partner at level 1. Without withLevels it
// leaves those fields out, which is what the caller without them "sees".
func flAccount(withLevels bool) string {
	hidden := ""
	if withLevels {
		hidden = `
[[fields]]
name = "credit_limit"
type = "int"
perm_level = 1
[[fields]]
name = "vault_code"
type = "data"
perm_level = 2
[[fields]]
name = "internal_ref"
type = "data"
unique = true
perm_level = 1
[[fields]]
name = "partner"
type = "link"
target = "party"
perm_level = 1
[[fields]]
name = "grade"
type = "select"
options = ["a", "b"]
perm_level = 1
[[fields]]
name = "ledger"
type = "table"
target = "ledger_line"
perm_level = 1
`
	}
	return fmt.Sprintf(`
name = "account"
label = "Account"
title_field = "title"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "region"
type = "data"
[[fields]]
name = "revenue"
type = "int"
%s
[[permissions]]
role = "User"
read = true
write = true
create = true
delete = true
export = true
[[permissions]]
role = "Manager"
read = true
write = true
create = true
export = true
[[permissions]]
role = "Manager"
level = 1
read = true
write = true
[[permissions]]
role = "Auditor"
read = true
[[permissions]]
role = "Auditor"
level = 1
read = true
[[permissions]]
role = "Mine"
read = true
write = true
create = true
if_owner = true
[[permissions]]
role = "Mine"
level = 1
read = true
[[permissions]]
role = "Viewer"
read = true
[[permissions]]
role = "Spy"
level = 2
read = true
write = true
`, hidden)
}

const flLedgerLine = `
name = "ledger_line"
label = "Ledger Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

const flParty = `
name = "party"
label = "Party"
[[fields]]
name = "label"
type = "data"
[[permissions]]
role = "User"
read = true
create = true
[[permissions]]
role = "Manager"
read = true
create = true
`

// flInvoice is a submittable DocType with a note at level 1; Clerk has every document right.
const flInvoice = `
name = "invoice"
label = "Invoice"
submittable = true
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "internal_note"
type = "data"
perm_level = 1
[[permissions]]
role = "Clerk"
read = true
write = true
create = true
submit = true
cancel = true
amend = true
[[permissions]]
role = "Manager"
read = true
write = true
create = true
[[permissions]]
role = "Manager"
level = 1
read = true
write = true
`

// flTicket is a workflow DocType with a note at level 1 (its transition test is P6; it is in the
// registry so that the sync is the Rust one).
const flTicket = `
name = "ticket"
label = "Ticket"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "secret_note"
type = "data"
perm_level = 1
[[permissions]]
role = "Clerk"
read = true
write = true
create = true
[[permissions]]
role = "Manager"
read = true
write = true
create = true
[[permissions]]
role = "Manager"
level = 1
read = true
write = true
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
roles = ["Clerk"]
`

// flSpy is what the hook of account saw on its last validate, and what it sets for the leak tests.
type flSpy struct {
	core.BaseHooks
	mu    sync.Mutex
	seen  []core.Value
	calls int
}

func (s *flSpy) Validate(_ core.HookContext, doc *core.Document) error {
	s.mu.Lock()
	s.calls++
	s.seen = append(s.seen, doc.Values["credit_limit"])
	s.mu.Unlock()
	switch doc.Values["title"].Text {
	case "dup":
		doc.Values["internal_ref"] = text("same")
	case "badhook":
		doc.Values["credit_limit"] = text("not a number")
	case "ghostlink":
		doc.Values["partner"] = text("no such party")
	}
	return nil
}

func flBuild(t *testing.T, withLevels bool) (*core.Registry, *flSpy) {
	t.Helper()
	reg := registry(t, flAccount(withLevels), flLedgerLine, flParty, flInvoice, flTicket)
	spy := &flSpy{}
	if err := reg.AddHooks("account", spy); err != nil {
		t.Fatal(err)
	}
	return reg, spy
}

type flEnv struct {
	t   *testing.T
	d   *dbtest.DB
	reg *core.Registry
	// plain is the same DocType without the levelled fields: the "unknown field" reference.
	plain *core.Registry
	spy   *flSpy
}

func newFlEnv(t *testing.T) *flEnv {
	t.Helper()
	reg, spy := flBuild(t, true)
	plain, _ := flBuild(t, false)
	d := dbtest.Migrated(t)
	mustSync(t, d, reg)
	return &flEnv{t: t, d: d, reg: reg, plain: plain, spy: spy}
}

func (e *flEnv) insert(doctype string, document core.Document, who core.Caller) (*db.StoredDocument, error) {
	return db.Insert(bg, e.d.Pool, e.reg, doctype, document, who)
}

func (e *flEnv) update(doctype string, stored *db.StoredDocument, c core.Changes, who core.Caller) (*db.StoredDocument, error) {
	return db.Update(bg, e.d.Pool, e.reg, doctype, stored.Name, stored.Version, c, who)
}

func (e *flEnv) get(doctype, name string, who core.Caller) *db.StoredDocument {
	e.t.Helper()
	got, err := db.Get(bg, e.d.Pool, e.reg, doctype, name, who)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// fullAccount is an account made by the administrator with every level filled.
func (e *flEnv) fullAccount() *db.StoredDocument { return e.fullAccountRef("REF-1") }

func (e *flEnv) fullAccountRef(internalRef string) *db.StoredDocument {
	e.t.Helper()
	document := doc("title", text("Acme"), "region", text("north"), "revenue", core.Int(10),
		"credit_limit", core.Int(500), "vault_code", text("1234"), "internal_ref", text(internalRef))
	document.Tables["ledger"] = []core.Row{lline(1), lline(2)}
	stored, err := e.insert("account", document, admin())
	if err != nil {
		e.t.Fatal(err)
	}
	return stored
}

func lline(qty int64) core.Row { return row("qty", core.Int(qty)) }

// message is the text of the error of a call that must have failed.
func message[T any](t *testing.T, _ T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected the call to be refused")
	}
	return err.Error()
}

func (e *flEnv) calls() int {
	e.spy.mu.Lock()
	defer e.spy.mu.Unlock()
	return e.spy.calls
}

// ----- Reads ---------------------------------------------------------------------------------

// Rust: field_levels.rs::a_user_without_level_one_does_not_see_the_field_anywhere
//
// The list part (the same fields in a list, and the administrator's list) is ported with P3.
func TestAUserWithoutLevelOneDoesNotSeeTheFieldAnywhere(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	u := user("u", "User")
	got := env.get("account", stored.Name, u)
	if got == nil {
		t.Fatal("not found")
	}
	for _, hidden := range []string{"credit_limit", "vault_code", "internal_ref", "partner"} {
		if _, ok := got.Values[hidden]; ok {
			t.Errorf("%s is visible", hidden)
		}
	}
	if _, ok := got.Tables["ledger"]; ok {
		t.Error("the ledger is visible")
	}
	if got.Values["title"].Text != "Acme" || got.Values["revenue"].Int != 10 || got.Owner != "root" {
		t.Error(describeDoc(got))
	}
	// Administrator and System see every level.
	for _, who := range []core.Caller{admin(), sys("sys")} {
		all := env.get("account", stored.Name, who)
		if all.Values["credit_limit"].Int != 500 || all.Values["vault_code"].Text != "1234" || len(all.Tables["ledger"]) != 2 {
			t.Error(describeDoc(all))
		}
	}
}

// Rust: field_levels.rs::manager_sees_and_writes_level_one_but_not_level_two
//
// The part where the Spy role cannot list is ported with P3 (it gets PermissionDenied from list; get
// is covered below).
func TestManagerSeesAndWritesLevelOneButNotLevelTwo(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	manager := user("m", "Manager")
	got := env.get("account", stored.Name, manager)
	if got.Values["credit_limit"].Int != 500 || len(got.Tables["ledger"]) != 2 {
		t.Error(describeDoc(got))
	}
	if _, ok := got.Values["vault_code"]; ok {
		t.Error("level 2 is visible")
	}
	// Writes level 1, also the table.
	change := changes("credit_limit", core.Int(900))
	change.Tables = map[string][]core.Row{"ledger": {lline(7)}}
	updated, err := env.update("account", stored, change, manager)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Values["credit_limit"].Int != 900 {
		t.Error(describeDoc(updated))
	}
	sameRows(t, updated.Tables["ledger"], []core.Row{lline(7)}, "ledger")
	if _, ok := updated.Values["vault_code"]; ok {
		t.Error("level 2 is visible")
	}
	// Level 2 is unknown to the Manager, whatever the value.
	got2, err := env.update("account", updated, changes("vault_code", text("x")), manager)
	want(t, message(t, got2, err), "validation failed: unknown field `vault_code`", "message")
	// The Spy role has a level 2 rule but no level 0 right: it gets nothing.
	_, err = db.Get(bg, env.d.Pool, env.reg, "account", stored.Name, user("s", "Spy"))
	wantKind(t, err, db.ErrPermissionDenied)
	// Spy with a level 0 role too reads and writes level 2, and not level 1.
	both := env.get("account", stored.Name, user("s", "Spy", "Viewer"))
	if both.Values["vault_code"].Text != "1234" {
		t.Error(describeDoc(both))
	}
	if _, ok := both.Values["credit_limit"]; ok {
		t.Error("level 1 is visible")
	}
}

// ----- Writes --------------------------------------------------------------------------------

// Rust: field_levels.rs::writing_a_hidden_field_is_an_unknown_field_error_like_a_missing_one
func TestWritingAHiddenFieldIsAnUnknownFieldErrorLikeAMissingOne(t *testing.T) {
	env := newFlEnv(t)
	u := user("u", "User")
	// Insert: the very error of a field that does not exist (any value, null too).
	for _, value := range []core.Value{core.Int(1), core.Null(), text("not even an int")} {
		document := doc("title", text("t"), "credit_limit", value)
		hiddenStored, hiddenErr := env.insert("account", document, u)
		hidden := message(t, hiddenStored, hiddenErr)
		plainStored, plainErr := db.Insert(bg, env.d.Pool, env.plain, "account", document, u)
		missing := message(t, plainStored, plainErr)
		if hidden != missing || hidden != "validation failed: unknown field `credit_limit`" {
			t.Errorf("%q vs %q", hidden, missing)
		}
	}
	// A hidden Table field in tables.
	document := doc("title", text("t"))
	document.Tables["ledger"] = []core.Row{lline(1)}
	s, err := env.insert("account", document, u)
	want(t, message(t, s, err), "validation failed: unknown field `ledger`", "table")
	// Nothing was written.
	if count(t, env.d, "doc_account") != 0 {
		t.Error("written")
	}
	// Update: also when the value equals the stored one.
	stored := env.fullAccount()
	for _, value := range []core.Value{core.Int(500), core.Int(1), core.Null()} {
		s, err := env.update("account", stored, changes("credit_limit", value), u)
		want(t, message(t, s, err), "validation failed: unknown field `credit_limit`", "update")
	}
	// Several problems are all reported, hidden and missing alike, in field order.
	s, err = env.update("account", stored, changes("credit_limit", core.Int(1), "nope", core.Int(1)), u)
	want(t, message(t, s, err), "validation failed: unknown field `credit_limit`; unknown field `nope`", "several")
	// The hook did not run for any of these (it ran once for the administrator's account).
	if env.calls() != 1 {
		t.Errorf("the hook ran %d times", env.calls())
	}
}

// Rust: field_levels.rs::an_update_by_a_user_keeps_the_hidden_data_and_returns_documents_without_it
func TestAnUpdateByAUserKeepsTheHiddenDataAndReturnsDocumentsWithoutIt(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	u := user("u", "User")
	updated, err := env.update("account", stored, changes("title", text("Acme Inc")), u)
	if err != nil {
		t.Fatal(err)
	}
	// What the call returns has no hidden field.
	if updated.Values["title"].Text != "Acme Inc" {
		t.Error(describeDoc(updated))
	}
	if _, ok := updated.Values["credit_limit"]; ok {
		t.Error("credit_limit")
	}
	if _, ok := updated.Tables["ledger"]; ok {
		t.Error("ledger")
	}
	// What is stored is untouched, rows included.
	all := env.get("account", stored.Name, admin())
	if all.Values["credit_limit"].Int != 500 || all.Values["vault_code"].Text != "1234" ||
		all.Values["internal_ref"].Text != "REF-1" || len(all.Tables["ledger"]) != 2 || all.Version != 2 {
		t.Error(describeDoc(all))
	}
	// Insert returns the document without the hidden fields, too (defaults: none set by a User).
	created, err := env.insert("account", doc("title", text("New")), u)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := created.Values["credit_limit"]; ok {
		t.Error("credit_limit")
	}
	if _, ok := created.Tables["ledger"]; ok {
		t.Error("ledger")
	}
	if env.get("account", created.Name, admin()).Values["credit_limit"].Kind != core.KindNull {
		t.Error("not null")
	}
}

// Rust: field_levels.rs::a_reader_of_level_one_cannot_write_it_but_may_send_the_stored_value_back
func TestAReaderOfLevelOneCannotWriteItButMaySendTheStoredValueBack(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	// The Auditor reads level 0 and level 1 and has no write right at all: use a caller with the
	// right to write level 0 and read-only level 1 (User + Auditor).
	both := user("a", "User", "Auditor")
	got := env.get("account", stored.Name, both)
	if got.Values["credit_limit"].Int != 500 || len(got.Tables["ledger"]) != 2 {
		t.Error(describeDoc(got))
	}
	callsBefore := env.calls()
	// A different value, null over a stored value, and a new row list: refused as not permitted,
	// before any hook runs.
	for _, c := range []core.Changes{changes("credit_limit", core.Int(1)), changes("credit_limit", core.Null())} {
		s, err := env.update("account", stored, c, both)
		message(t, s, err)
		want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrNotPermitted, Field: "credit_limit"}}, "errors")
		want(t, err.Error(), "validation failed: field `credit_limit` cannot be changed with your permissions", "text")
	}
	rows := core.Changes{Tables: map[string][]core.Row{"ledger": {lline(9)}}}
	_, err := env.update("account", stored, rows, both)
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrNotPermitted, Field: "ledger"}}, "rows")
	if env.calls() != callsBefore {
		t.Error("a hook ran")
	}
	// Sending the stored value (and the stored rows) back is not a change.
	same := changes("credit_limit", core.Int(500), "title", text("Renamed"))
	same.Tables = map[string][]core.Row{"ledger": got.Tables["ledger"]}
	updated, err := env.update("account", stored, same, both)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Values["title"].Text != "Renamed" || updated.Values["credit_limit"].Int != 500 {
		t.Error(describeDoc(updated))
	}
	// An insert with a value is refused too; null (nothing) and absence are fine.
	s, err := env.insert("account", doc("title", text("x"), "credit_limit", core.Int(1)), both)
	want(t, message(t, s, err), "validation failed: field `credit_limit` cannot be changed with your permissions", "insert")
	if _, err := env.insert("account", doc("title", text("x"), "credit_limit", core.Null()), both); err != nil {
		t.Errorf("null is nothing: %v", err)
	}
	if _, err := env.insert("account", doc("title", text("y")), both); err != nil {
		t.Errorf("absent: %v", err)
	}
}

// Rust: field_levels.rs::administrator_and_system_write_every_level
func TestAdministratorAndSystemWriteEveryLevel(t *testing.T) {
	env := newFlEnv(t)
	for _, c := range []struct {
		who         core.Caller
		internalRef string
	}{{admin(), "REF-A"}, {sys("sys"), "REF-S"}} {
		stored := env.fullAccountRef(c.internalRef)
		change := changes("credit_limit", core.Int(1), "vault_code", text("9"))
		change.Tables = map[string][]core.Row{"ledger": {lline(5)}}
		updated, err := env.update("account", stored, change, c.who)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Values["credit_limit"].Int != 1 || updated.Values["vault_code"].Text != "9" {
			t.Error(describeDoc(updated))
		}
		sameRows(t, updated.Tables["ledger"], []core.Row{lline(5)}, "ledger")
	}
}

// Rust: field_levels.rs::a_table_at_level_one_is_hidden_and_protected
func TestATableAtLevelOneIsHiddenAndProtected(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	u := user("u", "User")
	change := core.Changes{Tables: map[string][]core.Row{"ledger": {}}}
	// Clearing the rows of a hidden table is an unknown field, and the rows stay.
	s, err := env.update("account", stored, change, u)
	want(t, message(t, s, err), "validation failed: unknown field `ledger`", "message")
	if len(env.get("account", stored.Name, admin()).Tables["ledger"]) != 2 {
		t.Error("the rows are gone")
	}
	// A Manager replaces them.
	change = core.Changes{Tables: map[string][]core.Row{"ledger": {lline(3)}}}
	if _, err := env.update("account", stored, change, user("m", "Manager")); err != nil {
		t.Fatal(err)
	}
}

// ----- Hooks ---------------------------------------------------------------------------------

// Rust: field_levels.rs::hooks_see_the_whole_document_during_an_update_by_a_user
func TestHooksSeeTheWholeDocumentDuringAnUpdateByAUser(t *testing.T) {
	env := newFlEnv(t)
	stored := env.fullAccount()
	env.spy.mu.Lock()
	env.spy.seen = nil
	env.spy.mu.Unlock()
	if _, err := env.update("account", stored, changes("title", text("Hooked")), user("u", "User")); err != nil {
		t.Fatal(err)
	}
	if len(env.spy.seen) != 1 || env.spy.seen[0].Kind != core.KindInt || env.spy.seen[0].Int != 500 {
		t.Errorf("%v", env.spy.seen)
	}
}

// ----- if_owner ------------------------------------------------------------------------------

// Rust: field_levels.rs::if_owner_at_level_zero_limits_the_documents_a_level_is_seen_on
//
// The list of the other's document with a filter on the level 1 field is ported with P3.
func TestIfOwnerAtLevelZeroLimitsTheDocumentsALevelIsSeenOn(t *testing.T) {
	env := newFlEnv(t)
	mine := user("mine", "Mine")
	own, err := env.insert("account", doc("title", text("own")), mine)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else's document with a level 1 value.
	other := env.fullAccount()
	// On the own document the level 1 read rule applies; the field is null but present.
	got := env.get("account", own.Name, mine)
	if v, ok := got.Values["credit_limit"]; !ok || v.Kind != core.KindNull {
		t.Error(describeDoc(got))
	}
	// The other's document is not found at all.
	if env.get("account", other.Name, mine) != nil {
		t.Error("found")
	}
	// Level 1 is read only for Mine: writing it is not permitted (and a level 0 write works).
	s, err := env.update("account", own, changes("credit_limit", core.Int(1)), mine)
	if m := message(t, s, err); !strings.Contains(m, "cannot be changed") {
		t.Error(m)
	}
	if _, err := env.update("account", own, changes("region", text("x")), mine); err != nil {
		t.Errorf("level 0: %v", err)
	}
}

// ----- Leak review ---------------------------------------------------------------------------

// Rust: field_levels.rs::errors_about_hidden_fields_name_neither_the_field_nor_the_value
func TestErrorsAboutHiddenFieldsNameNeitherTheFieldNorTheValue(t *testing.T) {
	env := newFlEnv(t)
	u := user("u", "User")

	// A value a hook sets that fails validation: the Administrator is told, the User is not.
	bad := doc("title", text("badhook"))
	s, err := env.insert("account", bad, admin())
	if m := message(t, s, err); !strings.Contains(m, "credit_limit") {
		t.Error(m)
	}
	s, err = env.insert("account", bad, u)
	message(t, s, err)
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrHidden}}, "hidden")
	if shown := err.Error(); strings.Contains(shown, "credit_limit") || strings.Contains(shown, "not a number") {
		t.Error(shown)
	}

	// A Link a hook sets to a missing document.
	ghost := doc("title", text("ghostlink"))
	s, err = env.insert("account", ghost, admin())
	if m := message(t, s, err); !strings.Contains(m, "partner") {
		t.Error(m)
	}
	s, err = env.insert("account", ghost, u)
	message(t, s, err)
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrHidden}}, "ghost")
	if strings.Contains(err.Error(), "partner") {
		t.Error(err)
	}

	// A unique collision on a hidden field (only a hook can set it): generic for the User.
	if _, err := env.insert("account", doc("title", text("dup")), admin()); err != nil {
		t.Fatal(err)
	}
	s, err = env.insert("account", doc("title", text("dup")), admin())
	if m := message(t, s, err); !strings.Contains(m, "internal_ref") {
		t.Error(m)
	}
	s, err = env.insert("account", doc("title", text("dup")), u)
	message(t, s, err)
	wantKind(t, err, db.ErrUniqueViolation)
	if shown := err.Error(); strings.Contains(shown, "internal_ref") || strings.Contains(shown, "same") {
		t.Error(shown)
	}

	// A stored hidden value that is not valid any more (the column was changed behind the framework's
	// back, or an option was removed later): the User updating another field gets a generic failure,
	// never the field or the value.
	stored := env.fullAccountRef("REF-2")
	env.d.Exec(fmt.Sprintf("UPDATE doc_account SET grade = 'zzz' WHERE name = '%s'", stored.Name))
	current := env.get("account", stored.Name, sys("sys"))
	s, err = env.update("account", current, changes("region", text("south")), u)
	message(t, s, err)
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrHidden}}, "stored")
	if strings.Contains(err.Error(), "grade") || strings.Contains(err.Error(), "zzz") {
		t.Error(err)
	}
	// The Administrator sees what is wrong.
	s, err = env.update("account", current, changes("region", text("south")), admin())
	if m := message(t, s, err); !strings.Contains(m, "grade") {
		t.Error(m)
	}
}

// ----- Submit, cancel, amend ----------------------------------------------------------------

// Rust: field_levels.rs::lifecycle_calls_return_redacted_documents_and_amend_keeps_hidden_values
func TestLifecycleCallsReturnRedactedDocumentsAndAmendKeepsHiddenValues(t *testing.T) {
	env := newFlEnv(t)
	manager, clerk := user("m", "Manager"), user("c", "Clerk")
	created, err := env.insert("invoice", doc("title", text("inv"), "internal_note", text("secret")), manager)
	if err != nil {
		t.Fatal(err)
	}
	if created.Values["internal_note"].Text != "secret" {
		t.Error(describeDoc(created))
	}
	// The Manager has no submit right: use the clerk, who cannot read the note.
	submitted, err := db.Submit(bg, env.d.Pool, env.reg, "invoice", created.Name, created.Version, clerk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := submitted.Values["internal_note"]; ok {
		t.Error("submit leaks the note")
	}
	cancelled, err := db.Cancel(bg, env.d.Pool, env.reg, "invoice", created.Name, submitted.Version, clerk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cancelled.Values["internal_note"]; ok {
		t.Error("cancel leaks the note")
	}
	amended, err := db.Amend(bg, env.d.Pool, env.reg, "invoice", created.Name, cancelled.Version, clerk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := amended.Values["internal_note"]; ok {
		t.Error("amend leaks the note")
	}
	// The copy is not caller input: it keeps the hidden value.
	if stored := env.get("invoice", amended.Name, admin()); stored.Values["internal_note"].Text != "secret" {
		t.Error(describeDoc(stored))
	}
}
