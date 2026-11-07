package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The route handlers (the Rust module routes.rs). Every handler that needs a user authenticates first,
// so authentication and CSRF are decided before the path, query or body is looked at; a handler with a
// JSON body then reads it before it looks at the path and the query, as the Rust extractors do (so a
// body that is not JSON is 415, 413 or 400 even for an unknown DocType).

// cookieMaxAgeSecs is the session lifetime in the cookie (Max-Age): the 7 days of the session's
// absolute expiry.
const cookieMaxAgeSecs = 7 * 24 * 3600

// topLevel returns the DocType called name that is not a child DocType, or the 404.
func topLevel(registry *core.Registry, name string) (*core.DocType, error) {
	d := registry.Get(name)
	if d == nil || d.IsChild {
		return nil, errNotFound("unknown doctype")
	}
	return d, nil
}

// require refuses with permission_denied when the caller has no DocType-level access for one of
// rights, before the body or the query is mapped. Mapping errors name fields, so they must not reach a
// caller who may not use the DocType at all. This asks the one evaluator of internal/core and decides
// nothing itself; internal/db still enforces every right.
func require(doctype *core.DocType, caller core.Caller, rights []core.Right, reported core.Right) error {
	for _, right := range rights {
		if core.Evaluate(doctype, caller, right) == core.AccessDenied {
			return &db.Error{Kind: db.ErrPermissionDenied, DocType: doctype.Name, Right: reported}
		}
	}
	return nil
}

// syntaxToQuery is a problem with the filters or sort parameter (not with a body).
func syntaxToQuery(err error) error {
	if je, ok := err.(*JSONError); ok && je.Kind == JSONSyntax {
		return errInvalidQuery("invalid filter: not valid JSON")
	}
	return err
}

// cookieHeader is the session cookie with the attributes of the owner decision: HttpOnly, Secure
// (unless the explicit development mode), SameSite=Lax, Path=/.
func (s *State) cookieHeader(value string, maxAge int) string {
	secure := ""
	if s.config.SecureCookies {
		secure = " Secure;"
	}
	return fmt.Sprintf("%s=%s; HttpOnly;%s SameSite=Lax; Path=/; Max-Age=%d", sessionCookie, value, secure, maxAge)
}

// object is a JSON object with no keys other than allowed.
func object(v any, allowed ...string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errBadRequest("the body must be a JSON object")
	}
	for _, key := range sortedKeys(m) {
		known := false
		for _, a := range allowed {
			known = known || a == key
		}
		if !known {
			return nil, errBadRequest("unknown key " + core.DebugQuote(core.Shorten(key)))
		}
	}
	return m, nil
}

// asInt64 is the integer of a JSON number, like serde_json's as_i64.
func asInt64(v any) (int64, bool) {
	n, ok := v.(jsonNumber)
	if !ok {
		return 0, false
	}
	return n.int64Value()
}

func version(m map[string]any) (int64, error) {
	v, ok := asInt64(m["version"])
	if !ok {
		return 0, errBadRequest("`version` must be an integer")
	}
	return v, nil
}

// parseInt64 and parseUint read a number as Rust's str::parse does: a leading "+" is allowed.
func parseInt64(text string) (int64, bool) {
	i, err := strconv.ParseInt(text, 10, 64)
	return i, err == nil
}

func parseUint(text string, bits int) (uint64, bool) {
	i, err := strconv.ParseUint(strings.TrimPrefix(text, "+"), 10, bits)
	return i, err == nil
}

func versionParam(query map[string]string) (int64, error) {
	if v, ok := query["version"]; ok {
		if i, ok := parseInt64(v); ok {
			return i, nil
		}
	}
	return 0, errInvalidQuery("`version` must be an integer")
}

// linkTitlesFlag is the link_titles parameter (UX.4): absent is false, 1 is true, anything else is
// invalid_query.
func linkTitlesFlag(query map[string]string) (bool, error) {
	switch v, ok := query["link_titles"]; {
	case !ok:
		return false, nil
	case v == "1":
		return true, nil
	}
	return false, errInvalidQuery("`link_titles` must be 1")
}

// params are the query parameters as a map: invalid_query for a parameter that is not in allowed or
// that is repeated (?limit=1&limit=500 must not depend on which one a parser keeps).
func params(r *request, allowed ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range queryPairs(r.URL.RawQuery) {
		key := pair[0]
		known := false
		for _, a := range allowed {
			known = known || a == key
		}
		if !known {
			return nil, errInvalidQuery("unknown parameter " + core.DebugQuote(core.Shorten(key)))
		}
		if _, dup := out[key]; dup {
			return nil, errInvalidQuery("duplicate parameter " + core.DebugQuote(core.Shorten(key)))
		}
		out[key] = pair[1]
	}
	return out, nil
}

// withLinkTitles is the response of a read: {"data": value}, plus a sibling link_titles key when
// asked for. Without the parameter the output is exactly what it was before UX.4.
func (s *State) withLinkTitles(w http.ResponseWriter, r *request, registry *core.Registry, caller core.Caller,
	doctype string, documents []*db.StoredDocument, value any) error {
	titles, err := db.LinkTitles(r.Context(), s.pool, registry, caller, doctype, documents)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": value, "link_titles": titles})
	return nil
}

// ---- login and logout -------------------------------------------------------------------------

// login is POST /api/v1/login. Not authenticated (and so not CSRF-protected: there is no session yet).
func handleLogin(s *State, w http.ResponseWriter, r *request) error {
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	m, err := object(body, "username", "password")
	if err != nil {
		return err
	}
	username, ok1 := m["username"].(string)
	password, ok2 := m["password"].(string)
	if !ok1 || !ok2 {
		return errBadRequest("`username` and `password` must be strings")
	}
	// Bounds the Argon2 work (memory) and the connections that logins hold. Not waiting: an excess
	// login is turned away so an attacker cannot queue requests.
	select {
	case s.logins <- struct{}{}:
		defer func() { <-s.logins }()
	default:
		return errTooManyLogins()
	}
	ls, err := db.Login(r.Context(), s.pool, username, password)
	if err != nil {
		return err
	}
	roles := append([]string{}, ls.User.Roles...)
	w.Header().Add("Set-Cookie", s.cookieHeader(ls.Token, cookieMaxAgeSecs))
	writeData(w, http.StatusOK, map[string]any{
		"username":   ls.User.Username,
		"roles":      roles,
		"csrf_token": ls.CSRFToken,
	})
	return nil
}

// logout is POST /api/v1/logout: ends the cookie session and clears the cookie. A request that
// authenticated with an API token has no session to end: 400.
func handleLogout(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	if auth.session == "" {
		return errBadRequest("logout needs a cookie session; revoke an API token with the command-line tool")
	}
	if _, err := db.Logout(r.Context(), s.pool, auth.session); err != nil {
		return err
	}
	w.Header().Add("Set-Cookie", s.cookieHeader("", 0))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// session is GET /api/v1/session: the user of the cookie session and a new CSRF token, so that the desk
// can keep the token in memory only and recover it after a page reload. The session's stored CSRF
// digest is replaced: the previous token (also the one another tab holds) stops working, and a request
// that fails with csrf_failed can call this once and retry. Safe because the response cannot be read
// cross-origin (no CORS) and the cookie is SameSite=Lax. An API token has no session: 400.
//
// The request must carry "X-Forgebase-CSRF: refresh", else 400 bad_request and nothing is rotated. A
// navigation or an <img> cannot send a custom header, and a cross-origin fetch with it needs a
// preflight, which this server answers with 405 and no CORS headers; so a foreign page cannot make the
// desk's in-memory token stop working.
func handleSession(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	// Exactly one value: a repeated header must not depend on which one a parser keeps.
	if values := r.Header.Values(csrfHeader); len(values) != 1 || values[0] != "refresh" {
		return errBadRequest("this request needs the header X-Forgebase-CSRF: refresh")
	}
	if auth.session == "" {
		return errBadRequest("a session needs a cookie; an API token has none")
	}
	csrf, err := db.RotateCSRF(r.Context(), s.pool, auth.session)
	if err != nil {
		return err
	}
	if csrf == "" {
		// The session ended between the check and now; the same 401 as any other.
		return errUnauthenticated()
	}
	writeData(w, http.StatusOK, map[string]any{
		"username":   auth.user.Username,
		"roles":      append([]string{}, auth.user.Roles...),
		"csrf_token": csrf,
	})
	return nil
}

// ---- resources --------------------------------------------------------------------------------

// list is GET /api/v1/resource/{doctype} with filters, sort, limit and offset, and link_titles=1 to add
// the titles of the linked documents the caller may read (UX.4).
func handleList(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if err := require(doctype, caller, []core.Right{core.RightRead}, core.RightRead); err != nil {
		return err
	}
	query, err := params(r, "filters", "sort", "limit", "offset", "link_titles")
	if err != nil {
		return err
	}
	titles, err := linkTitlesFlag(query)
	if err != nil {
		return err
	}
	// A field the caller cannot read is unknown to the filters (ADR 0024).
	readable := core.ReadableDocTypeFor(doctype, caller)
	var listQuery db.ListQuery
	if text, ok := query["filters"]; ok {
		if listQuery.Filters, err = ParseFilters(readable, text); err != nil {
			return syntaxToQuery(err)
		}
	}
	if text, ok := query["sort"]; ok {
		if listQuery.Sort, err = ParseSort(strings.Split(text, ",")); err != nil {
			return syntaxToQuery(err)
		}
	}
	if text, ok := query["limit"]; ok {
		n, ok := parseUint(text, 32)
		if !ok {
			return errInvalidQuery("`limit` must be a positive integer")
		}
		limit := uint32(n)
		listQuery.Limit = &limit
	}
	if text, ok := query["offset"]; ok {
		n, ok := parseUint(text, 64)
		if !ok {
			return errInvalidQuery("`offset` must be a non-negative integer")
		}
		listQuery.Offset = &n
	}
	documents, err := db.List(r.Context(), s.pool, registry, doctype.Name, caller, listQuery)
	if err != nil {
		return err
	}
	value := make([]any, len(documents))
	for i, d := range documents {
		if value[i], err = db.DocumentToJSON(d); err != nil {
			return err
		}
	}
	if titles {
		return s.withLinkTitles(w, r, registry, caller, doctype.Name, documents, value)
	}
	writeData(w, http.StatusOK, value)
	return nil
}

// create is POST /api/v1/resource/{doctype}: the body is the document.
func handleCreate(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if err := require(doctype, caller, []core.Right{core.RightCreate}, core.RightCreate); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	// A field the caller cannot read is unknown to the input: its value is not even looked at.
	readable := core.ReadableDocTypeFor(doctype, caller)
	input, err := InputFromJSON(registry, readable, body)
	if err != nil {
		return err
	}
	stored, err := db.Insert(r.Context(), s.pool, registry, doctype.Name,
		core.Document{Values: input.Values, Tables: input.Tables}, caller)
	if err != nil {
		return err
	}
	return writeDocument(w, http.StatusCreated, stored)
}

func writeDocument(w http.ResponseWriter, status int, stored *db.StoredDocument) error {
	value, err := db.DocumentToJSON(stored)
	if err != nil {
		return err
	}
	writeData(w, status, value)
	return nil
}

// read is GET /api/v1/resource/{doctype}/{name}, with link_titles=1 as for the list.
func handleRead(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	query, err := params(r, "link_titles")
	if err != nil {
		return err
	}
	titles, err := linkTitlesFlag(query)
	if err != nil {
		return err
	}
	caller := auth.caller()
	stored, err := db.Get(r.Context(), s.pool, registry, doctype.Name, path[1], caller)
	if err != nil {
		return err
	}
	if stored == nil {
		return &db.Error{Kind: db.ErrNotFound, DocType: doctype.Name, Name: path[1]}
	}
	value, err := db.DocumentToJSON(stored)
	if err != nil {
		return err
	}
	if titles {
		return s.withLinkTitles(w, r, registry, caller, doctype.Name, []*db.StoredDocument{stored}, value)
	}
	writeData(w, http.StatusOK, value)
	return nil
}

// update is PATCH /api/v1/resource/{doctype}/{name}: body {"version": N, "data": {changes}}.
func handleUpdate(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if err := require(doctype, caller, []core.Right{core.RightWrite, core.RightRead}, core.RightWrite); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	m, err := object(body, "version", "data")
	if err != nil {
		return err
	}
	expected, err := version(m)
	if err != nil {
		return err
	}
	changes, ok := m["data"]
	if !ok {
		return errBadRequest("`data` must be a JSON object")
	}
	readable := core.ReadableDocTypeFor(doctype, caller)
	input, err := InputFromJSON(registry, readable, changes)
	if err != nil {
		return err
	}
	stored, err := db.Update(r.Context(), s.pool, registry, doctype.Name, path[1], expected,
		core.Changes{Values: input.Values, Tables: input.Tables}, caller)
	if err != nil {
		return err
	}
	return writeDocument(w, http.StatusOK, stored)
}

// remove is DELETE /api/v1/resource/{doctype}/{name}?version=N: 204 without a body.
func handleDelete(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	query, err := params(r, "version")
	if err != nil {
		return err
	}
	expected, err := versionParam(query)
	if err != nil {
		return err
	}
	if err := db.Delete(r.Context(), s.pool, registry, doctype.Name, path[1], expected, auth.caller()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// lifecycle runs submit, cancel or amend: the body is {"version": N}.
func lifecycle(s *State, w http.ResponseWriter, r *request,
	op func(db.TxStarter, *core.Registry, string, string, int64, core.Caller) (*db.StoredDocument, error),
	status int) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	m, err := object(body, "version")
	if err != nil {
		return err
	}
	expected, err := version(m)
	if err != nil {
		return err
	}
	stored, err := op(s.pool, registry, doctype.Name, path[1], expected, auth.caller())
	if err != nil {
		return err
	}
	return writeDocument(w, status, stored)
}

// submit is POST /api/v1/resource/{doctype}/{name}/submit: body {"version": N}.
func handleSubmit(s *State, w http.ResponseWriter, r *request) error {
	return lifecycle(s, w, r, func(c db.TxStarter, reg *core.Registry, dt, name string, v int64, caller core.Caller) (*db.StoredDocument, error) {
		return db.Submit(r.Context(), c, reg, dt, name, v, caller)
	}, http.StatusOK)
}

// cancel is POST /api/v1/resource/{doctype}/{name}/cancel: body {"version": N}.
func handleCancel(s *State, w http.ResponseWriter, r *request) error {
	return lifecycle(s, w, r, func(c db.TxStarter, reg *core.Registry, dt, name string, v int64, caller core.Caller) (*db.StoredDocument, error) {
		return db.Cancel(r.Context(), c, reg, dt, name, v, caller)
	}, http.StatusOK)
}

// amend is POST /api/v1/resource/{doctype}/{name}/amend: body {"version": N}; 201 with the new
// document (the amendment is a new document).
func handleAmend(s *State, w http.ResponseWriter, r *request) error {
	return lifecycle(s, w, r, func(c db.TxStarter, reg *core.Registry, dt, name string, v int64, caller core.Caller) (*db.StoredDocument, error) {
		return db.Amend(r.Context(), c, reg, dt, name, v, caller)
	}, http.StatusCreated)
}
