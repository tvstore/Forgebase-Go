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

// Permission enforcement (M3.2, ADR 0011) against a real PostgreSQL: the authorization matrix,
// if_owner, deny by default, the union of roles, hidden documents that look missing (also for stale
// versions and links), and what errors may say. Ported from crates/forgebase-db/tests/permissions.rs.
//
// Deferred, with the phase that has what they need: the export and import rights of the matrix (P7)
// and the export, transition and available_transitions parts of the_matrix_with_user_permissions (P6
// and P7). Each place says so. The calls to list are ported (P3).

// permTicket is submittable, one role per right (each also reads), and Own, which has every right
// on own documents only. A Link to party in the document and in its rows.
const permTicket = `
name = "ticket"
label = "Ticket"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "customer"
type = "link"
target = "party"
[[fields]]
name = "lines"
type = "table"
target = "ticket_line"

[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Writer"
read = true
write = true
[[permissions]]
role = "Creator"
read = true
create = true
[[permissions]]
role = "Deleter"
read = true
delete = true
[[permissions]]
role = "Submitter"
read = true
submit = true
[[permissions]]
role = "Canceller"
read = true
cancel = true
[[permissions]]
role = "Amender"
read = true
amend = true
[[permissions]]
role = "WriteOnly"
write = true
[[permissions]]
role = "Own"
read = true
write = true
create = true
delete = true
submit = true
cancel = true
amend = true
if_owner = true
[[permissions]]
role = "LevelOnly"
level = 1
read = true
write = true
`

const permTicketLine = `
name = "ticket_line"
label = "Ticket Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "contact"
type = "link"
target = "party"
`

// permAgentCase is a workflow DocType with the export right, for the matrix of user permissions
// (M10.3): Agent reads, exports and takes the transition "Close".
const permAgentCase = `
name = "agent_case"
label = "Agent Case"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "customer"
type = "link"
target = "party"
[[permissions]]
role = "Agent"
read = true
write = true
export = true
[workflow]
[[workflow.states]]
name = "Open"
docstatus = 0
[[workflow.states]]
name = "Closed"
docstatus = 0
[[workflow.transitions]]
action = "Close"
from = "Open"
to = "Closed"
roles = ["Agent"]
`

// permParty is a Link target: Reader reads all, OwnReader reads own, PartyAdmin reads and deletes all.
const permParty = `
name = "party"
label = "Party"
naming = { field = "email" }
[[fields]]
name = "email"
type = "data"
required = true
unique = true

[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "OwnReader"
read = true
if_owner = true
[[permissions]]
role = "PartyAdmin"
read = true
delete = true
[[permissions]]
role = "PartyCreator"
create = true
`

// permMemo is readable by the whole team, but only the owner may change or delete.
const permMemo = `
name = "memo"
label = "Memo"
[[fields]]
name = "body"
type = "text"

[[permissions]]
role = "Team"
read = true
create = true
[[permissions]]
role = "Team"
write = true
delete = true
if_owner = true
`

// permSecret has no rules at all: deny by default.
const permSecret = `
name = "secret"
label = "Secret"
[[fields]]
name = "body"
type = "text"
`

func permRegistry(t *testing.T) *core.Registry {
	return registry(t, permTicket, permTicketLine, permParty, permMemo, permSecret, permAgentCase)
}

func permSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := permRegistry(t)
	mustSync(t, d, reg)
	return d, reg
}

func admin() core.Caller { return user("root", core.Administrator) }

// permRow is a row of a ticket with an optional contact ("" for none).
func permRow(contact string) core.Row {
	r := row("qty", core.Int(1), "contact", core.Null())
	if contact != "" {
		r["contact"] = text(contact)
	}
	return r
}

func makeTicket(t *testing.T, d *dbtest.DB, reg *core.Registry, owner core.Caller) *db.StoredDocument {
	t.Helper()
	return mustInsert(t, d, reg, "ticket", doc("title", text("t")), owner)
}

func makeParty(t *testing.T, d *dbtest.DB, reg *core.Registry, email string, owner core.Caller) *db.StoredDocument {
	t.Helper()
	return mustInsert(t, d, reg, "party", doc("email", text(email)), owner)
}

// attempt is a ticket made by the system and moved to the state right needs, then right attempted by
// caller. The result of the attempt only.
func attempt(t *testing.T, d *dbtest.DB, reg *core.Registry, right core.Right, caller core.Caller) error {
	t.Helper()
	if right == core.RightCreate {
		_, err := db.Insert(bg, d.Pool, reg, "ticket", doc("title", text("n")), caller)
		return err
	}
	made := makeTicket(t, d, reg, sys("sys"))
	name, version := made.Name, made.Version
	// Move the document to the status the right works on.
	if right == core.RightCancel || right == core.RightAmend {
		version = mustSubmit(t, d, reg, "ticket", name, version, sys("sys")).Version
	}
	if right == core.RightAmend {
		version = mustCancel(t, d, reg, "ticket", name, version, sys("sys")).Version
	}
	switch right {
	case core.RightRead:
		got, err := db.Get(bg, d.Pool, reg, "ticket", name, caller)
		if err != nil {
			return err
		}
		if got == nil {
			return &db.Error{Kind: db.ErrNotFound, DocType: "ticket", Name: name}
		}
		return nil
	case core.RightWrite:
		_, err := db.Update(bg, d.Pool, reg, "ticket", name, version, changes("title", text("x")), caller)
		return err
	case core.RightDelete:
		return db.Delete(bg, d.Pool, reg, "ticket", name, version, caller)
	case core.RightSubmit:
		_, err := db.Submit(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	case core.RightCancel:
		_, err := db.Cancel(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	case core.RightAmend:
		_, err := db.Amend(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	}
	// Export and import are rights of a file, not of one document: the Rust test calls the export
	// and the import here; they arrive with P7.
	t.Fatalf("right %v is not an operation of this matrix (export and import are P7)", right)
	return nil
}

// roleOf is the role that grants each right of the matrix (the order of the Rust ROLE_OF).
var roleOf = []struct {
	right core.Right
	role  string
}{
	{core.RightRead, "Reader"}, {core.RightWrite, "Writer"}, {core.RightCreate, "Creator"},
	{core.RightDelete, "Deleter"}, {core.RightSubmit, "Submitter"}, {core.RightCancel, "Canceller"},
	{core.RightAmend, "Amender"},
}

func isDenied(err error, doctype string, right core.Right) bool {
	var e *db.Error
	return errorsAs(err, &e) && e.Kind == db.ErrPermissionDenied && e.DocType == doctype && e.Right == right
}

// Rust: permissions.rs::every_right_is_allowed_with_its_role_and_denied_without
func TestEveryRightIsAllowedWithItsRoleAndDeniedWithout(t *testing.T) {
	d, reg := permSetup(t)
	for _, c := range roleOf {
		if err := attempt(t, d, reg, c.right, user("u", c.role)); err != nil {
			t.Errorf("%s with %s: %v", c.right, c.role, err)
		}
		if err := attempt(t, d, reg, c.right, user("u")); !isDenied(err, "ticket", c.right) {
			t.Errorf("%s without a role: %v", c.right, err)
		}
		if err := attempt(t, d, reg, c.right, user("u", "Nobody", "reader")); !isDenied(err, "ticket", c.right) {
			t.Errorf("%s with an unrelated role: %v", c.right, err)
		}
		// Every other role (each of which also reads) is denied this right, except for read.
		for _, other := range roleOf {
			if other.right == c.right {
				continue
			}
			err := attempt(t, d, reg, c.right, user("u", other.role))
			if c.right == core.RightRead {
				if err != nil {
					t.Errorf("read with %s: %v", other.role, err)
				}
			} else if !isDenied(err, "ticket", c.right) {
				t.Errorf("%s with %s: %v", c.right, other.role, err)
			}
		}
	}
	// Write without read: the document cannot even be looked at.
	if err := attempt(t, d, reg, core.RightWrite, user("u", "WriteOnly")); !isDenied(err, "ticket", core.RightWrite) {
		t.Error(err)
	}
	if err := attempt(t, d, reg, core.RightRead, user("u", "WriteOnly")); !isDenied(err, "ticket", core.RightRead) {
		t.Error(err)
	}
}

// Rust: permissions.rs::a_rule_of_a_field_level_grants_no_document_right
//
// M10.2 (ADR 0024): a rule of a level above 0 is about fields; it grants no document right, with or
// without a level 0 role next to it.
func TestARuleOfAFieldLevelGrantsNoDocumentRight(t *testing.T) {
	d, reg := permSetup(t)
	for _, c := range roleOf {
		if err := attempt(t, d, reg, c.right, user("u", "LevelOnly")); !isDenied(err, "ticket", c.right) {
			t.Errorf("%s with a level rule only: %v", c.right, err)
		}
		// Next to the role of another right it adds nothing: the matrix is the same.
		for _, other := range roleOf {
			if other.right == c.right {
				continue
			}
			err := attempt(t, d, reg, c.right, user("u", "LevelOnly", other.role))
			if c.right == core.RightRead {
				if err != nil {
					t.Errorf("read with %s and a level rule: %v", other.role, err)
				}
			} else if !isDenied(err, "ticket", c.right) {
				t.Errorf("%s with %s and a level rule: %v", c.right, other.role, err)
			}
		}
	}
}

// Rust: permissions.rs::administrator_and_the_system_context_bypass_every_rule
func TestAdministratorAndTheSystemContextBypassEveryRule(t *testing.T) {
	d, reg := permSetup(t)
	// The seven rights of the matrix; export and import are P7.
	for _, c := range roleOf {
		for _, caller := range []core.Caller{admin(), sys("sys"), user("u", "Nobody", core.Administrator)} {
			if err := attempt(t, d, reg, c.right, caller); err != nil {
				t.Errorf("%s as %+v: %v", c.right, caller, err)
			}
		}
	}
	// A role that merely looks like the built-in one is no administrator.
	if err := attempt(t, d, reg, core.RightRead, user("u", "administrator")); !isDenied(err, "ticket", core.RightRead) {
		t.Error(err)
	}
}

// Rust: permissions.rs::a_doctype_without_rules_is_denied_to_users_and_open_to_administrator_and_system
func TestADoctypeWithoutRulesIsDeniedToUsersAndOpenToAdministratorAndSystem(t *testing.T) {
	d, reg := permSetup(t)
	made := mustInsert(t, d, reg, "secret", doc("body", text("s")), admin())
	nobody := user("u", "Reader", "Writer", "Own", "Team", "Anything")
	_, err := db.Insert(bg, d.Pool, reg, "secret", doc(), nobody)
	if !isDenied(err, "secret", core.RightCreate) {
		t.Error(err)
	}
	_, err = db.Get(bg, d.Pool, reg, "secret", made.Name, nobody)
	if !isDenied(err, "secret", core.RightRead) {
		t.Error(err)
	}
	_, err = db.List(bg, d.Pool, reg, "secret", nobody, db.ListQuery{})
	if !isDenied(err, "secret", core.RightRead) {
		t.Error(err)
	}
	_, err = db.Update(bg, d.Pool, reg, "secret", made.Name, 1, changes("body", text("x")), nobody)
	if !isDenied(err, "secret", core.RightWrite) {
		t.Error(err)
	}
	if err := db.Delete(bg, d.Pool, reg, "secret", made.Name, 1, nobody); !isDenied(err, "secret", core.RightDelete) {
		t.Error(err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "secret", made.Name, sys("sys")); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "secret", made.Name, admin()); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	if listed, err := db.List(bg, d.Pool, reg, "secret", admin(), db.ListQuery{}); err != nil || len(listed) != 1 {
		t.Errorf("%v %v", listed, err)
	}
}

// Rust: permissions.rs::roles_are_a_union
func TestRolesAreAUnion(t *testing.T) {
	d, reg := permSetup(t)
	both := user("u", "Reader", "Writer")
	if err := attempt(t, d, reg, core.RightRead, both); err != nil {
		t.Error(err)
	}
	if err := attempt(t, d, reg, core.RightWrite, both); err != nil {
		t.Error(err)
	}
	if err := attempt(t, d, reg, core.RightDelete, both); !isDenied(err, "ticket", core.RightDelete) {
		t.Error(err)
	}
	// Own alone covers only own documents, Reader adds reading everything.
	others := makeTicket(t, d, reg, user("zoe", "Own"))
	if got, err := db.Get(bg, d.Pool, reg, "ticket", others.Name, user("ann", "Own")); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	annBoth := user("ann", "Own", "Reader")
	if got, err := db.Get(bg, d.Pool, reg, "ticket", others.Name, annBoth); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	// ... but writing stays limited to her own documents.
	_, err := db.Update(bg, d.Pool, reg, "ticket", others.Name, others.Version, changes("title", text("x")), annBoth)
	if !isDenied(err, "ticket", core.RightWrite) {
		t.Error(err)
	}
}

// hiddenErrors is every operation as caller on name with a stale version 99, all of which must fail
// the same way for a document the caller cannot read.
func hiddenErrors(t *testing.T, d *dbtest.DB, reg *core.Registry, name string, caller core.Caller) []string {
	t.Helper()
	var out []string
	_, err := db.Update(bg, d.Pool, reg, "ticket", name, 99, changes("title", text("x")), caller)
	out = append(out, errText(t, err))
	out = append(out, errText(t, db.Delete(bg, d.Pool, reg, "ticket", name, 99, caller)))
	_, err = db.Submit(bg, d.Pool, reg, "ticket", name, 99, caller)
	out = append(out, errText(t, err))
	_, err = db.Cancel(bg, d.Pool, reg, "ticket", name, 99, caller)
	out = append(out, errText(t, err))
	_, err = db.Amend(bg, d.Pool, reg, "ticket", name, 99, caller)
	out = append(out, errText(t, err))
	return out
}

func errText(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}

// Rust: permissions.rs::a_document_of_somebody_else_is_invisible_under_if_owner
func TestADocumentOfSomebodyElseIsInvisibleUnderIfOwner(t *testing.T) {
	d, reg := permSetup(t)
	ann, bob := user("ann", "Own"), user("bob", "Own")
	anns := makeTicket(t, d, reg, ann)
	bobs := makeTicket(t, d, reg, bob)
	// get: own is there, the other's is nil, exactly like a name that does not exist.
	if got, err := db.Get(bg, d.Pool, reg, "ticket", anns.Name, ann); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "ticket", bobs.Name, ann); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "ticket", "no-such", ann); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	// Every mutation: NotFound, and the same message as for a missing name, whatever the version (a
	// stale version must not turn into Conflict, a right the caller has must not turn into
	// PermissionDenied).
	hidden := hiddenErrors(t, d, reg, bobs.Name, ann)
	missing := hiddenErrors(t, d, reg, "no-such", ann)
	for i := range hidden {
		if !strings.Contains(hidden[i], "not found") ||
			strings.ReplaceAll(hidden[i], bobs.Name, "X") != strings.ReplaceAll(missing[i], "no-such", "X") {
			t.Errorf("%q vs %q", hidden[i], missing[i])
		}
	}
	// The right version does not help either.
	_, err := db.Update(bg, d.Pool, reg, "ticket", bobs.Name, bobs.Version, changes("title", text("x")), ann)
	wantKind(t, err, db.ErrNotFound)
	// Nothing of bob's changed, and ann's own document works, including a stale version, which is a
	// Conflict only for a document she can see.
	after := mustGet(t, d, reg, "ticket", bobs.Name, bob)
	if after.Version != 1 || after.Values["title"].Text != "t" {
		t.Error(describeDoc(after))
	}
	_, err = db.Update(bg, d.Pool, reg, "ticket", anns.Name, 99, changes("title", text("x")), ann)
	if e := wantKind(t, err, db.ErrConflict); e.CurrentVersion != 1 {
		t.Errorf("%+v", e)
	}
	updated := mustUpdate(t, d, reg, "ticket", anns.Name, 1, changes("title", text("mine")), ann)
	if updated.ModifiedBy != "ann" || updated.Owner != "ann" {
		t.Error(describeDoc(updated))
	}
}

// Rust: permissions.rs::a_readable_document_of_somebody_else_is_permission_denied_for_an_if_owner_write
func TestAReadableDocumentOfSomebodyElseIsPermissionDeniedForAnIfOwnerWrite(t *testing.T) {
	d, reg := permSetup(t)
	ann, bob := user("ann", "Team"), user("bob", "Team")
	anns := mustInsert(t, d, reg, "memo", doc("body", text("a")), ann)
	// Bob reads it ...
	if got, err := db.Get(bg, d.Pool, reg, "memo", anns.Name, bob); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	// ... but cannot change or delete it, with the right or a stale version alike.
	for _, version := range []int64{1, 99} {
		_, err := db.Update(bg, d.Pool, reg, "memo", anns.Name, version, changes("body", text("x")), bob)
		if !isDenied(err, "memo", core.RightWrite) {
			t.Error(err)
		}
		if err := db.Delete(bg, d.Pool, reg, "memo", anns.Name, version, bob); !isDenied(err, "memo", core.RightDelete) {
			t.Error(err)
		}
	}
	// The owner can.
	mustUpdate(t, d, reg, "memo", anns.Name, 1, changes("body", text("y")), ann)
	if err := db.Delete(bg, d.Pool, reg, "memo", anns.Name, 2, ann); err != nil {
		t.Fatal(err)
	}
}

// Rust: permissions.rs::amend_needs_a_readable_original_and_the_new_draft_belongs_to_the_caller
func TestAmendNeedsAReadableOriginalAndTheNewDraftBelongsToTheCaller(t *testing.T) {
	d, reg := permSetup(t)
	ann, bob := user("ann", "Own"), user("bob", "Own")
	made := makeTicket(t, d, reg, ann)
	v := mustSubmit(t, d, reg, "ticket", made.Name, 1, ann).Version
	v = mustCancel(t, d, reg, "ticket", made.Name, v, ann).Version
	_, err := db.Amend(bg, d.Pool, reg, "ticket", made.Name, v, bob)
	wantKind(t, err, db.ErrNotFound)
	// An administrator can amend it; the draft is the administrator's.
	amended := mustAmend(t, d, reg, "ticket", made.Name, v, admin())
	if amended.Owner != "root" || amended.ModifiedBy != "root" {
		t.Error(describeDoc(amended))
	}
	// Ann amends her own cancelled document (after the amendment is deleted).
	if err := db.Delete(bg, d.Pool, reg, "ticket", amended.Name, 1, admin()); err != nil {
		t.Fatal(err)
	}
	if again := mustAmend(t, d, reg, "ticket", made.Name, v, ann); again.Owner != "ann" {
		t.Error(describeDoc(again))
	}
}

// Rust: permissions.rs::child_rows_follow_the_parent_and_need_no_rules_of_their_own
func TestChildRowsFollowTheParentAndNeedNoRulesOfTheirOwn(t *testing.T) {
	d, reg := permSetup(t)
	ann := user("ann", "Own")
	withRows := doc("title", text("t"))
	withRows.Tables["lines"] = []core.Row{permRow(""), permRow("")}
	made := mustInsert(t, d, reg, "ticket", withRows, ann)
	if len(made.Tables["lines"]) != 2 {
		t.Error(describeDoc(made))
	}
	if fetched := mustGet(t, d, reg, "ticket", made.Name, ann); len(fetched.Tables["lines"]) != 2 {
		t.Error(describeDoc(fetched))
	}
	// The child DocType has no rules and is not addressable at all.
	_, err := db.Get(bg, d.Pool, reg, "ticket_line", "x", admin())
	wantKind(t, err, db.ErrChildDocType)
	_, err = db.Get(bg, d.Pool, reg, "ticket_line", "x", ann)
	wantKind(t, err, db.ErrChildDocType)
}

// Rust: permissions.rs::invalid_caller_names_are_rejected
func TestInvalidCallerNamesAreRejected(t *testing.T) {
	d, reg := permSetup(t)
	for _, name := range []string{"", "a\x00b"} {
		for _, caller := range []core.Caller{core.UserCaller(name, []string{core.Administrator}), sys(name)} {
			_, err := db.Insert(bg, d.Pool, reg, "ticket", doc("title", text("t")), caller)
			wantKind(t, err, db.ErrInvalidActor)
		}
	}
}

// ---- Decision 9: links and what errors say -----------------------------------------------------

// linkNotFound is the LinkNotFound problems of a validation error as (field:target, name) pairs, a
// problem in a row as table[row].field:target.
func linkNotFound(t *testing.T, err error) [][2]string {
	t.Helper()
	var out [][2]string
	for _, e := range validationOf(t, err) {
		switch {
		case e.Kind == core.ErrLinkNotFound:
			out = append(out, [2]string{e.Field + ":" + e.Target, e.Name})
		case e.Kind == core.ErrInRow && e.Inner.Kind == core.ErrLinkNotFound:
			out = append(out, [2]string{fmt.Sprintf("%s[%d].%s:%s", e.Field, e.Row, e.Inner.Field, e.Inner.Target), e.Inner.Name})
		default:
			t.Fatalf("unexpected %v", e)
		}
	}
	return out
}

// ticketLinking is a ticket linking to customer and, with contact, to it in a row ("" for no row).
func ticketLinking(customer, contact string) core.Document {
	document := doc("title", text("t"), "customer", text(customer))
	if contact != "" {
		document.Tables["lines"] = []core.Row{permRow(contact)}
	}
	return document
}

// Rust: permissions.rs::a_link_to_a_document_the_caller_cannot_read_is_reported_as_missing
func TestALinkToADocumentTheCallerCannotReadIsReportedAsMissing(t *testing.T) {
	d, reg := permSetup(t)
	makeParty(t, d, reg, "real@x.y", sys("sys"))
	// Own may create tickets but has no right on party at all.
	blind := user("ann", "Own")
	for _, customer := range []string{"real@x.y", "ghost@x.y"} {
		_, err := db.Insert(bg, d.Pool, reg, "ticket", ticketLinking(customer, customer), blind)
		// The same errors for the hidden and for the missing target (names differ, as given).
		want(t, linkNotFound(t, err), [][2]string{{"customer:party", customer}, {"lines[1].contact:party", customer}}, "errors")
		want(t, err.Error(), fmt.Sprintf("validation failed: field `customer`: party `%s` does not exist; "+
			"field `lines` row 1: field `contact`: party `%s` does not exist", customer, customer), "text")
	}
	// The same on update: the real target is "missing" for a caller who cannot read it.
	annTicket := makeTicket(t, d, reg, blind)
	_, err := db.Update(bg, d.Pool, reg, "ticket", annTicket.Name, 1, changes("customer", text("real@x.y")), blind)
	want(t, linkNotFound(t, err), [][2]string{{"customer:party", "real@x.y"}}, "update")
	// Nothing was linked or locked: the system can delete the party again.
	party := mustGet(t, d, reg, "party", "real@x.y", sys("sys"))
	if err := db.Delete(bg, d.Pool, reg, "party", "real@x.y", party.Version, sys("sys")); err != nil {
		t.Fatal(err)
	}
}

// Rust: permissions.rs::an_if_owner_read_resolves_only_own_link_targets
func TestAnIfOwnerReadResolvesOnlyOwnLinkTargets(t *testing.T) {
	d, reg := permSetup(t)
	makeParty(t, d, reg, "mine@x.y", user("ann", "PartyCreator"))
	makeParty(t, d, reg, "theirs@x.y", user("bob", "PartyCreator"))
	ann := user("ann", "Own", "OwnReader")
	ok := mustInsert(t, d, reg, "ticket", ticketLinking("mine@x.y", "mine@x.y"), ann)
	if ok.Values["customer"].Text != "mine@x.y" {
		t.Error(describeDoc(ok))
	}
	_, err := db.Insert(bg, d.Pool, reg, "ticket", ticketLinking("theirs@x.y", ""), ann)
	want(t, linkNotFound(t, err), [][2]string{{"customer:party", "theirs@x.y"}}, "errors")
	// A caller with full read access on party resolves both.
	ok = mustInsert(t, d, reg, "ticket", ticketLinking("theirs@x.y", ""), user("ann", "Own", "Reader"))
	if ok.Values["customer"].Text != "theirs@x.y" {
		t.Error(describeDoc(ok))
	}
}

// Rust: permissions.rs::amend_resolves_links_with_the_callers_rights
func TestAmendResolvesLinksWithTheCallersRights(t *testing.T) {
	d, reg := permSetup(t)
	makeParty(t, d, reg, "real@x.y", sys("sys"))
	made := mustInsert(t, d, reg, "ticket", ticketLinking("real@x.y", ""), sys("sys"))
	v := mustSubmit(t, d, reg, "ticket", made.Name, 1, sys("sys")).Version
	v = mustCancel(t, d, reg, "ticket", made.Name, v, sys("sys")).Version
	// Amender reads and amends tickets but cannot read party.
	_, err := db.Amend(bg, d.Pool, reg, "ticket", made.Name, v, user("u", "Amender"))
	want(t, linkNotFound(t, err), [][2]string{{"customer:party", "real@x.y"}}, "errors")
	// Reader reads tickets and parties alike: now the link resolves.
	ok := mustAmend(t, d, reg, "ticket", made.Name, v, user("u", "Amender", "Reader"))
	if ok.Values["customer"].Text != "real@x.y" {
		t.Error(describeDoc(ok))
	}
}

// Rust: permissions.rs::a_still_linked_error_names_the_linking_doctype_only_to_a_caller_who_can_read_it
func TestAStillLinkedErrorNamesTheLinkingDoctypeOnlyToACallerWhoCanReadIt(t *testing.T) {
	d, reg := permSetup(t)
	party := makeParty(t, d, reg, "p@x.y", sys("sys"))
	mustInsert(t, d, reg, "ticket", ticketLinking("p@x.y", ""), sys("sys"))

	blind := user("u", "PartyAdmin") // reads and deletes parties, nothing on tickets
	err := db.Delete(bg, d.Pool, reg, "party", party.Name, party.Version, blind)
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy != nil {
		t.Errorf("%+v", e)
	}
	want(t, err.Error(), "party `p@x.y` cannot be deleted: it is linked from another document", "message")

	// With read access to tickets the DocType is named.
	sighted := user("u", "PartyAdmin", "Reader")
	err = db.Delete(bg, d.Pool, reg, "party", party.Name, party.Version, sighted)
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy == nil || *e.ReferencedBy != "ticket" {
		t.Errorf("%+v", e)
	}
	if !strings.Contains(err.Error(), "ticket") {
		t.Error(err)
	}
	// A link in a child row names the owning (parent) DocType, to a reader of it only.
	lone := makeParty(t, d, reg, "q@x.y", sys("sys"))
	mustInsert(t, d, reg, "ticket", ticketLinking("p@x.y", "q@x.y"), sys("sys"))
	err = db.Delete(bg, d.Pool, reg, "party", lone.Name, lone.Version, blind)
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy != nil {
		t.Errorf("%+v", e)
	}
	err = db.Delete(bg, d.Pool, reg, "party", lone.Name, lone.Version, sighted)
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy == nil || *e.ReferencedBy != "ticket" {
		t.Errorf("%+v", e)
	}
	// The system (and an administrator) see the DocType.
	err = db.Delete(bg, d.Pool, reg, "party", lone.Name, lone.Version, sys("sys"))
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy == nil {
		t.Errorf("%+v", e)
	}
}

// Rust: permissions.rs::permission_denied_never_names_a_document
func TestPermissionDeniedNeverNamesADocument(t *testing.T) {
	d, reg := permSetup(t)
	made := makeTicket(t, d, reg, sys("sys"))
	_, err := db.Update(bg, d.Pool, reg, "ticket", made.Name, 1, changes("title", text("x")), user("u", "Reader"))
	want(t, errText(t, err), "permission denied: no `write` right on DocType `ticket`", "message")
	if strings.Contains(err.Error(), made.Name) {
		t.Error("the message names the document")
	}
}

// ---- Hooks -------------------------------------------------------------------------------------

// actorSpy logs the actor each hook sees.
type actorSpy struct {
	core.BaseHooks
	mu  sync.Mutex
	log []string
}

func (s *actorSpy) add(what string, ctx core.HookContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, what+":"+ctx.Actor)
}

func (s *actorSpy) BeforeInsert(ctx core.HookContext, _ *core.Document) error {
	s.add("insert", ctx)
	return nil
}
func (s *actorSpy) Validate(ctx core.HookContext, _ *core.Document) error {
	s.add("validate", ctx)
	return nil
}
func (s *actorSpy) BeforeDelete(ctx core.HookContext, _ *core.Document) error {
	s.add("delete", ctx)
	return nil
}
func (s *actorSpy) BeforeCancel(ctx core.HookContext, _ *core.Document) error {
	s.add("cancel", ctx)
	return nil
}

// Rust: permissions.rs::hooks_receive_the_username_and_the_owner_is_the_username
func TestHooksReceiveTheUsernameAndTheOwnerIsTheUsername(t *testing.T) {
	spy := &actorSpy{}
	reg := registry(t, permTicket, permTicketLine, permParty, permMemo, permSecret)
	if err := reg.AddHooks("ticket", spy); err != nil {
		t.Fatal(err)
	}
	d := dbtest.Migrated(t)
	mustSync(t, d, reg)
	ann := user("ann", "Own")
	made := mustInsert(t, d, reg, "ticket", doc("title", text("t")), ann)
	if made.Owner != "ann" {
		t.Error(made.Owner)
	}
	v := mustSubmit(t, d, reg, "ticket", made.Name, 1, ann).Version
	v = mustCancel(t, d, reg, "ticket", made.Name, v, ann).Version
	if err := db.Delete(bg, d.Pool, reg, "ticket", made.Name, v, ann); err != nil {
		t.Fatal(err)
	}
	want(t, spy.log, []string{"insert:ann", "validate:ann", "validate:ann", "cancel:ann", "delete:ann"}, "hooks")
	spy.log = nil
	// A refused operation runs no hook.
	_, err := db.Insert(bg, d.Pool, reg, "ticket", doc("title", text("t")), user("bob"))
	wantKind(t, err, db.ErrPermissionDenied)
	if len(spy.log) != 0 {
		t.Error(spy.log)
	}
}

// Rust: permissions.rs::stored_links_to_unreadable_targets_do_not_block_update_or_submit
//
// Links already stored on a document do not block update and submit when the caller cannot read
// their targets; only new or changed Link values get the caller's read check.
func TestStoredLinksToUnreadableTargetsDoNotBlockUpdateOrSubmit(t *testing.T) {
	d, reg := permSetup(t)
	hidden := makeParty(t, d, reg, "hidden@x.y", sys("sys"))
	other := makeParty(t, d, reg, "other@x.y", sys("sys"))
	u := user("u", "Writer", "Submitter", "OwnReader", "PartyCreator")
	own := mustInsert(t, d, reg, "party", doc("email", text("own@x.y")), u)
	// u cannot read hidden and other, but can read own.
	if got, err := db.Get(bg, d.Pool, reg, "party", hidden.Name, u); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	made := mustInsert(t, d, reg, "ticket", ticketLinking(hidden.Name, hidden.Name), sys("sys"))
	// Only the title changes: the stored Links (document and row) are kept.
	updated := mustUpdate(t, d, reg, "ticket", made.Name, made.Version, changes("title", text("changed")), u)
	if updated.Values["customer"].Text != hidden.Name {
		t.Error(describeDoc(updated))
	}
	// Replacing the rows: a stored name is kept, a new unreadable one is reported as missing.
	rows := func(contacts ...string) core.Changes {
		var rs []core.Row
		for _, c := range contacts {
			rs = append(rs, permRow(c))
		}
		return core.Changes{Tables: map[string][]core.Row{"lines": rs}}
	}
	_, err := db.Update(bg, d.Pool, reg, "ticket", made.Name, updated.Version, rows(hidden.Name, other.Name), u)
	want(t, linkNotFound(t, err), [][2]string{{"lines[2].contact:party", other.Name}}, "rows")
	updated = mustUpdate(t, d, reg, "ticket", made.Name, updated.Version, rows(hidden.Name, own.Name), u)
	if len(updated.Tables["lines"]) != 2 {
		t.Error(describeDoc(updated))
	}
	// Changing the document Link to another unreadable party is still refused; to an own one it
	// works.
	_, err = db.Update(bg, d.Pool, reg, "ticket", made.Name, updated.Version, changes("customer", text(other.Name)), u)
	want(t, linkNotFound(t, err), [][2]string{{"customer:party", other.Name}}, "customer")
	updated = mustUpdate(t, d, reg, "ticket", made.Name, updated.Version, changes("customer", text(own.Name)), u)
	if updated.Values["customer"].Text != own.Name {
		t.Error(describeDoc(updated))
	}
	// Submit with stored Links to unreadable targets (a second ticket, rows included).
	second := mustInsert(t, d, reg, "ticket", ticketLinking(hidden.Name, hidden.Name), sys("sys"))
	submitted := mustSubmit(t, d, reg, "ticket", second.Name, second.Version, u)
	if statusOf(submitted) != "submitted" {
		t.Error(describeDoc(submitted))
	}
}

// ---- Stored Links are kept per field and per table ---------------------------------------------

// permCase has two Link fields with different targets and two child tables, one Link in each. The
// caller (CaseWriter) can read neither party nor vendor.
const permCase = `
name = "case"
label = "Case"
[[fields]]
name = "a"
type = "link"
target = "party"
[[fields]]
name = "b"
type = "link"
target = "vendor"
[[fields]]
name = "rows"
type = "table"
target = "case_row"
[[fields]]
name = "others"
type = "table"
target = "case_other"
[[permissions]]
role = "CaseWriter"
read = true
write = true
create = true
`

const permCaseRow = `
name = "case_row"
label = "Case Row"
is_child = true
[[fields]]
name = "a_ref"
type = "link"
target = "party"
`

const permCaseOther = `
name = "case_other"
label = "Case Other"
is_child = true
[[fields]]
name = "b_ref"
type = "link"
target = "vendor"
`

const permVendor = `
name = "vendor"
label = "Vendor"
naming = { field = "email" }
[[fields]]
name = "email"
type = "data"
required = true
unique = true
`

// Rust: permissions.rs::the_kept_rule_is_scoped_per_field_and_per_table
//
// A stored Link is kept only in the field (and table) it is stored in: the same name moved to another
// field of another target DocType, or to the other table, is a new value and is checked against the
// caller's read access like any other.
func TestTheKeptRuleIsScopedPerFieldAndPerTable(t *testing.T) {
	d, reg := setup(t, permCase, permCaseRow, permCaseOther, permParty, permVendor, permTicket, permTicketLine, permMemo, permSecret)
	// The same names exist as party and as vendor, so only the permission can refuse them.
	for _, name := range []string{"x1@x.y", "y1@x.y"} {
		makeParty(t, d, reg, name, sys("sys"))
		mustInsert(t, d, reg, "vendor", doc("email", text(name)), sys("sys"))
	}
	u := user("u", "CaseWriter")
	stored := doc("a", text("x1@x.y"), "b", text("y1@x.y"))
	stored.Tables["rows"] = []core.Row{row("a_ref", text("x1@x.y"))}
	stored.Tables["others"] = []core.Row{row("b_ref", text("y1@x.y"))}
	made := mustInsert(t, d, reg, "case", stored, sys("sys"))

	rows := func(table, field, name string) core.Changes {
		return core.Changes{Tables: map[string][]core.Row{table: {row(field, text(name))}}}
	}
	version := made.Version
	// Refused: a stored name moved to the other Link field.
	for _, c := range []struct {
		changes  core.Changes
		expected string
	}{
		{changes("b", text("x1@x.y")), "b:vendor"},
		{changes("a", text("y1@x.y")), "a:party"},
		// Moved between the tables: each table keeps only its own stored value.
		{rows("others", "b_ref", "x1@x.y"), "others[1].b_ref:vendor"},
		{rows("rows", "a_ref", "y1@x.y"), "rows[1].a_ref:party"},
	} {
		_, err := db.Update(bg, d.Pool, reg, "case", made.Name, version, c.changes, u)
		found := linkNotFound(t, err)
		if len(found) != 1 || found[0][0] != c.expected {
			t.Errorf("%s: %v", c.expected, found)
		}
	}
	// Kept: the same values in their own fields and tables (a changed title would be no change at all,
	// so the Links are rewritten as they are stored).
	same := core.Changes{
		Values: pairs("a", text("x1@x.y"), "b", text("y1@x.y")),
		Tables: map[string][]core.Row{
			"rows":   {row("a_ref", text("x1@x.y")), row("a_ref", text("x1@x.y"))},
			"others": {row("b_ref", text("y1@x.y"))},
		},
	}
	updated := mustUpdate(t, d, reg, "case", made.Name, version, same, u)
	if len(updated.Tables["rows"]) != 2 || updated.Version != made.Version+1 {
		t.Error(describeDoc(updated))
	}
}

// linker sets the customer Link in before_submit.
type linker struct {
	core.BaseHooks
	target string
}

func (l linker) BeforeSubmit(_ core.HookContext, doc *core.Document) error {
	doc.Values["customer"] = text(l.target)
	return nil
}

// Rust: permissions.rs::a_before_submit_hook_cannot_link_to_an_unreadable_document
//
// A before_submit hook that sets a Link to a document the caller cannot read gets the same
// LinkNotFound as the caller would for their own input: hooks do not widen read access.
func TestABeforeSubmitHookCannotLinkToAnUnreadableDocument(t *testing.T) {
	reg := registry(t, permTicket, permTicketLine, permParty, permMemo, permSecret)
	if err := reg.AddHooks("ticket", linker{target: "hidden@x.y"}); err != nil {
		t.Fatal(err)
	}
	d := dbtest.Migrated(t)
	mustSync(t, d, reg)
	hidden := makeParty(t, d, reg, "hidden@x.y", sys("sys"))
	made := makeTicket(t, d, reg, sys("sys"))
	// Submitter reads tickets but has no right on party.
	_, err := db.Submit(bg, d.Pool, reg, "ticket", made.Name, made.Version, user("u", "Submitter"))
	want(t, linkNotFound(t, err), [][2]string{{"customer:party", hidden.Name}}, "errors")
	// Nothing was persisted: still a draft, and the same hook is fine for a caller who can read it.
	stored := mustGet(t, d, reg, "ticket", made.Name, sys("sys"))
	if stored.Version != made.Version || stored.Values["customer"].Kind != core.KindNull {
		t.Error(describeDoc(stored))
	}
	submitted := mustSubmit(t, d, reg, "ticket", made.Name, made.Version, user("v", "Submitter", "Reader"))
	if submitted.Values["customer"].Text != hidden.Name {
		t.Error(describeDoc(submitted))
	}
}

// ---- the matrix with user permissions (M10.3, ADR 0025) ----------------------------------------

// attemptLinked is a ticket made by the system with this Link ("" for none), moved to the state right
// needs, then right attempted by caller: the result of the attempt only (a list counts as a read of
// that one ticket).
func attemptLinked(t *testing.T, d *dbtest.DB, reg *core.Registry, right core.Right, caller core.Caller, customer string) error {
	t.Helper()
	document := doc("title", text("m"))
	if customer != "" {
		document.Values["customer"] = text(customer)
	}
	made := mustInsert(t, d, reg, "ticket", document, sys("sys"))
	name, version := made.Name, made.Version
	if right == core.RightCancel || right == core.RightAmend {
		version = mustSubmit(t, d, reg, "ticket", name, version, sys("sys")).Version
	}
	if right == core.RightAmend {
		version = mustCancel(t, d, reg, "ticket", name, version, sys("sys")).Version
	}
	switch right {
	case core.RightRead:
		// Both paths: get, and the page of a list that finds the ticket by its name.
		got, err := db.Get(bg, d.Pool, reg, "ticket", name, caller)
		if err != nil {
			return err
		}
		listed, err := db.List(bg, d.Pool, reg, "ticket", caller, db.ListQuery{Filters: []db.Filter{f("name", db.Eq(text(name)))}})
		if err != nil {
			return err
		}
		if (got != nil) != (len(listed) == 1) {
			t.Errorf("get and list disagree: %v and %d documents", got != nil, len(listed))
		}
		if got == nil {
			return &db.Error{Kind: db.ErrNotFound, DocType: "ticket", Name: name}
		}
		return nil
	case core.RightWrite:
		_, err := db.Update(bg, d.Pool, reg, "ticket", name, version, changes("title", text("x")), caller)
		return err
	case core.RightDelete:
		return db.Delete(bg, d.Pool, reg, "ticket", name, version, caller)
	case core.RightSubmit:
		_, err := db.Submit(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	case core.RightCancel:
		_, err := db.Cancel(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	case core.RightAmend:
		_, err := db.Amend(bg, d.Pool, reg, "ticket", name, version, caller)
		return err
	}
	t.Fatalf("not an operation of this matrix: %v", right)
	return nil
}

// Rust: permissions.rs::the_matrix_with_user_permissions
//
// Every right of the matrix on tickets whose Link to party is allowed, empty or hidden by the user
// permissions of the caller: the same rights, the same roles, one more dimension. A hidden ticket is
// NotFound for every operation (before the right, the version or the state), a visible one behaves as
// without the restriction, and the Administrator and the system ignore the rows.
//
// Not ported here: the workflow transition and available_transitions on agent_case (P6) and the
// export (P7).
func TestTheMatrixWithUserPermissions(t *testing.T) {
	d, reg := permSetup(t)
	makeParty(t, d, reg, "ok@x.y", sys("sys"))
	makeParty(t, d, reg, "other@x.y", sys("sys"))
	allRights := []string{"Reader", "Writer", "Creator", "Deleter", "Submitter", "Canceller", "Amender"}
	restrictions := core.NewUserRestrictions([][2]string{{"party", "ok@x.y"}})
	restricted := user("rita", allRights...).WithRestrictions(restrictions)
	plain := user("pat", allRights...)
	adminRestricted := user("root", core.Administrator).WithRestrictions(restrictions)

	// (the Link of the ticket, whether the restricted user sees it)
	tickets := []struct {
		customer string
		visible  bool
	}{{"ok@x.y", true}, {"", true}, {"other@x.y", false}}
	for _, right := range []core.Right{core.RightRead, core.RightWrite, core.RightDelete, core.RightSubmit, core.RightCancel, core.RightAmend} {
		for _, ticket := range tickets {
			for _, who := range []struct {
				caller  core.Caller
				visible bool
				label   string
			}{
				{restricted, ticket.visible, "restricted"},
				{plain, true, "unrestricted"},
				{adminRestricted, true, "administrator"},
				{sys("sys"), true, "system"},
			} {
				err := attemptLinked(t, d, reg, right, who.caller, ticket.customer)
				var e *db.Error
				switch {
				case err == nil && who.visible:
				case !who.visible && errorsAs(err, &e) && e.Kind == db.ErrNotFound:
				default:
					t.Errorf("%s on a ticket linked to %q by %s: %v", right, ticket.customer, who.label, err)
				}
			}
		}
	}

	// Creating, and the links a write resolves: a hidden target is a missing one for the restricted
	// user, in the document and in a row, on insert and on update.
	for _, c := range []struct {
		customer, contact string
		ok                bool
	}{
		{"ok@x.y", "ok@x.y", true},
		{"other@x.y", "", false},
		{"ok@x.y", "other@x.y", false},
	} {
		_, err := db.Insert(bg, d.Pool, reg, "ticket", ticketLinking(c.customer, c.contact), restricted)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: %v", c.customer, c.contact, err)
		}
		if !c.ok && len(linkNotFound(t, err)) == 0 {
			t.Error("no LinkNotFound")
		}
		mustInsert(t, d, reg, "ticket", ticketLinking(c.customer, c.contact), plain)
	}
	// The restricted DocType itself: the party the user may see is the only one they read, and they
	// cannot create one (the system and the unrestricted user can).
	if got, err := db.Get(bg, d.Pool, reg, "party", "other@x.y", restricted); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "party", "ok@x.y", restricted); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	creator := user("rita", "PartyCreator").WithRestrictions(restrictions)
	_, err := db.Insert(bg, d.Pool, reg, "party", doc("email", text("n@x.y")), creator)
	if !isDenied(err, "party", core.RightCreate) {
		t.Error(err)
	}
}
