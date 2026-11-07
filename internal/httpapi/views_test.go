package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// Saved list views and the group-by summary over HTTP (M8.3): each route with its status codes,
// authentication, CSRF, the envelopes, the 404 for another user's private view and the read scope of the
// summary. Ported from crates/forgebase-http/tests/views.rs.

// viewDealDoc: Clerk reads and creates everything, Mine reads and creates only its own documents.
const viewDealDoc = `
name = "deal"
label = "Deal"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "stage"
type = "select"
options = ["new", "won", "lost"]
[[fields]]
name = "amount"
type = "decimal"
precision = 10
scale = 2
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "notes"
type = "text"
[[fields]]
name = "lines"
type = "table"
target = "deal_line"
[[permissions]]
role = "Clerk"
read = true
create = true
[[permissions]]
role = "Mine"
read = true
create = true
if_owner = true
`

const viewDealLineDoc = `
name = "deal_line"
label = "Deal Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

type viewSetupT struct {
	e              *env
	ann, bob, root session
}

func newViewSetup(t *testing.T) *viewSetupT {
	t.Helper()
	e := newEnvWith(t, DefaultConfig(), viewDealDoc, viewDealLineDoc)
	e.user("ann", "Clerk")
	e.user("bob", "Clerk")
	e.user("root", "Administrator")
	e.user("nobody")
	return &viewSetupT{e: e, ann: e.login("ann"), bob: e.login("bob"), root: e.login("root")}
}

func viewDefinitionJSON() J {
	return J{"filters": []any{J{"field": "stage", "op": "=", "value": "new"}}, "sort": []string{"title:desc"}, "columns": []string{"title", "stage"}}
}

func (s *viewSetupT) create(who session, label string, shared bool) reply {
	return s.e.send(post("/api/v1/views/deal").session(who).json(J{"label": label, "definition": viewDefinitionJSON(), "shared": shared}))
}

func status(t *testing.T, r reply, status int, code string) {
	t.Helper()
	wantStatus(t, r, status, r.text)
	if code != "" && r.code() != code {
		t.Fatalf("code %q, want %q: %s", r.code(), code, r.text)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Rust: views.rs::the_routes_create_list_update_and_delete_with_their_envelopes
func TestHTTPViewRoutesCreateListUpdateAndDeleteWithTheirEnvelopes(t *testing.T) {
	s := newViewSetup(t)
	created := s.create(s.ann, "Open deals", false)
	status(t, created, http.StatusCreated, "")
	view := created.dataObj()
	var keys []string
	for k := range view {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	strsEq(t, keys, "definition", "id", "label", "modified_at", "owner", "shared", "version")
	eqJSON(t, view["label"], `"Open deals"`)
	eqJSON(t, view["shared"], `false`)
	eqJSON(t, view["owner"], `"ann"`)
	eqJSON(t, view["version"], `1`)
	eqJSON(t, view["definition"], jsonOf(t, viewDefinitionJSON()))
	if !strings.HasSuffix(view["modified_at"].(string), "Z") {
		t.Fatal(view["modified_at"])
	}
	id := view["id"].(string)

	list := s.e.send(get("/api/v1/views/deal").session(s.ann))
	status(t, list, http.StatusOK, "")
	if len(list.obj().(map[string]any)) != 1 {
		t.Fatal(list.text)
	}
	eqJSON(t, list.data(), "["+jsonOf(t, view)+"]")

	// Only the label; then only the definition.
	url := "/api/v1/views/deal/" + id
	renamed := s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 1, "label": "Won"}))
	status(t, renamed, http.StatusOK, "")
	eqJSON(t, renamed.dataObj()["label"], `"Won"`)
	eqJSON(t, renamed.dataObj()["version"], `2`)
	eqJSON(t, renamed.dataObj()["definition"], jsonOf(t, viewDefinitionJSON()))
	empty := J{"filters": []any{}, "sort": []any{}, "columns": []any{}}
	redefined := s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 2, "definition": empty}))
	eqJSON(t, redefined.dataObj()["definition"], jsonOf(t, empty))
	// A partial definition is completed.
	partial := s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 3, "definition": J{"columns": []string{"title"}}}))
	eqJSON(t, partial.dataObj()["definition"], `{"filters":[],"sort":[],"columns":["title"]}`)

	// A stale version, and a delete with a stale version.
	status(t, s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 1, "label": "x"})), http.StatusConflict, "conflict")
	status(t, s.e.send(reqOf(http.MethodDelete, url+"?version=1").session(s.ann)), http.StatusConflict, "conflict")
	deleted := s.e.send(reqOf(http.MethodDelete, url+"?version=4").session(s.ann))
	if deleted.status != http.StatusNoContent || deleted.text != "" {
		t.Fatal(deleted.status, deleted.text)
	}
	status(t, s.e.send(reqOf(http.MethodDelete, url+"?version=4").session(s.ann)), http.StatusNotFound, "not_found")
	eqJSON(t, s.e.send(get("/api/v1/views/deal").session(s.ann)).data(), `[]`)
}

// Rust: views.rs::authentication_and_csrf_are_needed_like_on_the_resource_routes
func TestHTTPViewAuthenticationAndCsrfAreNeededLikeOnTheResourceRoutes(t *testing.T) {
	s := newViewSetup(t)
	id := s.create(s.ann, "Mine", false).dataObj()["id"].(string)
	url := "/api/v1/views/deal/" + id
	body := J{"label": "x", "definition": viewDefinitionJSON()}
	// 401 without a credential.
	for _, r := range []*req{
		get("/api/v1/views/deal"),
		post("/api/v1/views/deal").json(body),
		reqOf(http.MethodPatch, url).json(J{"version": 1, "label": "x"}),
		reqOf(http.MethodDelete, url+"?version=1"),
		get("/api/v1/summary/deal?group_by=stage"),
	} {
		status(t, s.e.send(r), http.StatusUnauthorized, "unauthenticated")
	}
	// 403 without the CSRF token on the writes; a read needs none.
	for _, r := range []*req{
		post("/api/v1/views/deal").cookie(s.ann).json(body),
		reqOf(http.MethodPatch, url).cookie(s.ann).json(J{"version": 1, "label": "x"}),
		reqOf(http.MethodDelete, url+"?version=1").cookie(s.ann),
	} {
		status(t, s.e.send(r), http.StatusForbidden, "csrf_failed")
	}
	status(t, s.e.send(get("/api/v1/views/deal").cookie(s.ann)), http.StatusOK, "")
	// An API token needs no CSRF header.
	token := s.e.apiToken("ann")
	status(t, s.e.send(get("/api/v1/views/deal").token(token)), http.StatusOK, "")
	status(t, s.e.send(reqOf(http.MethodDelete, url+"?version=1").token(token)), http.StatusNoContent, "")
}

// Rust: views.rs::another_users_private_view_is_not_found_not_forbidden
func TestHTTPAnotherUsersPrivateViewIsNotFoundNotForbidden(t *testing.T) {
	s := newViewSetup(t)
	id := s.create(s.ann, "Private", false).dataObj()["id"].(string)
	url := "/api/v1/views/deal/" + id
	const missingID = "00000000-0000-7000-8000-000000000000"
	for _, who := range []session{s.bob, s.root} {
		eqJSON(t, s.e.send(get("/api/v1/views/deal").session(who)).data(), `[]`)
		status(t, s.e.send(reqOf(http.MethodPatch, url).session(who).json(J{"version": 1, "label": "x"})), http.StatusNotFound, "not_found")
		del := s.e.send(reqOf(http.MethodDelete, url+"?version=1").session(who))
		status(t, del, http.StatusNotFound, "not_found")
		// The message of a hidden view is the one of a missing one.
		missing := s.e.send(reqOf(http.MethodDelete, "/api/v1/views/deal/"+missingID+"?version=1").session(who))
		text := func(r reply, id string) string { return strings.ReplaceAll(r.errorObj()["message"].(string), id, "ID") }
		if text(del, id) != text(missing, missingID) {
			t.Fatalf("%q vs %q", text(del, id), text(missing, missingID))
		}
	}
	if str(s.e.send(get("/api/v1/views/deal").session(s.ann)).dataList()[0], "label") != "Private" {
		t.Fatal("the view changed")
	}
	// A path that is no UUID names no view.
	status(t, s.e.send(reqOf(http.MethodDelete, "/api/v1/views/deal/nope?version=1").session(s.ann)), http.StatusNotFound, "not_found")
}

// Rust: views.rs::shared_views_are_for_administrators_to_create_and_everyone_to_see
func TestHTTPSharedViewsAreForAdministratorsToCreateAndEveryoneToSee(t *testing.T) {
	s := newViewSetup(t)
	status(t, s.create(s.ann, "Team", true), http.StatusForbidden, "permission_denied")
	shared := s.create(s.root, "Team", true)
	status(t, shared, http.StatusCreated, "")
	eqJSON(t, shared.dataObj()["shared"], `true`)
	eqJSON(t, shared.dataObj()["owner"], `null`)
	id := shared.dataObj()["id"].(string)
	s.create(s.bob, "B mine", false)
	var labels []string
	for _, v := range s.e.send(get("/api/v1/views/deal").session(s.bob)).dataList() {
		labels = append(labels, str(v, "label"))
	}
	strsEq(t, labels, "Team", "B mine")
	url := "/api/v1/views/deal/" + id
	status(t, s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 1, "label": "x"})), http.StatusForbidden, "permission_denied")
	status(t, s.e.send(reqOf(http.MethodDelete, url+"?version=1").session(s.ann)), http.StatusForbidden, "permission_denied")
	status(t, s.e.send(reqOf(http.MethodDelete, url+"?version=1").session(s.root)), http.StatusNoContent, "")
}

// Rust: views.rs::the_doctype_and_the_read_right_are_checked_first
func TestHTTPTheDoctypeAndTheReadRightAreCheckedFirst(t *testing.T) {
	s := newViewSetup(t)
	nobody := s.e.login("nobody")
	status(t, s.e.send(get("/api/v1/views/deal").session(nobody)), http.StatusForbidden, "permission_denied")
	// Before the body is looked at: a bad body gives a caller without the right nothing.
	status(t, s.e.send(post("/api/v1/views/deal").session(nobody).json(J{"nonsense": 1})), http.StatusForbidden, "permission_denied")
	for _, uri := range []string{
		"/api/v1/views/nope", "/api/v1/views/deal_line", "/api/v1/summary/nope?group_by=x", "/api/v1/summary/deal_line?group_by=qty",
	} {
		status(t, s.e.send(get(uri).session(s.ann)), http.StatusNotFound, "not_found")
	}
	status(t, s.e.send(post("/api/v1/views/nope").session(s.ann).json(J{})), http.StatusNotFound, "not_found")
	status(t, s.e.send(get("/api/v1/summary/deal?group_by=stage").session(nobody)), http.StatusForbidden, "permission_denied")
}

// Rust: views.rs::bad_bodies_and_definitions_are_refused
func TestHTTPBadBodiesAndDefinitionsAreRefused(t *testing.T) {
	s := newViewSetup(t)
	postBody := func(body any) reply { return s.e.send(post("/api/v1/views/deal").session(s.ann).json(body)) }
	with := func(definition any) J { return J{"label": "v", "definition": definition} }
	// 422: the label or the definition is not valid.
	for _, c := range []struct {
		body   any
		needle string
	}{
		{J{"label": "", "definition": viewDefinitionJSON()}, "label"},
		{J{"label": " x", "definition": viewDefinitionJSON()}, "whitespace"},
		{with(J{"filters": []any{J{"field": "nope", "op": "is null"}}}), "unknown field"},
		{with(J{"filters": []any{J{"field": "qty", "op": "like", "value": "1%"}}}), "text fields only"},
		{with(J{"filters": []any{J{"field": "qty", "op": "?", "value": 1}}}), "unknown operator"},
		{with(J{"filters": "x"}), "must be a list"},
		{with(J{"sort": []string{"nope"}}), "unknown field"},
		{with(J{"sort": []string{"title:sideways"}}), "invalid sort"},
		{with(J{"sort": []any{1}}), "list of strings"},
		{with(J{"columns": []string{"lines"}}), "table field"},
		{with(J{"columns": []string{"notes"}}), "text field"},
		{with(J{"columns": []string{"nope"}}), "unknown column"},
		{with(J{"columns": []string{"title", "title"}}), "twice"},
		{with(J{"columns": []string{"title", "stage", "amount", "qty", "name", "owner", "modified_by", "created_at", "modified_at"}}), "at most 8"},
		{with(J{"limit": 5}), "unknown key"},
		{with("x"), "must be an object"},
	} {
		rep := postBody(c.body)
		status(t, rep, http.StatusUnprocessableEntity, "validation_failed")
		if d := rep.details()[0]; !strings.Contains(d, c.needle) {
			t.Errorf("%v: %s", c.body, d)
		}
	}
	// A definition over 16 KiB.
	big := strings.Repeat("x", 17*1024)
	status(t, postBody(with(J{"filters": []any{J{"field": "title", "op": "=", "value": big}}})), http.StatusUnprocessableEntity, "validation_failed")
	// 400: the body is not what the route takes.
	for _, body := range []any{
		J{"definition": viewDefinitionJSON()}, J{"label": "v"}, J{"label": 5, "definition": viewDefinitionJSON()},
		J{"label": "v", "definition": viewDefinitionJSON(), "shared": "yes"}, J{"label": "v", "definition": viewDefinitionJSON(), "owner": "bob"},
		[]any{},
	} {
		status(t, postBody(body), http.StatusBadRequest, "bad_request")
	}
	view := s.create(s.ann, "ok", false).dataObj()
	url := "/api/v1/views/deal/" + view["id"].(string)
	for _, body := range []J{{"label": "x"}, {"version": "1", "label": "x"}, {"version": 1, "label": "x", "shared": true}, {"version": 1, "label": 7}} {
		status(t, s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(body)), http.StatusBadRequest, "bad_request")
	}
	// No change at all.
	status(t, s.e.send(reqOf(http.MethodPatch, url).session(s.ann).json(J{"version": 1})), http.StatusBadRequest, "bad_request")
	// Unknown and repeated parameters, and a missing or bad version.
	status(t, s.e.send(get("/api/v1/views/deal?x=1").session(s.ann)), http.StatusBadRequest, "invalid_query")
	for _, suffix := range []string{"", "?version=x", "?version=1&version=1", "?version=1&x=1"} {
		status(t, s.e.send(reqOf(http.MethodDelete, url+suffix).session(s.ann)), http.StatusBadRequest, "invalid_query")
	}
	// Not JSON.
	status(t, s.e.send(post("/api/v1/views/deal").session(s.ann).raw("application/json", []byte("{"))), http.StatusBadRequest, "bad_request")
	status(t, s.e.send(post("/api/v1/views/deal").session(s.ann).raw("text/plain", []byte("{}"))), http.StatusUnsupportedMediaType, "unsupported_media_type")
}

// Rust: views.rs::a_duplicate_label_is_a_conflict_and_a_stale_view_is_stored_as_written
func TestHTTPADuplicateLabelIsAConflictAndAStaleViewIsStoredAsWritten(t *testing.T) {
	s := newViewSetup(t)
	status(t, s.create(s.ann, "Same", false), http.StatusCreated, "")
	duplicate := s.create(s.ann, "Same", false)
	status(t, duplicate, http.StatusConflict, "conflict")
	if !strings.Contains(duplicate.errorObj()["message"].(string), `"Same"`) {
		t.Fatal(duplicate.text)
	}
	status(t, s.create(s.bob, "Same", false), http.StatusCreated, "")
	// A view that names a field the DocType no longer has is listed as stored.
	s.e.db.Exec(`INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition) VALUES
		(gen_random_uuid(), 'deal', 'Old', NULL,
		'{"filters":[{"field":"gone","op":"is null"}],"sort":[],"columns":["gone"]}')`)
	list := s.e.send(get("/api/v1/views/deal").session(s.ann)).dataList()
	if str(list[0], "label") != "Old" {
		t.Fatal(list)
	}
	eqJSON(t, at(list[0], "definition", "columns"), `["gone"]`)
	// Applying it is the list's business: invalid_query.
	applied := s.e.send(get("/api/v1/resource/deal?filters=%5B%7B%22field%22%3A%22gone%22%2C%22op%22%3A%22is%20null%22%7D%5D").session(s.ann))
	status(t, applied, http.StatusBadRequest, "invalid_query")
}

func (s *viewSetupT) addDeals(t *testing.T) {
	for _, d := range []struct {
		who                  session
		title, stage, amount string
		qty                  int
	}{
		{s.ann, "a", "new", "10.00", 1}, {s.ann, "b", "new", "20.50", 2}, {s.ann, "c", "won", "100.00", 5},
		{s.bob, "d", "lost", "7.00", 3}, {s.bob, "e", "new", "1.00", 4},
	} {
		status(t, s.e.send(post("/api/v1/resource/deal").session(d.who).json(J{"title": d.title, "stage": d.stage, "amount": d.amount, "qty": d.qty})),
			http.StatusCreated, "")
	}
}

// Rust: views.rs::the_summary_groups_with_aggregates_and_the_envelope
func TestHTTPTheSummaryGroupsWithAggregatesAndTheEnvelope(t *testing.T) {
	s := newViewSetup(t)
	s.addDeals(t)
	rep := s.e.send(get("/api/v1/summary/deal?group_by=stage&aggregates=sum:amount,avg:amount,sum:qty,max:qty").session(s.ann))
	status(t, rep, http.StatusOK, "")
	if len(rep.obj().(map[string]any)) != 1 {
		t.Fatal(rep.text)
	}
	eqJSON(t, rep.data(), `{"groups":[
		{"key":"new","count":3,"values":{"sum_amount":"31.50","avg_amount":"10.5000","sum_qty":7,"max_qty":4}},
		{"key":"lost","count":1,"values":{"sum_amount":"7.00","avg_amount":"7.0000","sum_qty":3,"max_qty":3}},
		{"key":"won","count":1,"values":{"sum_amount":"100.00","avg_amount":"100.0000","sum_qty":5,"max_qty":5}}],
		"truncated":false}`)
	// Without aggregates, with a filter (the list's JSON form), by owner.
	filters := "%5B%7B%22field%22%3A%22stage%22%2C%22op%22%3A%22%3D%22%2C%22value%22%3A%22new%22%7D%5D"
	rep = s.e.send(get("/api/v1/summary/deal?group_by=owner&filters=" + filters).session(s.ann))
	eqJSON(t, rep.data(), `{"groups":[{"key":"ann","count":2,"values":{}},{"key":"bob","count":1,"values":{}}],"truncated":false}`)
}

// Rust: views.rs::the_summary_respects_if_owner
func TestHTTPTheSummaryRespectsIfOwner(t *testing.T) {
	s := newViewSetup(t)
	s.e.user("mine", "Mine")
	mine := s.e.login("mine")
	s.addDeals(t)
	for _, stage := range []string{"new", "new", "won"} {
		status(t, s.e.send(post("/api/v1/resource/deal").session(mine).json(J{"stage": stage, "qty": 1})), http.StatusCreated, "")
	}
	rep := s.e.send(get("/api/v1/summary/deal?group_by=stage&aggregates=sum:qty").session(mine))
	eqJSON(t, at(rep.data(), "groups"), `[{"key":"new","count":2,"values":{"sum_qty":2}},{"key":"won","count":1,"values":{"sum_qty":1}}]`)
	// The Clerk sees all eight.
	all := s.e.send(get("/api/v1/summary/deal?group_by=stage").session(s.ann))
	total := 0
	for _, g := range at(all.data(), "groups").([]any) {
		var n int
		_, _ = fmt.Sscan(fmt.Sprint(at(g, "count")), &n)
		total += n
	}
	if total != 8 {
		t.Fatal(total)
	}
}

// Rust: views.rs::bad_summary_queries_are_invalid_queries
func TestHTTPBadSummaryQueriesAreInvalidQueries(t *testing.T) {
	s := newViewSetup(t)
	for _, query := range []string{
		"", "group_by=", "group_by=nope", "group_by=notes", "group_by=amount", "group_by=lines", "group_by=name",
		"group_by=stage&aggregates=sum:title", "group_by=stage&aggregates=sum:nope", "group_by=stage&aggregates=count:qty",
		"group_by=stage&aggregates=sum", "group_by=stage&aggregates=sum:qty,sum:qty",
		"group_by=stage&aggregates=sum:qty,avg:qty,min:qty,max:qty,sum:amount,avg:amount,min:amount",
		"group_by=stage&filters=%5B%7B%22field%22%3A%22nope%22%2C%22op%22%3A%22is%20null%22%7D%5D",
		"group_by=stage&filters=%5B", "group_by=stage&limit=5", "group_by=stage&group_by=owner",
	} {
		rep := s.e.send(get("/api/v1/summary/deal?" + query).session(s.ann))
		wantCode(t, rep, http.StatusBadRequest, "invalid_query", query)
	}
	// The summary is a read: it takes no body method.
	status(t, s.e.send(post("/api/v1/summary/deal").session(s.ann).json(J{})), http.StatusMethodNotAllowed, "method_not_allowed")
}
