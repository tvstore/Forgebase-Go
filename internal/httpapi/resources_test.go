package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Ported from crates/forgebase-http/tests/resources.rs: the resource routes over HTTP: the
// authorization matrix, if_owner, the document lifecycle, decision 9 (no leaks through Links) and
// injection attempts, against a real PostgreSQL.

// resourceSetup is an environment with the users the tests need and the Administrator's session.
func resourceSetup(t *testing.T) (*env, session) {
	t.Helper()
	e := newEnv(t)
	e.user("admin", "Administrator")
	e.user("reader", "Reader")
	e.user("writer", "Writer")
	e.user("clerk", "Clerk")
	e.user("plain")
	return e, e.login("admin")
}

func uri(doctype, name string) string { return "/api/v1/resource/" + doctype + "/" + name }

// Rust: resources.rs::the_authorization_matrix_over_http
func TestTheAuthorizationMatrixOverHTTP(t *testing.T) {
	e, admin := resourceSetup(t)
	note := e.adminInsert(admin, "note", J{"title": "n"})
	reader, writer, plain := e.apiToken("reader"), e.apiToken("writer"), e.apiToken("plain")
	one := uri("note", note)

	// Reader: read yes, everything else 403.
	wantStatus(t, e.send(get("/api/v1/resource/note").token(reader)), http.StatusOK)
	wantStatus(t, e.send(get(one).token(reader)), http.StatusOK)
	for _, r := range []*req{
		post("/api/v1/resource/note").token(reader).json(J{"title": "x"}),
		reqOf(http.MethodPatch, one).token(reader).json(J{"version": 1, "data": J{"body": "x"}}),
		reqOf(http.MethodDelete, one+"?version=1").token(reader),
	} {
		wantCode(t, e.send(r), http.StatusForbidden, "permission_denied")
	}

	// Writer: create, change and delete.
	created := e.send(post("/api/v1/resource/note").token(writer).json(J{"title": "w"}))
	wantStatus(t, created, http.StatusCreated)
	mine := str(created.data(), "name")
	patched := e.send(reqOf(http.MethodPatch, uri("note", mine)).token(writer).json(J{"version": 1, "data": J{"body": "hello", "points": 3}}))
	wantStatus(t, patched, http.StatusOK)
	eqJSON(t, at(patched.data(), "version"), `2`)
	eqJSON(t, at(patched.data(), "body"), `"hello"`)
	wantStatus(t, e.send(reqOf(http.MethodDelete, uri("note", mine)+"?version=2").token(writer)), http.StatusNoContent)
	wantCode(t, e.send(get(uri("note", mine)).token(writer)), http.StatusNotFound, "not_found")

	// A user with no role at all, and a DocType without rules: 403 (Administrator excepted).
	wantCode(t, e.send(get("/api/v1/resource/note").token(plain)), http.StatusForbidden, "permission_denied")
	for _, token := range []string{reader, writer} {
		wantStatus(t, e.send(get("/api/v1/resource/secret").token(token)), http.StatusForbidden)
		wantStatus(t, e.send(post("/api/v1/resource/secret").token(token).json(J{"x": "y"})), http.StatusForbidden)
	}
	secret := e.adminInsert(admin, "secret", J{"x": "y"})
	wantStatus(t, e.send(get(uri("secret", secret)).session(admin)), http.StatusOK)
	// Without a read right a get is 403, not a 404 that would hint at the name.
	wantStatus(t, e.send(get(uri("secret", secret)).token(writer)), http.StatusForbidden)
	// A body or a filter that does not fit the DocType never gets a 422/400 that would describe a
	// DocType the caller may not use: the missing right is reported first.
	wantStatus(t, e.send(post("/api/v1/resource/secret").token(writer).json(J{"nope": 5, "x": 1})), http.StatusForbidden)
	// x is a Data field, so without the pre-check this would be a 422 naming it.
	rep := e.send(reqOf(http.MethodPatch, uri("secret", secret)).token(writer).json(J{"version": 1, "data": J{"x": 5}}))
	wantCode(t, rep, http.StatusForbidden, "permission_denied")
	// Write without Read is refused too (an update returns the document, so it needs Read): the
	// pre-check asks for both rights.
	e.user("dropper", "Dropper")
	dropper := e.apiToken("dropper")
	drop := e.adminInsert(admin, "dropbox", J{"x": "a"})
	rep = e.send(reqOf(http.MethodPatch, uri("dropbox", drop)).token(dropper).json(J{"version": 1, "data": J{"x": 5}}))
	wantCode(t, rep, http.StatusForbidden, "permission_denied")
	wantStatus(t, e.send(get("/api/v1/resource/secret?filters=%5B%7B%22field%22%3A%22nope%22%7D%5D").token(writer)), http.StatusForbidden)
}

// Rust: resources.rs::if_owner_hides_the_documents_of_others
func TestIfOwnerHidesTheDocumentsOfOthers(t *testing.T) {
	e, admin := resourceSetup(t)
	e.user("own1", "Own")
	e.user("own2", "Own")
	own1, own2 := e.apiToken("own1"), e.apiToken("own2")
	create := func(token, title string) string {
		rep := e.send(post("/api/v1/resource/note").token(token).json(J{"title": title}))
		wantStatus(t, rep, http.StatusCreated)
		return str(rep.data(), "name")
	}
	n1 := create(own1, "one")
	n2 := create(own2, "two")
	// Each sees only their own, in lists and by name.
	list := e.send(get("/api/v1/resource/note").token(own1))
	var names []any
	for _, d := range list.dataList() {
		names = append(names, at(d, "name"))
	}
	eqJSON(t, names, fmt.Sprintf("[%q]", n1))
	// Neither a filter nor a sort reveals the other's document.
	filtered := e.send(get("/api/v1/resource/note?filters=" +
		"%5B%7B%22field%22%3A%22title%22%2C%22op%22%3A%22%3D%22%2C%22value%22%3A%22two%22%7D%5D").token(own1))
	eqJSON(t, filtered.data(), `[]`)
	// 404 for get, patch and delete of another's document: the same as a missing one.
	missing, theirs := uri("note", "does-not-exist"), uri("note", n2)
	patch := J{"version": 1, "data": J{"body": "x"}}
	for _, c := range []*req{
		get(theirs), get(missing),
		reqOf(http.MethodPatch, theirs).json(patch), reqOf(http.MethodPatch, missing).json(patch),
		reqOf(http.MethodDelete, theirs+"?version=1"), reqOf(http.MethodDelete, missing+"?version=1"),
	} {
		rep := e.send(c.token(own1))
		wantCode(t, rep, http.StatusNotFound, "not_found", c.method, c.uri)
	}
	// The other's document is untouched; the admin sees both.
	if n := len(e.send(get("/api/v1/resource/note").session(admin)).dataList()); n != 2 {
		t.Errorf("%d documents", n)
	}
	eqJSON(t, at(e.send(get(theirs).token(own2)).data(), "version"), `1`)
}

// Rust: resources.rs::the_lifecycle_and_its_error_codes
func TestTheLifecycleAndItsErrorCodes(t *testing.T) {
	e, _ := resourceSetup(t)
	e.user("biller", "Biller")
	e.user("viewer", "Viewer")
	biller, viewer := e.apiToken("biller"), e.apiToken("viewer")
	created := e.send(post("/api/v1/resource/bill").token(biller).json(J{"title": "b"}))
	wantStatus(t, created, http.StatusCreated)
	name := str(created.data(), "name")
	one := uri("bill", name)
	eqJSON(t, at(created.data(), "docstatus"), `0`)

	// Validation: every problem, in details.
	invalid := e.send(post("/api/v1/resource/bill").token(biller).json(J{"nope": 1}))
	wantCode(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	if n := len(invalid.details()); n != 2 {
		t.Errorf("%d details: %s", n, invalid.text)
	}
	// Mapping errors name fields: a caller without the right gets the 403 instead.
	wantStatus(t, e.send(post("/api/v1/resource/note").token(biller).json(J{"title": 5})), http.StatusForbidden)

	// Version conflict.
	stale := e.send(reqOf(http.MethodPatch, one).token(biller).json(J{"version": 9, "data": J{"title": "x"}}))
	wantCode(t, stale, http.StatusConflict, "conflict")
	if !strings.Contains(str(stale.errorObj(), "message"), "current version 1") {
		t.Error(stale.text)
	}
	// Empty change.
	wantStatus(t, e.send(reqOf(http.MethodPatch, one).token(biller).json(J{"version": 1, "data": J{}})), http.StatusBadRequest)

	// A viewer cannot submit; the biller can.
	wantStatus(t, e.send(post(one+"/submit").token(viewer).json(J{"version": 1})), http.StatusForbidden)
	submitted := e.send(post(one + "/submit").token(biller).json(J{"version": 1}))
	wantStatus(t, submitted, http.StatusOK)
	eqJSON(t, at(submitted.data(), "docstatus"), `1`)
	// A submitted document: cannot be deleted, its fields are locked, it cannot be submitted again.
	for _, c := range []struct {
		rep  reply
		code string
	}{
		{e.send(reqOf(http.MethodDelete, one+"?version=2").token(biller)), "invalid_docstatus"},
		{e.send(reqOf(http.MethodPatch, one).token(biller).json(J{"version": 2, "data": J{"title": "x"}})), "field_locked"},
		{e.send(post(one + "/submit").token(biller).json(J{"version": 2})), "invalid_docstatus"},
	} {
		wantCode(t, c.rep, http.StatusConflict, c.code)
	}
	cancelled := e.send(post(one + "/cancel").token(biller).json(J{"version": 2}))
	wantStatus(t, cancelled, http.StatusOK)
	eqJSON(t, at(cancelled.data(), "docstatus"), `2`)
	amended := e.send(post(one + "/amend").token(biller).json(J{"version": 3}))
	wantStatus(t, amended, http.StatusCreated)
	eqJSON(t, at(amended.data(), "amended_from"), fmt.Sprintf("%q", name))
	eqJSON(t, at(amended.data(), "docstatus"), `0`)
	wantCode(t, e.send(post(one+"/amend").token(biller).json(J{"version": 3})), http.StatusConflict, "already_amended")

	// Not submittable.
	e.user("writer2", "Writer")
	writer := e.apiToken("writer2")
	note := e.send(post("/api/v1/resource/note").token(writer).json(J{"title": "n"}))
	noteName := str(note.data(), "name")
	// note is not submittable: that is checked before the missing right.
	rep := e.send(post(uri("note", noteName) + "/submit").token(writer).json(J{"version": 1}))
	wantCode(t, rep, http.StatusUnprocessableEntity, "not_submittable")
}

// Rust: resources.rs::bodies_and_parameters_are_checked
func TestBodiesAndParametersAreChecked(t *testing.T) {
	e, admin := resourceSetup(t)
	note := e.adminInsert(admin, "note", J{"title": "n"})
	one := uri("note", note)
	patch := func(body any) *req { return reqOf(http.MethodPatch, one).session(admin).json(body) }
	cases := []struct {
		r    *req
		code string
	}{
		// PATCH without version, with a string version, with unknown keys, without data.
		{patch(J{"data": J{"body": "x"}}), "bad_request"},
		{patch(J{"version": "1", "data": J{"body": "x"}}), "bad_request"},
		{patch(J{"version": 1, "data": J{"body": "x"}, "x": 1}), "bad_request"},
		{patch(J{"version": 1}), "bad_request"},
		{patch(J{"version": 1, "data": []any{}}), "bad_request"},
		{post(one + "/submit").session(admin).json(J{}), "bad_request"},
		{post("/api/v1/resource/note").session(admin).json([]any{1}), "bad_request"},
		// A value that does not fit its field.
		{post("/api/v1/resource/note").session(admin).json(J{"title": 5}), "validation_failed"},
		// DELETE needs version.
		{reqOf(http.MethodDelete, one).session(admin), "invalid_query"},
		{reqOf(http.MethodDelete, one+"?version=x").session(admin), "invalid_query"},
		{reqOf(http.MethodDelete, one+"?version=1&x=1").session(admin), "invalid_query"},
		// Query parameters.
		{get("/api/v1/resource/note?limit=0").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?limit=501").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?limit=-1").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?offset=-1").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?bogus=1").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?sort=nope").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?sort=title:sideways").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?filters=%FF").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?filters=not-json").session(admin), "invalid_query"},
		{get("/api/v1/resource/note?filters=%7B%7D").session(admin), "invalid_query"},
	}
	for _, c := range cases {
		rep := e.send(c.r)
		if rep.status < 400 || rep.status > 499 {
			t.Fatalf("%s %s: %d %s", c.r.method, c.r.uri, rep.status, rep.text)
		}
		if got := rep.code(); got != c.code {
			t.Errorf("%s %s: code %q, want %q: %s", c.r.method, c.r.uri, got, c.code, rep.text)
		}
	}
}

// Rust: resources.rs::repeated_and_unknown_query_parameters_are_refused_everywhere
func TestRepeatedAndUnknownQueryParametersAreRefusedEverywhere(t *testing.T) {
	e, admin := resourceSetup(t)
	note := e.adminInsert(admin, "note", J{"title": "n"})
	one := uri("note", note)
	list := "/api/v1/resource/note"
	cases := []*req{
		// A repeated parameter is refused, whichever value a parser would keep.
		get(list + "?limit=1&limit=500"),
		get(list + "?limit=500&limit=1"),
		get(list + "?sort=title&sort=points"),
		reqOf(http.MethodDelete, one+"?version=1&version=2"),
		// Routes without parameters refuse any, for consistency.
		get(one + "?x=1"),
		post(list + "?x=1").json(J{"title": "t"}),
		reqOf(http.MethodPatch, one+"?x=1").json(J{"version": 1, "data": J{}}),
		post(one + "/submit?x=1").json(J{"version": 1}),
		post(one + "/cancel?x=1").json(J{"version": 1}),
		post(one + "/amend?x=1").json(J{"version": 1}),
	}
	for _, c := range cases {
		rep := e.send(c.session(admin))
		wantCode(t, rep, http.StatusBadRequest, "invalid_query", c.method, c.uri)
	}
	// Nothing was created, and a single parameter still works.
	rep := e.send(get(list + "?limit=1").session(admin))
	wantStatus(t, rep, http.StatusOK)
	if len(rep.dataList()) != 1 {
		t.Error(rep.text)
	}
	wantStatus(t, e.send(reqOf(http.MethodDelete, one+"?version=1").session(admin)), http.StatusNoContent)
}

// Rust: resources.rs::listing_filters_sorts_and_pages
func TestListingFiltersSortsAndPages(t *testing.T) {
	e, admin := resourceSetup(t)
	for _, n := range []struct {
		title  string
		points int
	}{{"a", 3}, {"b", 1}, {"c", 2}} {
		e.adminInsert(admin, "note", J{"title": n.title, "points": n.points})
	}
	titles := func(query string) []string {
		rep := e.send(get("/api/v1/resource/note?" + query).cookie(admin))
		wantStatus(t, rep, http.StatusOK)
		var out []string
		for _, d := range rep.dataList() {
			out = append(out, str(d, "title"))
		}
		return out
	}
	check := func(got []string, want string) {
		t.Helper()
		if strings.Join(got, ",") != want {
			t.Errorf("got %v, want %s", got, want)
		}
	}
	check(titles("sort=points"), "b,c,a")
	check(titles("sort=points:desc"), "a,c,b")
	check(titles("sort=points:desc&limit=1&offset=1"), "c")
	check(titles("filters="+enc(`[{"field":"points","op":">=","value":2}]`)+"&sort=title"), "a,c")
	// A leading + is accepted for the numbers, as Rust's parse does.
	check(titles("sort=points&limit=%2B1"), "b")
}

// Rust: resources.rs::decision_9_no_leaks_through_links
func TestDecision9NoLeaksThroughLinks(t *testing.T) {
	e, admin := resourceSetup(t)
	e.adminInsert(admin, "vault", J{"code": "v1"})
	e.adminInsert(admin, "item", J{"code": "i0"})
	e.adminInsert(admin, "item", J{"code": "i1"})
	clerk := e.apiToken("clerk")

	// A Link to a DocType the clerk cannot read: the same 422 as a Link to nothing.
	hidden := e.send(post("/api/v1/resource/holder").token(clerk).json(J{"title": "t", "vault": "v1"}))
	missing := e.send(post("/api/v1/resource/holder").token(clerk).json(J{"title": "t", "vault": "v2"}))
	wantCode(t, hidden, http.StatusUnprocessableEntity, "validation_failed")
	if hidden.status != missing.status || strings.ReplaceAll(hidden.text, "v1", "v2") != missing.text {
		t.Errorf("%s vs %s", hidden.text, missing.text)
	}
	// The admin can link to the vault.
	linked := e.send(post("/api/v1/resource/holder").session(admin).json(J{"title": "t", "vault": "v1", "item": "i0"}))
	wantStatus(t, linked, http.StatusCreated)

	// A document that unreadable documents link to: the clerk's delete fails without saying who.
	e.adminInsert(admin, "locked", J{"item": "i1"})
	blocked := e.send(reqOf(http.MethodDelete, "/api/v1/resource/item/i1?version=1").token(clerk))
	wantCode(t, blocked, http.StatusConflict, "linked_document_exists")
	if strings.Contains(blocked.text, "locked") || strings.Contains(blocked.text, "holder") {
		t.Error(blocked.text)
	}
}

// Rust: resources.rs::injection_attempts_are_inert
func TestInjectionAttemptsAreInert(t *testing.T) {
	e, admin := resourceSetup(t)
	note := e.adminInsert(admin, "note", J{"title": "n"})
	evil := []string{
		"x%22%3B%20DROP%20TABLE%20fb_user%3B--",
		"note%22%20OR%20%221%22%3D%221",
		"..%2F..%2Fetc",
		"%27%3B%20DROP%20TABLE%20fb_user%3B--",
		"note%00",
		"fb_user",
		"doc_note",
		"fb_session",
	}
	for _, name := range evil {
		for _, r := range []*req{
			get("/api/v1/resource/" + name),
			get("/api/v1/resource/" + name + "/x"),
			reqOf(http.MethodDelete, "/api/v1/resource/"+name+"/x?version=1"),
			post("/api/v1/resource/" + name).json(J{}),
			post("/api/v1/resource/" + name + "/x/submit").json(J{"version": 1}),
		} {
			rep := e.send(r.session(admin))
			wantCode(t, rep, http.StatusNotFound, "not_found", name, r.uri)
		}
	}
	// Malicious filter and sort keys never reach SQL.
	for _, filters := range []string{
		`[{"field":"title\" = 'x' OR 1=1; --","op":"=","value":"x"}]`,
		`[{"field":"title","op":"= 1; DROP TABLE fb_user; --","value":"x"}]`,
		`[{"field":"1=1","op":"is null"}]`,
		`[{"field":"title","op":"=","value":"x","extra":"y"}]`,
	} {
		rep := e.send(get("/api/v1/resource/note?filters=" + enc(filters)).session(admin))
		wantStatus(t, rep, http.StatusBadRequest, filters)
	}
	for _, sort := range []string{"title;DROP TABLE fb_user", `title" desc, (select 1)`, "(select 1):desc"} {
		rep := e.send(get("/api/v1/resource/note?sort=" + enc(sort)).session(admin))
		wantStatus(t, rep, http.StatusBadRequest, sort)
	}
	// Values are bound parameters: inert text. Also as a document name and as data.
	value := "x'; DROP TABLE fb_user; --"
	filters := fmt.Sprintf(`[{"field":"title","op":"=","value":%q}]`, value)
	rep := e.send(get("/api/v1/resource/note?filters=" + enc(filters)).session(admin))
	wantStatus(t, rep, http.StatusOK)
	eqJSON(t, rep.data(), `[]`)
	created := e.adminInsert(admin, "note", J{"title": value})
	rep = e.send(get(uri("note", created)).session(admin))
	if str(rep.data(), "title") != value {
		t.Error(rep.text)
	}
	wantStatus(t, e.send(get(uri("note", enc("'; DROP TABLE fb_user; --"))).session(admin)), http.StatusNotFound)
	wantStatus(t, e.send(get(uri("note", note)).session(admin)), http.StatusOK)
	// The tables are all still there (the session above proves fb_user and fb_session).
	if n := len(e.send(get("/api/v1/resource/note").session(admin)).dataList()); n != 2 {
		t.Errorf("%d notes", n)
	}
}

// Rust: resources.rs::a_child_doctype_is_not_a_resource_and_table_rows_round_trip
func TestAChildDoctypeIsNotAResourceAndTableRowsRoundTrip(t *testing.T) {
	e, admin := resourceSetup(t)
	for _, name := range []string{"note_line", "Note", "NOTE", "notes", ""} {
		wantStatus(t, e.send(get("/api/v1/resource/"+name).session(admin)), http.StatusNotFound, name)
	}
	name := e.adminInsert(admin, "note", J{"title": "t", "lines": []any{J{"text": "a"}, J{"text": "b"}}})
	rep := e.send(get(uri("note", name)).session(admin))
	var texts []any
	for _, r := range at(rep.data(), "lines").([]any) {
		texts = append(texts, at(r, "text"))
	}
	eqJSON(t, texts, `["a","b"]`)
}

// Rust: resources.rs::a_caller_cannot_set_a_read_only_field_422_validation_failed
func TestACallerCannotSetAReadOnlyField422ValidationFailed(t *testing.T) {
	e := newEnv(t)
	e.user("keeper", "Keeper")
	keeper := e.login("keeper")
	details := func(rep reply) string {
		t.Helper()
		wantCode(t, rep, http.StatusUnprocessableEntity, "validation_failed")
		return strings.Join(rep.details(), "|")
	}
	// Insert: the top-level field and a row field are both named.
	rep := e.send(post("/api/v1/resource/ledger").session(keeper).json(J{"title": "t", "total": 5, "lines": []any{J{"qty": 1, "amount": 2}}}))
	if got := details(rep); got != "field `total` is read-only|field `lines` row 1: field `amount` is read-only" {
		t.Error(got)
	}
	// Without the computed values the insert works; null counts as "not given".
	rep = e.send(post("/api/v1/resource/ledger").session(keeper).json(J{"title": "t", "total": nil, "lines": []any{J{"qty": 1}}}))
	wantStatus(t, rep, http.StatusCreated)
	name := str(rep.data(), "name")

	// Update: a changed value is refused, the stored one (null) sent back is not.
	patch := func(name string, data J) reply {
		return e.send(reqOf(http.MethodPatch, uri("ledger", name)).session(keeper).json(J{"version": 1, "data": data}))
	}
	if got := details(patch(name, J{"total": 9})); got != "field `total` is read-only" {
		t.Error(got)
	}
	if got := details(patch(name, J{"lines": []any{J{"qty": 1, "amount": 3}}})); got != "field `lines` row 1: field `amount` is read-only" {
		t.Error(got)
	}
	wantStatus(t, patch(name, J{"title": "u", "total": nil}), http.StatusOK)

	// A hook-computed value cannot be cleared with null; sending it back is fine.
	rep = e.send(post("/api/v1/resource/ledger").session(keeper).json(J{"title": "computed"}))
	wantStatus(t, rep, http.StatusCreated)
	eqJSON(t, at(rep.data(), "total"), `7`)
	name = str(rep.data(), "name")
	if got := details(patch(name, J{"total": nil})); got != "field `total` is read-only" {
		t.Error(got)
	}
	wantStatus(t, patch(name, J{"total": 7}), http.StatusOK)
}
