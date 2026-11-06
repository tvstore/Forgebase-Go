package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Shared helpers of the integration tests (the Rust tests/common/mod.rs): one throwaway database per
// test with the framework tables and the test DocTypes, and a small client that sends requests
// straight into the handler (no network).

const testPassword = "correct horse battery"

// item is a Link target that Clerk may create and delete.
const itemDoc = `
name = "item"
label = "Item"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Clerk"
read = true
create = true
delete = true
`

// holderDoc is a Link to a DocType the Clerk may read (item) and to one nobody but Administrator may
// read (vault).
const holderDoc = `
name = "holder"
label = "Holder"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "item"
type = "link"
target = "item"
[[fields]]
name = "vault"
type = "link"
target = "vault"
[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Clerk"
read = true
create = true
write = true
delete = true
`

const vaultDoc = `
name = "vault"
label = "Vault"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
`

// lockedDoc has no rules, and a Link to item: a document here blocks the deletion of its item.
const lockedDoc = `
name = "locked"
label = "Locked"
[[fields]]
name = "item"
type = "link"
target = "item"
`

// noteDoc: Reader reads, Writer has every right except the lifecycle ones, Own only on its own.
const noteDoc = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "body"
type = "text"
[[fields]]
name = "points"
type = "int"
[[fields]]
name = "lines"
type = "table"
target = "note_line"
[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Writer"
read = true
write = true
create = true
delete = true
[[permissions]]
role = "Own"
read = true
write = true
create = true
delete = true
if_owner = true
`

const noteLineDoc = `
name = "note_line"
label = "Note Line"
is_child = true
[[fields]]
name = "text"
type = "data"
`

const billDoc = `
name = "bill"
label = "Bill"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[permissions]]
role = "Viewer"
read = true
[[permissions]]
role = "Biller"
read = true
write = true
create = true
delete = true
submit = true
cancel = true
amend = true
`

// ledgerDoc has read-only fields (a computed total and a computed line amount), a title and list
// fields. Keeper has every plain right.
const ledgerDoc = `
name = "ledger"
label = "Ledger"
title_field = "title"
list_fields = ["title", "total"]
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "total"
type = "int"
read_only = true
[[fields]]
name = "lines"
type = "table"
target = "ledger_line"
[[permissions]]
role = "Keeper"
read = true
write = true
create = true
delete = true
`

const ledgerLineDoc = `
name = "ledger_line"
label = "Ledger Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "amount"
type = "int"
read_only = true
`

// secretDoc has no rules at all: closed to everyone but Administrator.
const secretDoc = `
name = "secret"
label = "Secret"
[[fields]]
name = "x"
type = "data"
`

// dropboxDoc: Dropper may write and create but not read: the update pre-check must refuse it.
const dropboxDoc = `
name = "dropbox"
label = "Dropbox"
[[fields]]
name = "x"
type = "data"
[[permissions]]
role = "Dropper"
write = true
create = true
`

// purchaseDoc is a submittable DocType with a workflow (M8.1): Draft, Pending (only a Manager may edit
// it), Approved (submitted) and Cancelled. Reviewer is a role of Approve but cannot submit, Auditor can
// submit and cancel but is a role of no transition, and Own Writer reads everything but creates, writes
// and submits only its own documents.
const purchaseDoc = `
name = "purchase"
label = "Purchase"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "amount"
type = "int"
[[permissions]]
role = "Clerk"
read = true
create = true
write = true
[[permissions]]
role = "Manager"
read = true
create = true
write = true
delete = true
submit = true
cancel = true
amend = true
[[permissions]]
role = "Reviewer"
read = true
write = true
[[permissions]]
role = "Auditor"
read = true
submit = true
cancel = true
[[permissions]]
role = "Own Writer"
read = true
[[permissions]]
role = "Own Writer"
create = true
write = true
submit = true
if_owner = true

[workflow]
[[workflow.states]]
name = "Draft"
docstatus = 0
[[workflow.states]]
name = "Pending"
docstatus = 0
style = "warning"
edit_roles = ["Manager"]
[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"
[[workflow.states]]
name = "Cancelled"
docstatus = 2
style = "danger"
[[workflow.transitions]]
action = "Send"
from = "Draft"
to = "Pending"
roles = ["Clerk", "Own Writer"]
[[workflow.transitions]]
action = "Reject"
from = "Pending"
to = "Draft"
roles = ["Manager"]
[[workflow.transitions]]
action = "Approve"
from = "Pending"
to = "Approved"
roles = ["Manager", "Reviewer", "Own Writer"]
[[workflow.transitions]]
action = "Cancel"
from = "Approved"
to = "Cancelled"
roles = ["Manager"]
`

// ledgerTotal sets the read-only total of a ledger titled "computed" to 7, like an app's hook.
type ledgerTotal struct{ core.BaseHooks }

func (ledgerTotal) Validate(_ core.HookContext, doc *core.Document) error {
	if v := doc.Values["title"]; v.Kind == core.KindText && v.Text == "computed" {
		doc.Values["total"] = core.Int(7)
	}
	return nil
}

// testRegistry is the test DocTypes and those of extra (TOML texts).
func testRegistry(t testing.TB, extra ...string) *core.Registry {
	t.Helper()
	texts := append(append([]string{}, extra...), itemDoc, holderDoc, vaultDoc, lockedDoc, noteDoc, noteLineDoc,
		billDoc, ledgerDoc, ledgerLineDoc, secretDoc, dropboxDoc)
	registry := dbtest.Registry(t, texts...)
	if err := registry.AddHooks("ledger", ledgerTotal{}); err != nil {
		t.Fatal(err)
	}
	return registry
}

// ---- replies and requests ------------------------------------------------------------------------

// reply is what a request got back.
type reply struct {
	t      testing.TB
	status int
	header http.Header
	text   string
}

// obj is the whole body as a JSON value.
func (r reply) obj() any {
	r.t.Helper()
	var v any
	dec := json.NewDecoder(strings.NewReader(r.text))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		r.t.Fatalf("not JSON (%v): %q", err, r.text)
	}
	return v
}

// data is the data of a success response.
func (r reply) data() any {
	r.t.Helper()
	m, ok := r.obj().(map[string]any)
	if !ok {
		r.t.Fatalf("not an object: %q", r.text)
	}
	return m["data"]
}

// dataObj is the data as an object, dataList as a list.
func (r reply) dataObj() map[string]any {
	r.t.Helper()
	m, ok := r.data().(map[string]any)
	if !ok {
		r.t.Fatalf("data is not an object: %q", r.text)
	}
	return m
}

func (r reply) dataList() []any {
	r.t.Helper()
	l, ok := r.data().([]any)
	if !ok {
		r.t.Fatalf("data is not a list: %q", r.text)
	}
	return l
}

// errorObj is the error object of an error response.
func (r reply) errorObj() map[string]any {
	r.t.Helper()
	m, ok := r.obj().(map[string]any)
	if !ok {
		r.t.Fatalf("not an object: %q", r.text)
	}
	e, ok := m["error"].(map[string]any)
	if !ok {
		r.t.Fatalf("no error object in %q", r.text)
	}
	return e
}

// code is error.code of an error response.
func (r reply) code() string {
	r.t.Helper()
	code, ok := r.errorObj()["code"].(string)
	if !ok {
		r.t.Fatalf("no error code in %q", r.text)
	}
	return code
}

func (r reply) details() []string {
	r.t.Helper()
	var out []string
	for _, d := range r.errorObj()["details"].([]any) {
		out = append(out, d.(string))
	}
	return out
}

func (r reply) setCookies() []string { return r.header.Values("Set-Cookie") }

// str is the string at the path of keys in the data (an object), "" when absent.
func str(v any, keys ...string) string {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

// at follows keys in a JSON value.
func at(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// eqJSON compares a decoded JSON value with the JSON text want.
func eqJSON(t testing.TB, got any, want string) {
	t.Helper()
	var w any
	dec := json.NewDecoder(strings.NewReader(want))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		t.Fatalf("bad expectation %q: %v", want, err)
	}
	norm := func(v any) any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var out any
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !reflect.DeepEqual(norm(got), norm(w)) {
		g, _ := json.Marshal(got)
		t.Errorf("got %s, want %s", g, want)
	}
}

// session is a login: the cookie value and the CSRF token.
type session struct{ token, csrf string }

// req is a request under construction.
type req struct {
	method, uri string
	headers     [][2]string
	host        string
	body        []byte
	contentType string
}

func reqOf(method, uri string) *req { return &req{method: method, uri: uri, host: "localhost"} }
func get(uri string) *req           { return reqOf(http.MethodGet, uri) }
func post(uri string) *req          { return reqOf(http.MethodPost, uri) }

func (r *req) header(name, value string) *req {
	r.headers = append(r.headers, [2]string{name, value})
	return r
}

// onHost sets the Host; noHost leaves it out.
func (r *req) onHost(host string) *req { r.host = host; return r }
func (r *req) noHost() *req            { r.host = ""; return r }

// cookie is the session cookie only.
func (r *req) cookie(s session) *req { return r.cookieValue(s.token) }

func (r *req) cookieValue(v string) *req { return r.header("Cookie", "fb_session="+v) }
func (r *req) csrf(token string) *req    { return r.header("X-Forgebase-CSRF", token) }

// session is the cookie and its CSRF token.
func (r *req) session(s session) *req { return r.cookie(s).csrf(s.csrf) }

// token is "Authorization: token <key>:<secret>".
func (r *req) token(keySecret string) *req { return r.header("Authorization", "token "+keySecret) }

func (r *req) json(v any) *req {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return r.raw("application/json", b)
}

func (r *req) raw(contentType string, body []byte) *req {
	r.contentType, r.body = contentType, body
	return r
}

// sendTo sends the request into h.
func sendTo(t testing.TB, h http.Handler, r *req) reply {
	t.Helper()
	var body io.Reader = http.NoBody
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.uri, body)
	hr.Host = r.host
	for _, kv := range r.headers {
		hr.Header.Add(kv[0], kv[1])
	}
	if r.body != nil {
		hr.Header.Set("Content-Type", r.contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{t: t, status: res.StatusCode, header: res.Header, text: string(data)}
}

// ---- the environment -----------------------------------------------------------------------------

// env is a database, the handler on it and a log of every reply.
type env struct {
	t     *testing.T
	db    *dbtest.DB
	state *State
	h     http.Handler
	mu    sync.Mutex
	log   []reply
}

func newEnv(t *testing.T) *env { return newEnvWith(t, DefaultConfig()) }

// newEnvWith has the test DocTypes and those of extra (TOML texts), on a server with config.
func newEnvWith(t *testing.T, config Config, extra ...string) *env {
	t.Helper()
	return newEnvRegistry(t, config, testRegistry(t, extra...))
}

func newEnvRegistry(t *testing.T, config Config, registry *core.Registry) *env {
	t.Helper()
	d := dbtest.Migrated(t)
	if _, err := d.Sync(registry); err != nil {
		t.Fatalf("sync: %v", err)
	}
	state := NewState(d.Pool, registry, config)
	return &env{t: t, db: d, state: state, h: ImplicitSites(state)}
}

func (e *env) send(r *req) reply {
	e.t.Helper()
	rep := sendTo(e.t, e.h, r)
	e.mu.Lock()
	e.log = append(e.log, rep)
	e.mu.Unlock()
	return rep
}

// user creates a user with roles (password testPassword).
func (e *env) user(username string, roles ...string) {
	e.t.Helper()
	if _, err := db.CreateUser(context.Background(), e.db.Pool, username, testPassword, roles); err != nil {
		e.t.Fatalf("create user: %v", err)
	}
}

// login logs in over HTTP.
func (e *env) login(username string) session {
	e.t.Helper()
	rep := e.tryLogin(username, testPassword)
	if rep.status != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", username, rep.status, rep.text)
	}
	return sessionOf(e.t, rep)
}

func sessionOf(t testing.TB, rep reply) session {
	t.Helper()
	cookies := rep.setCookies()
	if len(cookies) == 0 {
		t.Fatalf("no session cookie in %q", rep.text)
	}
	value, _, _ := strings.Cut(strings.TrimPrefix(cookies[0], "fb_session="), ";")
	return session{token: value, csrf: str(rep.data(), "csrf_token")}
}

func (e *env) tryLogin(username, password string) reply {
	e.t.Helper()
	return e.send(post("/api/v1/login").json(map[string]any{"username": username, "password": password}))
}

// apiToken is an API token key:secret for username.
func (e *env) apiToken(username string) string {
	e.t.Helper()
	tok, err := db.CreateAPIToken(context.Background(), e.db.Pool, username)
	if err != nil {
		e.t.Fatalf("token: %v", err)
	}
	return tok.Key + ":" + tok.Secret
}

// adminInsert inserts a document of doctype over HTTP as the session admin and returns its name.
func (e *env) adminInsert(admin session, doctype string, body any) string {
	e.t.Helper()
	rep := e.send(post("/api/v1/resource/" + doctype).session(admin).json(body))
	if rep.status != http.StatusCreated {
		e.t.Fatalf("insert %s: %d %s", doctype, rep.status, rep.text)
	}
	return str(rep.data(), "name")
}

// enc percent-encodes every byte.
func enc(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		fmt.Fprintf(&b, "%%%02X", text[i])
	}
	return b.String()
}

// encQuery percent-encodes everything but the unreserved characters.
func encQuery(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte("-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func wantStatus(t testing.TB, r reply, status int, msg ...any) {
	t.Helper()
	if r.status != status {
		t.Fatalf("%v: status %d, want %d: %s", msg, r.status, status, r.text)
	}
}

func wantCode(t testing.TB, r reply, status int, code string, msg ...any) {
	t.Helper()
	wantStatus(t, r, status, msg...)
	if got := r.code(); got != code {
		t.Fatalf("%v: code %q, want %q: %s", msg, got, code, r.text)
	}
}

// assertEnvelope checks the shape of an error answer.
func assertEnvelope(t testing.TB, r reply, status int, code string) {
	t.Helper()
	wantCode(t, r, status, code)
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q: %s", ct, r.text)
	}
	m := r.obj().(map[string]any)
	if len(m) != 1 {
		t.Errorf("extra keys: %s", r.text)
	}
	if _, ok := r.errorObj()["message"].(string); !ok {
		t.Errorf("no message: %s", r.text)
	}
}

// J is a JSON object literal.
type J = map[string]any
