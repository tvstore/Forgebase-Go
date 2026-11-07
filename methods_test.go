package forgebase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// App method endpoints through the public API (M9.1, ADR 0019), with forgebase/testing: the gate
// (authentication, CSRF, roles, apps that a site does not install), what a method may do with documents as
// the caller, one transaction per call, the body and the errors, the startup rules, and the documents handle
// of a job (still the system context). Ported from crates/forgebase/tests/methods.rs.

const mNote = `
name = "note"
label = "Note"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "qty"
type = "int"

# A clerk sees and changes only the notes they own; an auditor only reads; the boss does it all.
[[permissions]]
role = "Clerk"
read = true
write = true
create = true
if_owner = true

[[permissions]]
role = "Auditor"
read = true

[[permissions]]
role = "Boss"
read = true
write = true
create = true
delete = true
`

const mTicket = `
name = "ticket"
label = "Ticket"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true

[[permissions]]
role = "Agent"
read = true
write = true
create = true

[[permissions]]
role = "Closer"
read = true
write = true

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
roles = ["Closer"]
`

func mDoc(code string) forgebase.Document {
	return forgebase.Document{Values: map[string]forgebase.Value{"code": forgebase.Text(code)}}
}

func mQty(n int64) forgebase.Changes {
	return forgebase.Changes{Values: map[string]forgebase.Value{"qty": forgebase.Int(n)}}
}

// mBody is what a test method reads of the request body.
type mBody struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
	Fail    bool   `json:"fail"`
}

func decodeBody(m *forgebase.MethodContext) mBody {
	body := mBody{Code: "none"}
	_ = json.Unmarshal(m.Body(), &body)
	return body
}

// A behavior is what a test method does. One handler with a behavior keeps the tests short.
type mBehavior string

const (
	mPing       mBehavior = "ping"
	mWhoami     mBehavior = "whoami"
	mEcho       mBehavior = "echo"
	mListNotes  mBehavior = "list_notes"
	mReadNote   mBehavior = "read_note"
	mAddNote    mBehavior = "add_note"
	mAddBad     mBehavior = "add_then_bad"
	mAddUnproc  mBehavior = "add_then_unprocessable"
	mAddPanic   mBehavior = "add_then_panic"
	mAddConfl   mBehavior = "add_then_conflict"
	mRefusedAdd mBehavior = "refused_then_add"
	mUpdateNote mBehavior = "update_note"
	mSwallowed  mBehavior = "swallowed_read"
	mSwallowGet mBehavior = "swallowed_get"
	mAddTicket  mBehavior = "add_ticket"
	mClose      mBehavior = "close_ticket"
	mEnqueue    mBehavior = "enqueue"
	mEnqUnknown mBehavior = "enqueue_unknown"
	mAddNamed   mBehavior = "add_then_error_value"
	mBadResult  mBehavior = "unmarshalable_result"
)

func shopMethod(b mBehavior) forgebase.MethodHandler {
	return func(ctx context.Context, m *forgebase.MethodContext) (any, error) {
		body := decodeBody(m)
		docs := m.Documents()
		switch b {
		case mPing:
			return map[string]any{"ok": true}, nil
		case mWhoami:
			c := m.Caller()
			return map[string]any{"user": c.Username(), "roles": c.Roles()}, nil
		case mEcho:
			return json.RawMessage(m.Body()), nil
		case mListNotes:
			notes, err := docs.List(ctx, "note", forgebase.NewListQuery().OrderBy("code", false))
			if err != nil {
				return nil, err
			}
			codes := []string{}
			for _, n := range notes {
				codes = append(codes, n.Name)
			}
			return codes, nil
		case mReadNote:
			found, err := docs.Get(ctx, "note", body.Code)
			if err != nil {
				return nil, err
			}
			return map[string]any{"found": found != nil}, nil
		case mAddNote:
			stored, err := docs.Insert(ctx, "note", mDoc(body.Code))
			if err != nil {
				return nil, err
			}
			return map[string]any{"name": stored.Name, "version": stored.Version}, nil
		case mAddBad:
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			return nil, forgebase.MethodBadRequest("Pick another code.")
		case mAddUnproc:
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			return nil, forgebase.MethodUnprocessable("The code is taken.")
		case mAddPanic:
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			panic("handler bug: secret detail")
		case mAddConfl:
			stored, err := docs.Insert(ctx, "note", mDoc(body.Code))
			if err != nil {
				return nil, err
			}
			if _, err := docs.Update(ctx, "note", stored.Name, 1, mQty(1)); err != nil {
				return nil, err
			}
			// The second update still says version 1: a conflict.
			if _, err := docs.Update(ctx, "note", stored.Name, 1, mQty(1)); err != nil {
				return nil, err
			}
			return nil, nil
		case mRefusedAdd:
			if _, err := docs.Insert(ctx, "note", forgebase.Document{}); err == nil {
				return nil, fmt.Errorf("an insert without a code was accepted")
			}
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			return map[string]any{"refused": true}, nil
		case mUpdateNote:
			stored, err := docs.Update(ctx, "note", body.Code, body.Version, mQty(7))
			if err != nil {
				return nil, err
			}
			return map[string]any{"version": stored.Version}, nil
		case mSwallowed:
			if err := swallowedRead(ctx, docs, body.Code); err != nil {
				return nil, err
			}
			return map[string]any{"swallowed": true}, nil
		case mSwallowGet:
			if err := swallowedGet(ctx, docs, body.Code); err != nil {
				return nil, err
			}
			return map[string]any{"swallowed": true}, nil
		case mAddTicket:
			stored, err := docs.Insert(ctx, "ticket", mDoc(body.Code))
			if err != nil {
				return nil, err
			}
			return map[string]any{"name": stored.Name, "version": stored.Version}, nil
		case mClose:
			stored, err := docs.Transition(ctx, "ticket", body.Name, "Close", body.Version)
			if err != nil {
				return nil, err
			}
			return map[string]any{"version": stored.Version}, nil
		case mEnqueue:
			if err := m.Enqueue(ctx, "shop.log", map[string]string{"code": body.Code}); err != nil {
				return nil, err
			}
			if body.Fail {
				return nil, forgebase.MethodBadRequest("No.")
			}
			return map[string]any{"queued": true}, nil
		case mEnqUnknown:
			if err := m.Enqueue(ctx, "shop.nobody", map[string]string{}); err != nil {
				return nil, err
			}
			return nil, nil
		case mAddNamed:
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			// An error wrapped by the app is still found: a MethodError inside fmt.Errorf.
			return nil, fmt.Errorf("while adding: %w", forgebase.MethodUnprocessable("Wrapped."))
		case mBadResult:
			if _, err := docs.Insert(ctx, "note", mDoc(body.Code)); err != nil {
				return nil, err
			}
			return map[string]any{"f": func() {}}, nil
		}
		return nil, nil
	}
}

// swallowedRead inserts the note <code>-1, makes a read that PostgreSQL itself refuses (a like pattern that
// ends with the escape character) and ignores the error, then inserts <code>-2.
func swallowedRead(ctx context.Context, docs *forgebase.Documents, code string) error {
	if _, err := docs.Insert(ctx, "note", mDoc(code+"-1")); err != nil {
		return err
	}
	// PostgreSQL raises the error when the match reaches the trailing backslash, which it does for the row
	// that was just inserted (a pattern that fails on its first characters never gets there).
	refused := forgebase.NewListQuery().Filter("code", forgebase.FilterLike, forgebase.Text(code+`-%\`))
	if _, err := docs.List(ctx, "note", refused); err == nil {
		return fmt.Errorf("the server accepted the pattern")
	}
	_, err := docs.Insert(ctx, "note", mDoc(code+"-2"))
	return err
}

// swallowedGet is the same with Get of a DocType without Table fields (a single statement, given its own
// savepoint by the documents handle): a name with a NUL character, which PostgreSQL refuses, is read and the
// error ignored; then <code>-2 is inserted.
func swallowedGet(ctx context.Context, docs *forgebase.Documents, code string) error {
	if _, err := docs.Insert(ctx, "note", mDoc(code+"-1")); err != nil {
		return err
	}
	if _, err := docs.Get(ctx, "note", "bad\x00name"); err == nil {
		return fmt.Errorf("the server accepted a name with a NUL character")
	}
	_, err := docs.Insert(ctx, "note", mDoc(code+"-2"))
	return err
}

// shop.swallow is the same through the documents handle of a job.
func swallowJob(ctx context.Context, job *forgebase.JobContext) error {
	return swallowedRead(ctx, job.Documents(), "job")
}

// shop.log inserts the note of the payload through the documents handle of a job, which is the system
// context: the users have no say. shop.bad inserts an invalid note.
func logJob(invalid bool) forgebase.JobHandler {
	return func(ctx context.Context, job *forgebase.JobContext) error {
		var p struct {
			Code string `json:"code"`
		}
		code := "none"
		if err := job.DecodePayload(&p); err == nil && p.Code != "" {
			code = p.Code
		}
		doc := mDoc(code)
		if invalid {
			doc = forgebase.Document{}
		}
		_, err := job.Documents().Insert(ctx, "note", doc)
		return err
	}
}

func mBase() *forgebase.App {
	return forgebase.NewApp("base").DocTypes(docTypes("doctypes/note.toml", mNote, "doctypes/ticket.toml", mTicket))
}

func mShop() *forgebase.App {
	m := func(b mBehavior) *forgebase.Method { return forgebase.NewMethod(shopMethod(b)) }
	writers := []string{"Clerk", "Auditor", "Boss"}
	return forgebase.NewApp("shop").
		DependsOn("base").
		Method("ping", m(mPing).AnyUser()).
		Method("whoami", m(mWhoami).AnyUser()).
		Method("echo", m(mEcho).AnyUser()).
		Method("boss_only", m(mPing).Roles("Boss")).
		Method("list_notes", m(mListNotes).Roles(writers...)).
		Method("read_note", m(mReadNote).Roles(writers...)).
		Method("add_note", m(mAddNote).Roles(writers...)).
		Method("add_then_bad", m(mAddBad).Roles(writers...)).
		Method("add_then_unprocessable", m(mAddUnproc).Roles(writers...)).
		Method("add_then_panic", m(mAddPanic).Roles(writers...)).
		Method("add_then_conflict", m(mAddConfl).Roles(writers...)).
		Method("refused_then_add", m(mRefusedAdd).Roles(writers...)).
		Method("update_note", m(mUpdateNote).Roles(writers...)).
		Method("swallowed_read", m(mSwallowed).Roles(writers...)).
		Method("swallowed_get", m(mSwallowGet).Roles(writers...)).
		Method("add_ticket", m(mAddTicket).Roles("Agent")).
		Method("close_ticket", m(mClose).Roles("Agent", "Closer")).
		Method("enqueue", m(mEnqueue).Roles("Clerk")).
		Method("enqueue_unknown", m(mEnqUnknown).Roles("Clerk")).
		Method("wrapped_error", m(mAddNamed).Roles("Clerk")).
		Method("bad_result", m(mBadResult).Roles("Clerk")).
		Job("log", logJob(false)).
		Job("bad", logJob(true)).
		Job("swallow", swallowJob)
}

func mBinary() *forgebase.Binary { return forgebase.New().App(mBase()).App(mShop()) }

func methodsReady(t *testing.T) *fbtest.DB {
	t.Helper()
	db := ready(t, mBinary())
	for name, roles := range map[string][]string{
		"ann": {"Clerk"}, "bob": {"Clerk"}, "ada": {"Auditor"}, "boss": {"Boss"},
		"root": {"Administrator"}, "gus": nil, "amy": {"Agent"}, "cleo": {"Closer"},
	} {
		db.CreateUser(name, roles...)
	}
	return db
}

func methodsLogin(db *fbtest.DB, user string) *fbtest.Client { return db.Client(mBinary()).Login(user) }

func callPath(method string) string { return "/api/v1/method/shop." + method }

func mCodes(db *fbtest.DB) []string {
	out := []string{}
	for _, r := range db.Rows("SELECT code FROM doc_note ORDER BY code") {
		out = append(out, r[0])
	}
	return out
}

func jobCount(db *fbtest.DB) string {
	return db.Rows("SELECT count(*)::text FROM fb_job WHERE name = 'shop.log'")[0][0]
}

func jsonOf(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func isData(t *testing.T, reply *fbtest.Reply, want string) {
	t.Helper()
	if !reflect.DeepEqual(reply.Data(), jsonOf(t, want)) {
		t.Errorf("data %#v, want %s (status %d, body %v)", reply.Data(), want, reply.Status, reply.Body)
	}
}

func isStatus(t *testing.T, reply *fbtest.Reply, status int) {
	t.Helper()
	if reply.Status != status {
		t.Fatalf("status %d, want %d: %v", reply.Status, status, reply.Body)
	}
}

// ---- the gate -------------------------------------------------------------------------------

// Rust: methods.rs::the_gate_is_authentication_csrf_and_the_roles
func TestTheGateIsAuthenticationCSRFAndTheRoles(t *testing.T) {
	db := methodsReady(t)
	anon := db.Client(mBinary())
	boss := anon.Login("boss")
	empty := map[string]any{}

	// Not authenticated: 401, whatever the method.
	isStatus(t, anon.Post(callPath("ping"), empty), 401)
	isStatus(t, anon.Post(callPath("nothing"), empty), 401)

	// A cookie call needs the CSRF header; a token call does not.
	noCSRF := boss.WithoutCSRF().Post(callPath("ping"), empty)
	isStatus(t, noCSRF, 403)
	if noCSRF.ErrorCode() != "csrf_failed" {
		t.Errorf("%v", noCSRF.Body)
	}
	token := db.CreateToken("boss")
	byToken := anon.WithToken(token).Post(callPath("ping"), empty)
	isStatus(t, byToken, 200)
	isData(t, byToken, `{"ok": true}`)
	isStatus(t, anon.WithToken("nokey:nosecret").Post(callPath("ping"), empty), 401)

	// The right role, the Administrator, and any user for an any-user method.
	reply := boss.Post(callPath("boss_only"), empty)
	isStatus(t, reply, 200)
	isData(t, reply, `{"ok": true}`)
	isStatus(t, anon.Login("root").Post(callPath("boss_only"), empty), 200)
	reply = anon.Login("gus").Post(callPath("whoami"), empty)
	isStatus(t, reply, 200)
	isData(t, reply, `{"user": "gus", "roles": []}`)

	// The wrong role, an unknown method and a method name that is not <app>.<method> are one and the same 404.
	ann := anon.Login("ann")
	refused := ann.Post(callPath("boss_only"), empty)
	unknown := ann.Post(callPath("nothing"), empty)
	otherApp := ann.Post("/api/v1/method/nobody.ping", empty)
	bare := ann.Post("/api/v1/method/ping", empty)
	isStatus(t, refused, 404)
	if refused.ErrorCode() != "not_found" {
		t.Errorf("%v", refused.Body)
	}
	for i, reply := range []*fbtest.Reply{unknown, otherApp, bare} {
		if reply.Status != 404 || !reflect.DeepEqual(reply.Body, refused.Body) {
			t.Errorf("reply %d: %d %v, want %v", i, reply.Status, reply.Body, refused.Body)
		}
	}

	// Another HTTP method on the route is 405.
	isStatus(t, boss.Get(callPath("ping")), 405)
	isStatus(t, boss.Patch(callPath("ping"), empty), 405)
	isStatus(t, boss.Delete(callPath("ping")), 405)
}

// Rust: methods.rs::the_methods_of_an_app_that_is_not_installed_do_not_exist
func TestTheMethodsOfAnAppThatIsNotInstalledDoNotExist(t *testing.T) {
	// A site that installs only base composes only base: the registry has no method of shop.
	onlyBase := forgebase.New().App(mBase())
	db := methodsReady(t)
	for _, who := range []string{"boss", "root", "gus"} {
		reply := db.Client(onlyBase).Login(who).Post(callPath("ping"), map[string]any{})
		isStatus(t, reply, 404)
		if reply.ErrorCode() != "not_found" {
			t.Errorf("%s: %v", who, reply.Body)
		}
	}
	isStatus(t, methodsLogin(db, "gus").Post(callPath("ping"), map[string]any{}), 200)
}

// ---- permissions inside the handler ---------------------------------------------------------

// Rust: methods.rs::a_method_reads_and_lists_only_what_the_caller_may
func TestAMethodReadsAndListsOnlyWhatTheCallerMay(t *testing.T) {
	db := methodsReady(t)
	ann, bob, ada, boss := methodsLogin(db, "ann"), methodsLogin(db, "bob"), methodsLogin(db, "ada"), methodsLogin(db, "boss")
	for _, c := range []struct {
		client *fbtest.Client
		code   string
	}{{ann, "a1"}, {ann, "a2"}, {bob, "b1"}} {
		reply := c.client.Post(callPath("add_note"), map[string]any{"code": c.code})
		isStatus(t, reply, 200)
		isData(t, reply, fmt.Sprintf(`{"name": %q, "version": 1}`, c.code))
	}
	// if_owner: a clerk lists and gets only their own notes.
	isData(t, ann.Post(callPath("list_notes"), map[string]any{}), `["a1", "a2"]`)
	isData(t, bob.Post(callPath("list_notes"), map[string]any{}), `["b1"]`)
	// The auditor and the boss read everything.
	isData(t, ada.Post(callPath("list_notes"), map[string]any{}), `["a1", "a2", "b1"]`)
	isData(t, boss.Post(callPath("list_notes"), map[string]any{}), `["a1", "a2", "b1"]`)
	read := func(c *fbtest.Client, code string) any {
		return c.Post(callPath("read_note"), map[string]any{"code": code}).DataObject()["found"]
	}
	expect(t, read(ann, "a1"), true)
	expect(t, read(bob, "a1"), false)
	expect(t, read(ada, "a1"), true)
	// The owner is the caller.
	expect(t, db.Rows("SELECT owner FROM doc_note WHERE code = 'b1'"), [][]string{{"bob"}})
}

// Rust: methods.rs::a_write_without_the_right_is_permission_denied
func TestAWriteWithoutTheRightIsPermissionDenied(t *testing.T) {
	db := methodsReady(t)
	reply := methodsLogin(db, "ada").Post(callPath("add_note"), map[string]any{"code": "x"})
	isStatus(t, reply, 403)
	if reply.ErrorCode() != "permission_denied" {
		t.Errorf("%v", reply.Body)
	}
	expect(t, mCodes(db), []string{})
}

// Rust: methods.rs::an_update_without_the_right_or_of_a_note_that_is_not_hers_is_refused
func TestAnUpdateWithoutTheRightOrOfANoteThatIsNotHersIsRefused(t *testing.T) {
	db := methodsReady(t)
	ann, bob, ada, boss := methodsLogin(db, "ann"), methodsLogin(db, "bob"), methodsLogin(db, "ada"), methodsLogin(db, "boss")
	isStatus(t, bob.Post(callPath("add_note"), map[string]any{"code": "b1"}), 200)
	row := func() [][]string {
		return db.Rows("SELECT version::text, coalesce(qty::text, '-'), modified_by FROM doc_note")
	}
	before := row()
	expect(t, before, [][]string{{"1", "-", "bob"}})

	// The auditor reads but has no write right: 403 permission_denied.
	denied := ada.Post(callPath("update_note"), map[string]any{"code": "b1", "version": 1})
	isStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%v", denied.Body)
	}
	expect(t, row(), before)

	// A clerk may write only her own notes (if_owner): another clerk's note is not found.
	hidden := ann.Post(callPath("update_note"), map[string]any{"code": "b1", "version": 1})
	isStatus(t, hidden, 404)
	if hidden.ErrorCode() != "not_found" {
		t.Errorf("%v", hidden.Body)
	}
	expect(t, row(), before)

	// The owner and the boss can.
	isStatus(t, bob.Post(callPath("update_note"), map[string]any{"code": "b1", "version": 1}), 200)
	isStatus(t, boss.Post(callPath("update_note"), map[string]any{"code": "b1", "version": 2}), 200)
	expect(t, row(), [][]string{{"3", "7", "boss"}})
}

// Rust: methods.rs::a_read_the_server_refuses_and_the_handler_ignores_loses_nothing
func TestAReadTheServerRefusesAndTheHandlerIgnoresLosesNothing(t *testing.T) {
	db := methodsReady(t)
	reply := methodsLogin(db, "ann").Post(callPath("swallowed_read"), map[string]any{"code": "n"})
	isStatus(t, reply, 200)
	isData(t, reply, `{"swallowed": true}`)
	// Both writes, the one before the failed read and the one after it, were committed.
	expect(t, mCodes(db), []string{"n-1", "n-2"})

	// The same through the documents handle of a job.
	queueJob(db, "shop.swallow", "{}")
	expect(t, counts(fbtest.RunJobs(mBinary(), db)), [3]int{1, 0, 0})
	expect(t, mCodes(db), []string{"job-1", "job-2", "n-1", "n-2"})
}

// Rust: methods.rs::a_get_the_server_refuses_and_the_handler_ignores_loses_nothing
func TestAGetTheServerRefusesAndTheHandlerIgnoresLosesNothing(t *testing.T) {
	db := methodsReady(t)
	reply := methodsLogin(db, "ann").Post(callPath("swallowed_get"), map[string]any{"code": "g"})
	isStatus(t, reply, 200)
	isData(t, reply, `{"swallowed": true}`)
	expect(t, mCodes(db), []string{"g-1", "g-2"})
}

// Rust: methods.rs::a_workflow_transition_in_a_method_uses_the_callers_roles
func TestAWorkflowTransitionInAMethodUsesTheCallersRoles(t *testing.T) {
	db := methodsReady(t)
	amy, cleo := methodsLogin(db, "amy"), methodsLogin(db, "cleo")
	isStatus(t, amy.Post(callPath("add_ticket"), map[string]any{"code": "t1"}), 200)
	body := map[string]any{"name": "t1", "version": 1}
	// The agent has the write right but is not a Closer.
	denied := amy.Post(callPath("close_ticket"), body)
	isStatus(t, denied, 403)
	if denied.ErrorCode() != "permission_denied" {
		t.Errorf("%v", denied.Body)
	}
	expect(t, db.Rows("SELECT workflow_state FROM doc_ticket"), [][]string{{"Open"}})
	closed := cleo.Post(callPath("close_ticket"), body)
	isStatus(t, closed, 200)
	isData(t, closed, `{"version": 2}`)
	expect(t, db.Rows("SELECT workflow_state FROM doc_ticket"), [][]string{{"Closed"}})
	// The workflow log records the user.
	expect(t, db.Rows("SELECT action, actor FROM fb_workflow_log"), [][]string{{"Close", "cleo"}})
}

// ---- one transaction ------------------------------------------------------------------------

// Rust: methods.rs::an_error_rolls_everything_back_and_a_document_error_keeps_its_status
func TestAnErrorRollsEverythingBackAndADocumentErrorKeepsItsStatus(t *testing.T) {
	db := methodsReady(t)
	ann := methodsLogin(db, "ann")

	bad := ann.Post(callPath("add_then_bad"), map[string]any{"code": "n1"})
	isStatus(t, bad, 400)
	if bad.ErrorCode() != "method_error" || bad.ErrorMessage() != "Pick another code." {
		t.Errorf("%v", bad.Body)
	}
	unprocessable := ann.Post(callPath("add_then_unprocessable"), map[string]any{"code": "n2"})
	isStatus(t, unprocessable, 422)
	if unprocessable.ErrorCode() != "method_error" || unprocessable.ErrorMessage() != "The code is taken." {
		t.Errorf("%v", unprocessable.Body)
	}
	// A MethodError inside an error the app wrapped is found too.
	wrapped := ann.Post(callPath("wrapped_error"), map[string]any{"code": "n5"})
	isStatus(t, wrapped, 422)
	if wrapped.ErrorMessage() != "Wrapped." {
		t.Errorf("%v", wrapped.Body)
	}
	// A version conflict inside the handler is the document API's 409.
	conflict := ann.Post(callPath("add_then_conflict"), map[string]any{"code": "n3"})
	isStatus(t, conflict, 409)
	if conflict.ErrorCode() != "conflict" {
		t.Errorf("%v", conflict.Body)
	}
	// Nothing was left behind by any of them.
	expect(t, mCodes(db), []string{})

	// A refused write that the handler handles leaves the transaction usable: the next insert commits.
	handled := ann.Post(callPath("refused_then_add"), map[string]any{"code": "n4"})
	isStatus(t, handled, 200)
	expect(t, mCodes(db), []string{"n4"})
}

// Rust: methods.rs::an_enqueued_job_exists_only_if_the_method_commits
func TestAnEnqueuedJobExistsOnlyIfTheMethodCommits(t *testing.T) {
	db := methodsReady(t)
	ann := methodsLogin(db, "ann")
	failed := ann.Post(callPath("enqueue"), map[string]any{"code": "j1", "fail": true})
	isStatus(t, failed, 400)
	expect(t, jobCount(db), "0")

	queued := ann.Post(callPath("enqueue"), map[string]any{"code": "j2"})
	isStatus(t, queued, 200)
	expect(t, jobCount(db), "1")
	expect(t, db.Rows("SELECT enqueued_by FROM fb_job WHERE name = 'shop.log'"), [][]string{{"ann"}})
	// The job runs later as the system context: the user's rights are not its rights.
	run := fbtest.RunJobs(mBinary(), db)
	expect(t, [2]int{run.Done, run.Dead}, [2]int{1, 0})
	expect(t, mCodes(db), []string{"j2"})
	expect(t, db.Rows("SELECT owner FROM doc_note"), [][]string{{"job:shop.log"}})
}

// ---- the body -------------------------------------------------------------------------------

// Rust: methods.rs::the_body_is_a_json_object_and_an_empty_one_is_an_empty_object
func TestTheBodyIsAJSONObjectAndAnEmptyOneIsAnEmptyObject(t *testing.T) {
	db := methodsReady(t)
	gus := methodsLogin(db, "gus")
	echo := callPath("echo")

	given := gus.Post(echo, map[string]any{"a": []int{1, 2}, "b": nil})
	isData(t, given, `{"a": [1, 2], "b": null}`)
	// Empty, with or without a content type.
	for _, contentType := range []string{"", "application/json"} {
		reply := gus.PostRaw(echo, contentType, nil)
		isStatus(t, reply, 200)
		isData(t, reply, `{}`)
		reply = gus.PostRaw(echo, contentType, []byte{})
		isStatus(t, reply, 200)
		isData(t, reply, `{}`)
	}
	// Content types with parameters and +json are JSON.
	for _, contentType := range []string{"application/json; charset=utf-8", "application/vnd.x+json"} {
		isData(t, gus.PostRaw(echo, contentType, []byte(`{"k":1}`)), `{"k": 1}`)
	}
	// Not an object, or not JSON at all: 400 before the handler runs.
	for _, body := range []string{"[1]", `"x"`, "null", "1", "{", "not json", " "} {
		reply := gus.PostRaw(echo, "application/json", []byte(body))
		if reply.Status != 400 || reply.ErrorCode() != "bad_request" {
			t.Errorf("%q: %d %v", body, reply.Status, reply.Body)
		}
	}
	// A body without a JSON content type is 415.
	for _, contentType := range []string{"", "text/plain"} {
		reply := gus.PostRaw(echo, contentType, []byte("{}"))
		if reply.Status != 415 || reply.ErrorCode() != "unsupported_media_type" {
			t.Errorf("%q: %d %v", contentType, reply.Status, reply.Body)
		}
	}
	// Over the body limit (1 MiB by default).
	big := []byte(strings.Repeat(" ", 1024*1024+1))
	reply := gus.PostRaw(echo, "application/json", big)
	if reply.Status != 413 || reply.ErrorCode() != "payload_too_large" {
		t.Errorf("%d %v", reply.Status, reply.Body)
	}
	// Query parameters are refused, as elsewhere.
	reply = gus.Post(echo+"?x=1", map[string]any{})
	if reply.Status != 400 || reply.ErrorCode() != "invalid_query" {
		t.Errorf("%d %v", reply.Status, reply.Body)
	}
}

// ---- errors ---------------------------------------------------------------------------------

func assertGeneric500(t *testing.T, reply *fbtest.Reply) {
	t.Helper()
	isStatus(t, reply, 500)
	want := `{"error": {"code": "internal", "message": "internal server error"}}`
	if !reflect.DeepEqual(reply.Body, jsonOf(t, want)) {
		t.Errorf("%v", reply.Body)
	}
}

// Rust: methods.rs::an_internal_error_is_a_generic_500_and_a_panic_does_not_stop_the_server
func TestAnInternalErrorIsAGeneric500AndAPanicDoesNotStopTheServer(t *testing.T) {
	db := methodsReady(t)
	ann := methodsLogin(db, "ann")

	// An app that enqueues a job that does not exist: its mistake, not the caller's.
	assertGeneric500(t, ann.Post(callPath("enqueue_unknown"), map[string]any{}))

	// A panic: a generic 500 with nothing of the panic message, and nothing left behind.
	panicked := ann.Post(callPath("add_then_panic"), map[string]any{"code": "p1"})
	assertGeneric500(t, panicked)
	if strings.Contains(panicked.Text, "secret") {
		t.Errorf("the panic message leaked: %s", panicked.Text)
	}
	expect(t, mCodes(db), []string{})
	// A result that cannot be written as JSON is a generic 500 too, and rolls the call back.
	assertGeneric500(t, ann.Post(callPath("bad_result"), map[string]any{"code": "p3"}))
	expect(t, mCodes(db), []string{})
	// The server and its pool still work, and the next call commits.
	next := ann.Post(callPath("add_note"), map[string]any{"code": "p2"})
	isStatus(t, next, 200)
	expect(t, mCodes(db), []string{"p2"})
	// No connection of the pool was left in a transaction by the panic.
	expect(t, db.Rows("SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'"),
		[][]string{{"0"}})
}

// ---- startup rules --------------------------------------------------------------------------

func methodProblems(t *testing.T, app *forgebase.App) string {
	t.Helper()
	err := forgebase.New().App(app).Check()
	if err == nil {
		t.Fatal("a startup error was expected")
	}
	return err.Error()
}

// Rust: methods.rs::a_method_needs_exactly_one_kind_of_access_and_a_valid_unique_name
func TestAMethodNeedsExactlyOneKindOfAccessAndAValidUniqueName(t *testing.T) {
	ping := func() *forgebase.Method { return forgebase.NewMethod(shopMethod(mPing)) }
	// Fine: both kinds, in two methods.
	ok := forgebase.NewApp("a").
		Method("by_role", ping().Roles("X")).
		Method("any", ping().AnyUser()).
		Method("more_roles", ping().Roles("X").Roles("Y"))
	if err := forgebase.New().App(ok).Check(); err != nil {
		t.Fatal(err)
	}

	text := methodProblems(t, forgebase.NewApp("a").
		Method("neither", ping()).
		Method("empty", ping().Roles()).
		Method("both", ping().Roles("X").AnyUser()).
		Method("Bad Name", ping().AnyUser()).
		Method("dup", ping().AnyUser()).
		Method("dup", ping().AnyUser()).
		Method("bad_role", ping().Roles(" ")).
		Method("nil_handler", forgebase.NewMethod(nil).AnyUser()).
		Method("nil_method", nil))
	for _, expected := range []string{
		"app `a`: method `neither`: no access given",
		"app `a`: method `empty`: an empty role list",
		"app `a`: method `both`: both .Roles(..) and .AnyUser() are given",
		"app `a`: method `Bad Name`: name \"Bad Name\"",
		"app `a`: method `dup` is registered more than once",
		"app `a`: method `bad_role`: invalid role",
		"app `a`: method `nil_handler` has no handler",
		"app `a`: method `nil_method` has no handler",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("%q is not in:\n%s", expected, text)
		}
	}
}

// ---- jobs: the documents handle is still the system context ---------------------------------

// Rust: methods.rs::a_jobs_documents_handle_is_still_the_system_context
func TestAJobsDocumentsHandleIsStillTheSystemContext(t *testing.T) {
	db := methodsReady(t)
	// ann may write only her own notes, and the job is not her: it writes as the system context, as
	// job:shop.log.
	queued := methodsLogin(db, "ann").Post(callPath("enqueue"), map[string]any{"code": "sys"})
	isStatus(t, queued, 200)
	expect(t, fbtest.RunJobs(mBinary(), db).Done, 1)
	expect(t, mCodes(db), []string{"sys"})
	queueJob(db, "shop.bad", "{}")
	// A refusal (validation) is permanent: the job is dead after one attempt, as before.
	run := fbtest.RunJobs(mBinary(), db)
	expect(t, [3]int{run.Done, run.Dead, run.Retried}, [3]int{0, 1, 0})
}
