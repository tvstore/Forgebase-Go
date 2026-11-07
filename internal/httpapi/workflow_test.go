package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The workflow routes over HTTP (M8.1, ADR 0015): transition, the list of what a caller may take, the
// history, and what plain submit and cancel and update do on a DocType with a workflow, against a real
// PostgreSQL. Ported from crates/forgebase-http/tests/workflow.rs (the metadata test of that file is in
// meta_test.go).

// wf is the users: one per case of the authorization matrix, each with an API token (no CSRF).
type wf struct {
	e                                          *env
	admin, clerk, boss, rita, ada, sam, nobody string
}

func newWF(t *testing.T) *wf {
	t.Helper()
	e := newEnvWith(t, DefaultConfig(), purchaseDoc)
	w := &wf{e: e}
	for _, u := range []struct {
		name  string
		role  string
		token *string
	}{
		{"admin", "Administrator", &w.admin}, {"clerk", "Clerk", &w.clerk}, {"boss", "Manager", &w.boss},
		{"rita", "Reviewer", &w.rita}, {"ada", "Auditor", &w.ada}, {"sam", "Own Writer", &w.sam}, {"nobody", "Other", &w.nobody},
	} {
		e.user(u.name, u.role)
		*u.token = e.apiToken(u.name)
	}
	return w
}

func wfURI(name, tail string) string { return "/api/v1/resource/purchase/" + name + tail }

func (w *wf) insert(token, title string) map[string]any {
	w.e.t.Helper()
	rep := w.e.send(post("/api/v1/resource/purchase").token(token).json(J{"title": title}))
	wantStatus(w.e.t, rep, http.StatusCreated, rep.text)
	return rep.dataObj()
}

func (w *wf) take(token string, doc map[string]any, action string) reply {
	w.e.t.Helper()
	return w.e.send(post(wfURI(doc["name"].(string), "/transition")).token(token).json(J{"action": action, "version": doc["version"]}))
}

// walk takes the actions as the Administrator.
func (w *wf) walk(doc map[string]any, actions ...string) map[string]any {
	w.e.t.Helper()
	for _, a := range actions {
		rep := w.take(w.admin, doc, a)
		wantStatus(w.e.t, rep, http.StatusOK, a, rep.text)
		doc = rep.dataObj()
	}
	return doc
}

func (w *wf) get(token, tail, name string) reply {
	w.e.t.Helper()
	return w.e.send(get(wfURI(name, tail)).token(token))
}

func docName(doc map[string]any) string { return doc["name"].(string) }

// Rust: workflow.rs::a_new_document_is_in_the_initial_state_and_a_transition_returns_it
func TestHTTPANewDocumentIsInTheInitialStateAndATransitionReturnsIt(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.clerk, "a")
	eqJSON(t, doc["workflow_state"], `"Draft"`)
	eqJSON(t, doc["docstatus"], `0`)
	eqJSON(t, doc["version"], `1`)

	rep := w.take(w.clerk, doc, "Send")
	wantStatus(t, rep, http.StatusOK, rep.text)
	sent := rep.dataObj()
	eqJSON(t, sent["workflow_state"], `"Pending"`)
	eqJSON(t, sent["docstatus"], `0`)
	eqJSON(t, sent["version"], `2`)
	eqJSON(t, sent["name"], fmt.Sprintf("%q", docName(doc)))
	// The document that get returns is the same.
	eqJSON(t, w.get(w.clerk, "", docName(doc)).data(), rep.text[len(`{"data":`):len(rep.text)-1])

	// Approve is 0 to 1: the document is submitted.
	approved := w.take(w.boss, sent, "Approve").dataObj()
	eqJSON(t, approved["workflow_state"], `"Approved"`)
	eqJSON(t, approved["docstatus"], `1`)
	cancelled := w.take(w.boss, approved, "Cancel").dataObj()
	eqJSON(t, cancelled["workflow_state"], `"Cancelled"`)
	eqJSON(t, cancelled["docstatus"], `2`)
}

// Rust: workflow.rs::the_transition_route_needs_csrf_like_the_other_changes
func TestHTTPTheTransitionRouteNeedsCsrfLikeTheOtherChanges(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.clerk, "a")
	s := w.e.login("clerk")
	body := J{"action": "Send", "version": 1}
	url := wfURI(docName(doc), "/transition")
	// A cookie without the CSRF token is refused; with it the transition is taken.
	wantCode(t, w.e.send(post(url).cookie(s).json(body)), http.StatusForbidden, "csrf_failed")
	rep := w.e.send(post(url).session(s).json(body))
	wantStatus(t, rep, http.StatusOK, rep.text)
	// No credentials at all.
	wantStatus(t, w.e.send(post(url).json(body)), http.StatusUnauthorized)
	// The body limit and the content type are those of the other routes.
	wantStatus(t, w.e.send(post(url).token(w.clerk).raw("text/plain", []byte("{}"))), http.StatusUnsupportedMediaType)
}

// Rust: workflow.rs::the_error_codes_of_a_transition
func TestHTTPTheErrorCodesOfATransition(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.clerk, "a")
	sent := w.take(w.clerk, doc, "Send").dataObj()

	// 403 for the role without the right, the right without the role, and no read right.
	for _, c := range []struct{ who, why string }{{w.rita, "role without right"}, {w.ada, "right without role"}, {w.nobody, "no read right"}} {
		wantCode(t, w.take(c.who, sent, "Approve"), http.StatusForbidden, "permission_denied", c.why)
	}
	// 404: a missing document, and a DocType that does not exist.
	wantCode(t, w.e.send(post(wfURI("missing", "/transition")).token(w.boss).json(J{"action": "Approve", "version": 1})),
		http.StatusNotFound, "not_found")
	wantStatus(t, w.e.send(post("/api/v1/resource/nothing/x/transition").token(w.boss).json(J{"action": "Approve", "version": 1})), http.StatusNotFound)
	// 409 unknown_transition: a wrong state and an unknown action.
	for _, action := range []string{"Send", "Nope"} {
		rep := w.take(w.boss, sent, action)
		wantCode(t, rep, http.StatusConflict, "unknown_transition", action)
		if !strings.Contains(rep.errorObj()["message"].(string), "`"+action+"`") {
			t.Fatal(rep.text)
		}
	}
	// 409 conflict: a stale version.
	stale := map[string]any{"name": sent["name"], "version": 1}
	wantCode(t, w.take(w.boss, stale, "Approve"), http.StatusConflict, "conflict")
	// 409 no_workflow: a DocType without one (also for a document that does not exist).
	for _, target := range []string{"/api/v1/resource/bill/x/transition", "/api/v1/resource/note/x/transition"} {
		wantCode(t, w.e.send(post(target).token(w.admin).json(J{"action": "Send", "version": 1})), http.StatusConflict, "no_workflow", target)
	}
	// 400 for the wrong shape of the body.
	for _, body := range []any{
		J{"version": 2}, J{"action": 5, "version": 2}, J{"action": "Approve"}, J{"action": "Approve", "version": "2"},
		J{"action": "Approve", "version": 2, "extra": true}, []string{"Approve"},
	} {
		wantCode(t, w.e.send(post(wfURI(docName(sent), "/transition")).token(w.boss).json(body)), http.StatusBadRequest, "bad_request", fmt.Sprint(body))
	}
	// Nothing was changed by any of it.
	eqJSON(t, w.get(w.boss, "", docName(sent)).data(), mustJSON(t, sent))
}

// Rust: workflow.rs::if_owner_decides_for_the_caller_who_owns_the_document
func TestHTTPIfOwnerDecidesForTheCallerWhoOwnsTheDocument(t *testing.T) {
	w := newWF(t)
	mine := w.insert(w.sam, "mine")
	theirs := w.insert(w.clerk, "theirs")
	// Own Writer may read all, but write and submit only its own documents.
	wantStatus(t, w.take(w.sam, mine, "Send"), http.StatusOK)
	wantCode(t, w.take(w.sam, theirs, "Send"), http.StatusForbidden, "permission_denied")
}

// Rust: workflow.rs::plain_submit_and_cancel_are_workflow_required_and_edit_roles_apply_to_updates
func TestHTTPPlainSubmitAndCancelAreWorkflowRequiredAndEditRolesApplyToUpdates(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.clerk, "a")
	for _, step := range []string{"submit", "cancel"} {
		rep := w.e.send(post(wfURI(docName(doc), "/"+step)).token(w.admin).json(J{"version": 1}))
		wantCode(t, rep, http.StatusConflict, "workflow_required", step)
		if !strings.Contains(rep.errorObj()["message"].(string), "use a transition") {
			t.Fatal(rep.text)
		}
	}
	// A document of a DocType without a workflow is not affected.
	rep := w.e.send(post("/api/v1/resource/bill").token(w.admin).json(J{"title": "b"}))
	wantStatus(t, rep, http.StatusCreated)
	if _, ok := rep.dataObj()["workflow_state"]; ok {
		t.Fatal(rep.text)
	}
	rep = w.e.send(post("/api/v1/resource/bill/" + str(rep.data(), "name") + "/submit").token(w.admin).json(J{"version": 1}))
	wantStatus(t, rep, http.StatusOK, rep.text)

	// In Pending only a Manager may update: 403 for a Clerk with the write right.
	sent := w.take(w.clerk, doc, "Send").dataObj()
	patch := func(token string, version int) *req {
		return reqOf(http.MethodPatch, wfURI(docName(sent), "")).token(token).json(J{"version": version, "data": J{"amount": 5}})
	}
	wantCode(t, w.e.send(patch(w.clerk, 2)), http.StatusForbidden, "permission_denied")
	rep = w.e.send(patch(w.boss, 2))
	wantStatus(t, rep, http.StatusOK, rep.text)
	eqJSON(t, rep.dataObj()["workflow_state"], `"Pending"`)
}

// Rust: workflow.rs::a_client_cannot_set_the_workflow_state
func TestHTTPAClientCannotSetTheWorkflowState(t *testing.T) {
	w := newWF(t)
	rep := w.e.send(post("/api/v1/resource/purchase").token(w.admin).json(J{"title": "a", "workflow_state": "Approved"}))
	wantCode(t, rep, http.StatusUnprocessableEntity, "validation_failed", rep.text)
	doc := w.insert(w.admin, "a")
	rep = w.e.send(reqOf(http.MethodPatch, wfURI(docName(doc), "")).token(w.admin).json(J{"version": 1, "data": J{"workflow_state": "Approved"}}))
	wantCode(t, rep, http.StatusUnprocessableEntity, "validation_failed", rep.text)
}

// Rust: workflow.rs::the_list_shows_the_state_and_can_filter_and_sort_by_it
func TestHTTPTheListShowsTheStateAndCanFilterAndSortByIt(t *testing.T) {
	w := newWF(t)
	a := w.insert(w.admin, "a")
	w.insert(w.admin, "b")
	w.walk(a, "Send")
	filters := `[{"field":"workflow_state","op":"=","value":"Pending"}]`
	rep := w.e.send(get("/api/v1/resource/purchase?filters=" + enc(filters) + "&sort=workflow_state:desc").token(w.admin))
	wantStatus(t, rep, http.StatusOK, rep.text)
	items := rep.dataList()
	if len(items) != 1 || str(items[0], "workflow_state") != "Pending" {
		t.Fatal(rep.text)
	}
	// A DocType without a workflow has no such column.
	wantCode(t, w.e.send(get("/api/v1/resource/bill?filters="+enc(filters)).token(w.admin)), http.StatusBadRequest, "invalid_query")
}

func actionsOf(t *testing.T, rep reply) []string {
	t.Helper()
	wantStatus(t, rep, http.StatusOK, rep.text)
	out := []string{}
	for _, tr := range at(rep.data(), "transitions").([]any) {
		out = append(out, str(tr, "action"))
	}
	return out
}

// Rust: workflow.rs::transitions_lists_what_the_caller_may_take_now
func TestHTTPTransitionsListsWhatTheCallerMayTakeNow(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.sam, "a")
	// Draft: Send for Clerk and Own Writer (the owner); nothing for Manager (not a role of Send).
	rep := w.get(w.clerk, "/transitions", docName(doc))
	wantStatus(t, rep, http.StatusOK, rep.text)
	eqJSON(t, rep.data(), `{"state":"Draft","transitions":[{"action":"Send","to":"Pending"}]}`)
	strsEq(t, actionsOf(t, w.get(w.sam, "/transitions", docName(doc))), "Send")
	// Administrator may take everything from the state, a Manager has no role of Send.
	strsEq(t, actionsOf(t, w.get(w.admin, "/transitions", docName(doc))), "Send")
	strsEq(t, actionsOf(t, w.get(w.boss, "/transitions", docName(doc))))

	// Pending: Reject (Manager) and Approve (Manager, Reviewer, Own Writer).
	sent := w.take(w.clerk, doc, "Send")
	wantStatus(t, sent, http.StatusOK, sent.text)
	strsEq(t, actionsOf(t, w.get(w.boss, "/transitions", docName(doc))), "Reject", "Approve")
	// The role without the right (Reviewer cannot submit), the right without the role (Auditor), and Own
	// Writer on a document it owns: Approve.
	strsEq(t, actionsOf(t, w.get(w.rita, "/transitions", docName(doc))))
	strsEq(t, actionsOf(t, w.get(w.ada, "/transitions", docName(doc))))
	strsEq(t, actionsOf(t, w.get(w.sam, "/transitions", docName(doc))), "Approve")
	// Somebody else's document: Own Writer reads it but may not submit it.
	other := w.insert(w.clerk, "b")
	wantStatus(t, w.take(w.clerk, other, "Send"), http.StatusOK)
	strsEq(t, actionsOf(t, w.get(w.sam, "/transitions", docName(other))))
	// The state is in the answer, and the list follows the state.
	approved := w.take(w.boss, sent.dataObj(), "Approve").dataObj()
	rep = w.get(w.boss, "/transitions", docName(approved))
	eqJSON(t, rep.data(), `{"state":"Approved","transitions":[{"action":"Cancel","to":"Cancelled"}]}`)

	// No read right: 403. A missing document: 404. A DocType without a workflow: 409.
	wantCode(t, w.get(w.nobody, "/transitions", docName(doc)), http.StatusForbidden, "permission_denied")
	wantCode(t, w.get(w.boss, "/transitions", "missing"), http.StatusNotFound, "not_found")
	wantCode(t, w.e.send(get("/api/v1/resource/bill/x/transitions").token(w.admin)), http.StatusConflict, "no_workflow")
	// Query parameters are refused like on every other route.
	wantStatus(t, w.e.send(get(wfURI(docName(doc), "/transitions")+"?x=1").token(w.boss)), http.StatusBadRequest)
	// GET only.
	wantStatus(t, w.e.send(post(wfURI(docName(doc), "/transitions")).token(w.boss).json(J{})), http.StatusMethodNotAllowed)
}

func strsEq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Rust: workflow.rs::history_is_oldest_first_and_needs_read_access
func TestHTTPHistoryIsOldestFirstAndNeedsReadAccess(t *testing.T) {
	w := newWF(t)
	doc := w.insert(w.clerk, "a")
	sent := w.take(w.clerk, doc, "Send").dataObj()
	rejected := w.take(w.boss, sent, "Reject").dataObj()
	w.take(w.clerk, rejected, "Send")

	rep := w.get(w.boss, "/history", docName(doc))
	wantStatus(t, rep, http.StatusOK, rep.text)
	history := at(rep.data(), "history").([]any)
	var rows []string
	for _, e := range history {
		rows = append(rows, strings.Join([]string{str(e, "action"), str(e, "from"), str(e, "to"), str(e, "actor")}, ","))
		m := e.(map[string]any)
		if len(m) != 5 {
			t.Fatalf("keys of %v", m)
		}
		if when := str(e, "at"); !strings.HasSuffix(when, "Z") || !strings.Contains(when, "T") {
			t.Fatalf("RFC 3339 in UTC: %s", when)
		}
	}
	strsEq(t, rows, "Send,Draft,Pending,clerk", "Reject,Pending,Draft,boss", "Send,Draft,Pending,clerk")
	// The same read rule as get: no read right 403, a missing document 404, no workflow 409.
	wantCode(t, w.get(w.nobody, "/history", docName(doc)), http.StatusForbidden, "permission_denied")
	wantCode(t, w.get(w.boss, "/history", "missing"), http.StatusNotFound, "not_found")
	wantCode(t, w.e.send(get("/api/v1/resource/bill/x/history").token(w.admin)), http.StatusConflict, "no_workflow")
	// A document with no transitions has an empty history.
	fresh := w.insert(w.clerk, "b")
	eqJSON(t, w.get(w.boss, "/history", docName(fresh)).data(), `{"history":[]}`)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
