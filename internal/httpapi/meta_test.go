package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Ported from crates/forgebase-http/tests/meta.rs and the unit tests of src/meta.rs: the metadata
// routes and the session route of the desk (M4.1), against a real PostgreSQL.

func metaEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	for _, u := range []struct {
		name  string
		roles []string
	}{
		{"root", []string{"Administrator"}}, {"reader", []string{"Reader"}}, {"clerk", []string{"Clerk"}},
		{"biller", []string{"Biller"}}, {"viewer", []string{"Viewer"}}, {"own", []string{"Own"}},
		{"dropper", []string{"Dropper"}}, {"nobody", nil},
	} {
		e.user(u.name, u.roles...)
	}
	return e
}

func names(data []any) []string {
	out := []string{}
	for _, d := range data {
		out = append(out, str(d, "name"))
	}
	return out
}

func entry(t *testing.T, data []any, name string) any {
	t.Helper()
	for _, d := range data {
		if str(d, "name") == name {
			return d
		}
	}
	t.Fatalf("%s not listed", name)
	return nil
}

func sameList(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Rust: meta.rs::the_meta_list_has_the_readable_doctypes_only_sorted_by_name
func TestTheMetaListHasTheReadableDoctypesOnlySortedByName(t *testing.T) {
	e := metaEnv(t)
	for _, c := range []struct {
		user string
		want []string
	}{
		// Administrator reads everything that is not a child DocType.
		{"root", []string{"bill", "dropbox", "holder", "item", "ledger", "locked", "note", "secret", "vault"}},
		{"reader", []string{"holder", "item", "note"}},
		{"clerk", []string{"holder", "item"}},
		{"biller", []string{"bill"}},
		// Dropper may write and create dropbox but not read it.
		{"dropper", []string{}},
		{"own", []string{"note"}},
		{"nobody", []string{}},
	} {
		s := e.login(c.user)
		rep := e.send(get("/api/v1/meta").session(s))
		wantStatus(t, rep, http.StatusOK, c.user)
		sameList(t, names(rep.dataList()), c.want...)
		// The shape, and nothing about the permission rules or the roles.
		for _, item := range rep.dataList() {
			var keys []string
			for k := range item.(map[string]any) {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			sameList(t, keys, "app", "icon", "label", "name", "rights", "submittable")
		}
		if c.user == "root" {
			for _, role := range []string{"Reader", "Clerk", "Writer", "Biller", "Own"} {
				if strings.Contains(rep.text, `"`+role+`"`) {
					t.Error(rep.text)
				}
			}
		}
		if rep.header.Get("Cache-Control") != "no-store" {
			t.Error(rep.header)
		}
	}
}

// Rust: meta.rs::rights_are_all_own_or_none_from_the_one_evaluator
func TestRightsAreAllOwnOrNoneFromTheOneEvaluator(t *testing.T) {
	e := metaEnv(t)
	list := func(user string) []any {
		return e.send(get("/api/v1/meta").session(e.login(user))).dataList()
	}
	rights := func(data []any, name string) any { return at(entry(t, data, name), "rights") }

	// Administrator: everything, but the lifecycle rights only where the DocType has one.
	data := list("root")
	eqJSON(t, rights(data, "bill"), `{"read":"all","write":"all","create":"all","delete":"all",
		"submit":"all","cancel":"all","amend":"all","export":"all","import":"all"}`)
	eqJSON(t, rights(data, "note"), `{"read":"all","write":"all","create":"all","delete":"all",
		"submit":"none","cancel":"none","amend":"none","export":"all","import":"all"}`)
	eqJSON(t, at(entry(t, data, "bill"), "submittable"), `true`)
	eqJSON(t, at(entry(t, data, "bill"), "label"), `"Bill"`)
	eqJSON(t, at(entry(t, data, "note"), "submittable"), `false`)

	// if_owner rules are "own"; a right the role lacks is "none".
	eqJSON(t, rights(list("own"), "note"), `{"read":"own","write":"own","create":"own","delete":"own",
		"submit":"none","cancel":"none","amend":"none","export":"none","import":"none"}`)
	eqJSON(t, rights(list("clerk"), "item"), `{"read":"all","write":"none","create":"all","delete":"all",
		"submit":"none","cancel":"none","amend":"none","export":"none","import":"none"}`)
	eqJSON(t, at(rights(list("reader"), "item"), "create"), `"none"`)
	// A lifecycle right is granted per role on a submittable DocType.
	eqJSON(t, rights(list("viewer"), "bill"), `{"read":"all","write":"none","create":"none","delete":"none",
		"submit":"none","cancel":"none","amend":"none","export":"none","import":"none"}`)
}

// Rust: meta.rs::the_meta_of_one_doctype_describes_its_fields_and_child_tables
func TestTheMetaOfOneDoctypeDescribesItsFieldsAndChildTables(t *testing.T) {
	e := metaEnv(t)
	reader := e.login("reader")
	rep := e.send(get("/api/v1/meta/note").session(reader))
	wantStatus(t, rep, http.StatusOK)
	if rep.header.Get("Cache-Control") != "no-store" {
		t.Error(rep.header)
	}
	data := rep.dataObj()
	eqJSON(t, data["name"], `"note"`)
	eqJSON(t, data["label"], `"Note"`)
	eqJSON(t, data["naming"], `"random"`)
	eqJSON(t, data["submittable"], `false`)
	eqJSON(t, at(data, "rights", "read"), `"all"`)
	eqJSON(t, at(data, "rights", "write"), `"none"`)
	eqJSON(t, data["fields"], `[
		{"name":"title","label":"Title","type":"data","required":true,"unique":false,"allow_on_submit":false,"read_only":false,"max_length":140},
		{"name":"body","label":"Body","type":"text","required":false,"unique":false,"allow_on_submit":false,"read_only":false},
		{"name":"points","label":"Points","type":"int","required":false,"unique":false,"allow_on_submit":false,"read_only":false},
		{"name":"lines","label":"Lines","type":"table","required":false,"unique":false,"allow_on_submit":false,"read_only":false,
		 "target":"note_line",
		 "fields":[{"name":"text","label":"Text","type":"data","required":false,"unique":false,"allow_on_submit":false,
		            "read_only":false,"max_length":140}]}
	]`)
	// No title_field and list_fields in the DocType: null.
	eqJSON(t, data["title_field"], `null`)
	eqJSON(t, data["list_fields"], `null`)
	eqJSON(t, data["workflow"], `null`)
	// Metadata only: no rules, no roles, no documents.
	if strings.Contains(rep.text, "permissions") || strings.Contains(rep.text, "Writer") {
		t.Error(rep.text)
	}

	clerk := e.login("clerk")
	eqJSON(t, e.send(get("/api/v1/meta/item").session(clerk)).dataObj()["naming"], `"field:code"`)
	link := e.send(get("/api/v1/meta/holder").session(clerk)).dataObj()["fields"].([]any)[1]
	eqJSON(t, at(link, "name"), `"item"`)
	eqJSON(t, at(link, "type"), `"link"`)
	eqJSON(t, at(link, "target"), `"item"`)
}

// Rust: meta.rs::the_meta_of_a_doctype_follows_the_same_read_rule_as_the_resource_routes
func TestTheMetaOfADoctypeFollowsTheSameReadRuleAsTheResourceRoutes(t *testing.T) {
	e := metaEnv(t)
	reader := e.login("reader")
	// Unknown and child DocTypes: 404, also for a hostile name.
	for _, path := range []string{"/api/v1/meta/nope", "/api/v1/meta/note_line", "/api/v1/meta/..%2fnote", "/api/v1/meta/note%27%3B--"} {
		wantCode(t, e.send(get(path).session(reader)), http.StatusNotFound, "not_found", path)
	}
	// A DocType the caller cannot read: 403, DocType level, like GET /resource.
	for _, c := range [][2]string{{"reader", "secret"}, {"reader", "vault"}, {"dropper", "dropbox"}, {"nobody", "note"}, {"biller", "note"}} {
		s := e.login(c[0])
		rep := e.send(get("/api/v1/meta/" + c[1]).session(s))
		wantCode(t, rep, http.StatusForbidden, "permission_denied", c)
		wantStatus(t, e.send(get("/api/v1/resource/"+c[1]).session(s)), http.StatusForbidden)
		if strings.Contains(rep.text, "title") {
			t.Error(rep.text)
		}
	}
	// Administrator reads a DocType that has no rules.
	root := e.login("root")
	rep := e.send(get("/api/v1/meta/secret").session(root))
	wantStatus(t, rep, http.StatusOK)
	eqJSON(t, at(rep.dataObj()["fields"].([]any)[0], "name"), `"x"`)
}

// Rust: meta.rs::meta_never_contains_data
func TestMetaNeverContainsData(t *testing.T) {
	e := metaEnv(t)
	root := e.login("root")
	e.adminInsert(root, "note", J{"title": "Quarterly plan", "points": 7})
	reader := e.login("reader")
	for _, path := range []string{"/api/v1/meta", "/api/v1/meta/note"} {
		rep := e.send(get(path).session(reader))
		wantStatus(t, rep, http.StatusOK)
		if strings.Contains(rep.text, "Quarterly") {
			t.Error(path, rep.text)
		}
	}
}

// Rust: meta.rs::meta_needs_authentication_and_a_valid_host_and_takes_get_only
func TestMetaNeedsAuthenticationAndAValidHostAndTakesGetOnly(t *testing.T) {
	e := metaEnv(t)
	root := e.login("root")
	for _, path := range []string{"/api/v1/meta", "/api/v1/meta/note", "/api/v1/session"} {
		wantCode(t, e.send(get(path)), http.StatusUnauthorized, "unauthenticated", path)
		wantStatus(t, e.send(get(path).cookieValue("garbage")), http.StatusUnauthorized, path)
		for _, host := range []*string{nil, ptr("evil.example")} {
			r := get(path).session(root)
			if host == nil {
				r.noHost()
			} else {
				r.onHost(*host)
			}
			wantCode(t, e.send(r), http.StatusBadRequest, "invalid_host", path)
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			rep := e.send(reqOf(method, path).session(root))
			wantStatus(t, rep, http.StatusMethodNotAllowed, path)
			if rep.header.Get("Allow") != "GET,HEAD" {
				t.Errorf("Allow %q", rep.header.Get("Allow"))
			}
		}
	}
	// An API token works for the meta routes (GET: no CSRF).
	rep := e.send(get("/api/v1/meta").token(e.apiToken("reader")))
	wantStatus(t, rep, http.StatusOK)
	sameList(t, names(rep.dataList()), "holder", "item", "note")
}

// refresh is GET /session as the desk sends it: with the header that marks a deliberate refresh.
func refresh(uri string) *req { return get(uri).header("X-Forgebase-CSRF", "refresh") }

// Rust: meta.rs::the_session_route_needs_the_refresh_header_and_does_not_rotate_without_it
func TestTheSessionRouteNeedsTheRefreshHeaderAndDoesNotRotateWithoutIt(t *testing.T) {
	e := metaEnv(t)
	login := e.login("clerk")
	// Without the header, or with another value: 400 and nothing is rotated.
	for _, r := range []*req{
		get("/api/v1/session"),
		get("/api/v1/session").header("X-Forgebase-CSRF", ""),
		get("/api/v1/session").header("X-Forgebase-CSRF", "Refresh"),
		get("/api/v1/session").header("X-Forgebase-CSRF", login.csrf),
		// A repeated header is refused too, even when every value is the right one.
		get("/api/v1/session").header("X-Forgebase-CSRF", "refresh").header("X-Forgebase-CSRF", "refresh"),
	} {
		rep := e.send(r.cookie(login))
		wantCode(t, rep, http.StatusBadRequest, "bad_request")
		if strings.Contains(rep.text, "csrf_token") {
			t.Error(rep.text)
		}
	}
	// The old token still works.
	wantStatus(t, e.send(post("/api/v1/resource/item").session(login).json(J{"code": "R-1"})), http.StatusCreated)
	// With the header it rotates, and then the old token stops working.
	wantStatus(t, e.send(refresh("/api/v1/session").cookie(login)), http.StatusOK)
	stale := e.send(post("/api/v1/resource/item").session(login).json(J{"code": "R-2"}))
	if stale.code() != "csrf_failed" {
		t.Error(stale.text)
	}
	// Not logged in: still the uniform 401, header or not.
	wantStatus(t, e.send(get("/api/v1/session")), http.StatusUnauthorized)
}

// Rust: meta.rs::the_session_route_rotates_the_csrf_token
func TestTheSessionRouteRotatesTheCSRFToken(t *testing.T) {
	e := metaEnv(t)
	login := e.login("clerk")
	rep := e.send(refresh("/api/v1/session").cookie(login))
	wantStatus(t, rep, http.StatusOK)
	if rep.header.Get("Cache-Control") != "no-store" || len(rep.setCookies()) != 0 {
		t.Error(rep.header)
	}
	data := rep.dataObj()
	eqJSON(t, data["username"], `"clerk"`)
	eqJSON(t, data["roles"], `["Clerk"]`)
	fresh := str(data, "csrf_token")
	if len(fresh) != 64 || fresh == login.csrf || len(data) != 3 || strings.Contains(rep.text, login.token) {
		t.Error(rep.text)
	}
	// The old token is refused, the new one is accepted.
	body := J{"code": "A-1"}
	old := e.send(post("/api/v1/resource/item").session(login).json(body))
	wantCode(t, old, http.StatusForbidden, "csrf_failed")
	wantStatus(t, e.send(post("/api/v1/resource/item").cookie(login).csrf(fresh).json(body)), http.StatusCreated)
	// Another call replaces it again; a GET with the old token is fine (GET needs none).
	second := str(e.send(refresh("/api/v1/session").cookie(login)).data(), "csrf_token")
	if second == fresh {
		t.Error("the token did not change")
	}
	stale := e.send(post("/api/v1/resource/item").cookie(login).csrf(fresh).json(J{"code": "A-2"}))
	if stale.code() != "csrf_failed" {
		t.Error(stale.text)
	}
	// Only a digest is stored.
	rows := e.db.Rows("SELECT count(*)::text FROM fb_session WHERE csrf_hash = sha256(convert_to('" + second + "'::text, 'UTF8'))")
	if rows[0][0] != "1" {
		t.Error(rows)
	}
}

// Rust: meta.rs::the_session_route_needs_a_valid_cookie_session
func TestTheSessionRouteNeedsAValidCookieSession(t *testing.T) {
	e := metaEnv(t)
	// An API token has no session.
	rep := e.send(refresh("/api/v1/session").token(e.apiToken("clerk")))
	wantCode(t, rep, http.StatusBadRequest, "bad_request")
	if strings.Contains(rep.text, "csrf_token") {
		t.Error(rep.text)
	}
	// Expired (absolute and idle) and logged-out sessions are the uniform 401, and nothing is rotated
	// for them.
	absolute := e.login("clerk")
	e.db.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second'")
	wantCode(t, e.send(refresh("/api/v1/session").cookie(absolute)), http.StatusUnauthorized, "unauthenticated")
	idle := e.login("reader")
	e.db.Exec("UPDATE fb_session SET last_seen_at = now() - interval '25 hours'")
	wantStatus(t, e.send(refresh("/api/v1/session").cookie(idle)), http.StatusUnauthorized)
	gone := e.login("biller")
	wantStatus(t, e.send(post("/api/v1/logout").session(gone)), http.StatusNoContent)
	wantStatus(t, e.send(refresh("/api/v1/session").cookie(gone)), http.StatusUnauthorized)
}

// Rust: meta.rs::the_meta_shows_read_only_fields_title_field_and_list_fields
func TestTheMetaShowsReadOnlyFieldsTitleFieldAndListFields(t *testing.T) {
	e := metaEnv(t)
	root := e.login("root")
	data := e.send(get("/api/v1/meta/ledger").session(root)).dataObj()
	eqJSON(t, data["label"], `"Ledger"`)
	eqJSON(t, data["title_field"], `"title"`)
	eqJSON(t, data["list_fields"], `["title","total"]`)
	readOnly := func(fields []any) string {
		var out []string
		for _, f := range fields {
			out = append(out, str(f, "name")+"="+map[bool]string{true: "true", false: "false"}[at(f, "read_only") == true])
		}
		return strings.Join(out, ",")
	}
	fields := data["fields"].([]any)
	if got := readOnly(fields); got != "title=false,total=true,lines=false" {
		t.Error(got)
	}
	// The inlined child fields carry the flag too, and a humanized label.
	child := at(fields[2], "fields").([]any)
	if got := readOnly(child); got != "qty=false,amount=true" {
		t.Error(got)
	}
	eqJSON(t, at(child[1], "label"), `"Amount"`)
}

// Rust: meta.rs::rights_of_a_restricted_user_say_none_for_create_and_amend
//
// A user restricted on a DocType itself (user permissions, ADR 0025) cannot create or amend documents
// of it, so the metadata says none for those two and is unchanged for the rest; a DocType they are not
// restricted on, and a user without rows, are unchanged.
func TestRightsOfARestrictedUserSayNoneForCreateAndAmend(t *testing.T) {
	e := metaEnv(t)
	root := e.login("root")
	name := e.adminInsert(root, "bill", J{"title": "b1"})
	e.user("limited", "Biller")
	e.db.Exec("INSERT INTO fb_user_permission (user_id, doctype, value) SELECT id, 'bill', '" + name + "' FROM fb_user WHERE username = 'limited'")
	rights := func(data []any, doctype string) any { return at(entry(t, data, doctype), "rights") }

	limited := e.login("limited")
	data := e.send(get("/api/v1/meta").session(limited)).dataList()
	eqJSON(t, rights(data, "bill"), `{"read":"all","write":"all","create":"none","delete":"all",
		"submit":"all","cancel":"all","amend":"none","export":"none","import":"none"}`)
	// The single-DocType route says the same.
	one := e.send(get("/api/v1/meta/bill").session(limited)).dataObj()
	eqJSON(t, at(one, "rights", "create"), `"none"`)
	eqJSON(t, at(one, "rights", "amend"), `"none"`)
	eqJSON(t, at(one, "rights", "write"), `"all"`)

	// Unchanged for a user without rows, and for the Administrator whatever rows exist.
	biller := e.login("biller")
	data = e.send(get("/api/v1/meta").session(biller)).dataList()
	eqJSON(t, at(rights(data, "bill"), "create"), `"all"`)
	eqJSON(t, at(rights(data, "bill"), "amend"), `"all"`)
	e.db.Exec("INSERT INTO fb_user_permission (user_id, doctype, value) SELECT id, 'bill', 'x' FROM fb_user WHERE username = 'root'")
	root = e.login("root")
	data = e.send(get("/api/v1/meta").session(root)).dataList()
	eqJSON(t, at(rights(data, "bill"), "create"), `"all"`)
	eqJSON(t, at(rights(data, "bill"), "amend"), `"all"`)
}

// Rust: workflow.rs::the_meta_has_the_workflow_without_any_role
func TestTheMetaHasTheWorkflowWithoutAnyRole(t *testing.T) {
	e := newEnvWith(t, DefaultConfig(), purchaseDoc)
	e.user("clerk", "Clerk")
	e.user("admin", "Administrator")
	rep := e.send(get("/api/v1/meta/purchase").token(e.apiToken("clerk")))
	wantStatus(t, rep, http.StatusOK)
	eqJSON(t, rep.dataObj()["workflow"], `{
		"states":[{"name":"Draft","docstatus":0,"style":"neutral"},{"name":"Pending","docstatus":0,"style":"warning"},
		          {"name":"Approved","docstatus":1,"style":"success"},{"name":"Cancelled","docstatus":2,"style":"danger"}],
		"transitions":[{"action":"Send","from":"Draft","to":"Pending"},{"action":"Reject","from":"Pending","to":"Draft"},
		               {"action":"Approve","from":"Pending","to":"Approved"},{"action":"Cancel","from":"Approved","to":"Cancelled"}]}`)
	// Neither the roles of the transitions nor the edit roles nor the rules are sent.
	for _, role := range []string{"Manager", "Reviewer", "Own Writer", "Auditor", "edit_roles", "roles", "permissions"} {
		if strings.Contains(rep.text, role) {
			t.Errorf("%s in %s", role, rep.text)
		}
	}
	// A DocType without a workflow has "workflow": null, and nothing else is new (UX.4 added icon).
	rep = e.send(get("/api/v1/meta/bill").token(e.apiToken("admin")))
	data := rep.dataObj()
	eqJSON(t, data["workflow"], `null`)
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sameList(t, keys, "app", "fields", "icon", "label", "list_fields", "name", "naming", "rights", "submittable", "title_field", "workflow")
}

// Rust: meta.rs::tests::naming_kinds
func TestNamingKinds(t *testing.T) {
	d, err := core.ParseDocType(`
name = "invoice"
label = "Invoice"
naming = { series = "INV-.YYYY.-.#####" }
submittable = true
[[fields]]
name = "amount"
label = "Amount"
type = "decimal"
precision = 12
scale = 2
[[fields]]
name = "state"
type = "select"
options = ["open", "paid"]
allow_on_submit = true
[[fields]]
name = "customer"
type = "link"
target = "customer"
required = true
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		n    core.Naming
		want string
	}{{d.Naming, "series"}, {core.Naming{}, "random"}, {core.Naming{Kind: core.NamingField, Field: "code"}, "field:code"}} {
		if got := namingName(c.n); got != c.want {
			t.Errorf("%v: %q", c.n, got)
		}
	}
	// Rust: meta.rs::tests::field_parameters_by_type
	eqJSON(t, field(&d.Fields[0]), `{"name":"amount","label":"Amount","type":"decimal","required":false,
		"unique":false,"allow_on_submit":false,"read_only":false,"precision":12,"scale":2}`)
	state := field(&d.Fields[1])
	eqJSON(t, state["options"], `["open","paid"]`)
	eqJSON(t, state["allow_on_submit"], `true`)
	customer := field(&d.Fields[2])
	eqJSON(t, customer["target"], `"customer"`)
	eqJSON(t, customer["required"], `true`)
}
