package httpapi

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Import and export over HTTP (M9.3, ADR 0021): the rights, the formats, the limits, the per-row errors and
// the report export, with the status code and the envelope of each case. Ported from
// crates/forgebase-http/tests/transfer.rs.

const trParcel = `
name = "parcel"
label = "Parcel"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "price"
type = "decimal"
precision = 12
scale = 2
[[fields]]
name = "due"
type = "date"
[[fields]]
name = "seen"
type = "datetime"
[[fields]]
name = "done"
type = "check"
[[fields]]
name = "tier"
type = "select"
options = ["gold", "silver"]
[[fields]]
name = "item"
type = "link"
target = "item"
[[fields]]
name = "lines"
type = "table"
target = "parcel_line"

[[permissions]]
role = "Exporter"
read = true
export = true
[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Export Only"
export = true
[[permissions]]
role = "Own Exporter"
read = true
export = true
if_owner = true
[[permissions]]
role = "Importer"
read = true
create = true
import = true
[[permissions]]
role = "Import No Create"
read = true
import = true
[[permissions]]
role = "Upserter"
read = true
create = true
write = true
import = true
`

// trTag is named by its code: a given name is allowed when it is the code.
const trTag = `
name = "tag"
label = "Tag"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "weight"
type = "int"
[[permissions]]
role = "Exporter"
read = true
export = true
[[permissions]]
role = "Importer"
read = true
create = true
import = true
`

// trStateful is a DocType without a workflow whose own field is called workflow_state.
const trStateful = `
name = "stateful"
label = "Stateful"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "workflow_state"
type = "data"
[[permissions]]
role = "Exporter"
read = true
export = true
[[permissions]]
role = "Importer"
read = true
create = true
import = true
`

const trParcelLine = `
name = "parcel_line"
label = "Parcel Line"
is_child = true
[[fields]]
name = "what"
type = "data"
`

// trRules sets the note of `hook-me` and refuses `reject`.
type trRules struct{ core.BaseHooks }

func (trRules) Validate(_ core.HookContext, doc *core.Document) error {
	if v := doc.Values["title"]; v.Kind == core.KindText {
		switch v.Text {
		case "reject":
			return core.NewHookError("rejected by the app")
		case "hook-me":
			doc.Values["note"] = core.Text("hooked")
		}
	}
	return nil
}

func trRegistryOf(t *testing.T) *core.Registry {
	t.Helper()
	registry := testRegistry(t, trParcel, trParcelLine, trTag, trStateful, purchaseDoc)
	if err := registry.AddHooks("parcel", trRules{}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func trSetupUsers(e *env) {
	for user, role := range map[string]string{"exporter": "Exporter", "reader": "Reader", "exportonly": "Export Only",
		"own": "Own Exporter", "importer": "Importer", "nocreate": "Import No Create", "upserter": "Upserter"} {
		// Clerk may read item, which a link in a file must be able to resolve.
		e.user(user, role, "Clerk")
	}
	e.user("root", "Administrator")
}

func trEnvWith(t *testing.T, config Config) *env {
	t.Helper()
	e := newEnvRegistry(t, config, trRegistryOf(t))
	trSetupUsers(e)
	return e
}

func trEnv(t *testing.T) *env { return trEnvWith(t, DefaultConfig()) }

func (e *env) trInsert(s session, body any) map[string]any {
	e.t.Helper()
	rep := e.send(post("/api/v1/resource/parcel").session(s).json(body))
	wantStatus(e.t, rep, http.StatusCreated, rep.text)
	return rep.dataObj()
}

func (e *env) trExport(s session, query string) reply {
	e.t.Helper()
	return e.send(get("/api/v1/export/parcel" + query).session(s))
}

// csvRows is every record of a CSV text, header included (the csv reader of the import).
func csvRows(text string) [][]string {
	r := &csvReader{text: text, line: 1}
	out := [][]string{}
	for {
		rec, ok := r.next()
		if !ok {
			return out
		}
		out = append(out, rec.fields)
	}
}

func (e *env) trImport(s session, query, contentType, body string) reply {
	e.t.Helper()
	return e.send(post("/api/v1/import/parcel"+query).session(s).raw(contentType, []byte(body)))
}

func (e *env) trTitles(admin session) []string {
	e.t.Helper()
	rep := e.send(get("/api/v1/resource/parcel?sort=title&limit=500").session(admin))
	out := []string{}
	for _, d := range rep.dataList() {
		out = append(out, str(d, "title"))
	}
	return out
}

func wantJSON(t *testing.T, got any, want string) {
	t.Helper()
	eqJSON(t, got, want)
}

// ---- export ----------------------------------------------------------------------------------------

// Rust: transfer.rs::export_needs_authentication_read_and_export
func TestExportNeedsAuthenticationReadAndExport(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.trInsert(root, J{"title": "a"})

	// Not logged in; CSRF is not needed for a GET.
	wantStatus(t, e.send(get("/api/v1/export/parcel")), http.StatusUnauthorized)
	exporter := e.login("exporter")
	cookieOnly := e.send(get("/api/v1/export/parcel").cookie(exporter))
	wantStatus(t, cookieOnly, http.StatusOK, cookieOnly.text)

	// Read without export, export without read: 403 permission_denied naming the right.
	for user, right := range map[string]string{"reader": "export", "exportonly": "read"} {
		rep := e.trExport(e.login(user), "")
		wantCode(t, rep, http.StatusForbidden, "permission_denied", user)
		if !strings.Contains(str(rep.errorObj(), "message"), right) {
			t.Errorf("%s: %s", user, rep.text)
		}
	}
	// A DocType that does not exist (or a child DocType) is 404, like the list.
	wantStatus(t, e.send(get("/api/v1/export/nope").session(exporter)), http.StatusNotFound)
	wantStatus(t, e.send(get("/api/v1/export/parcel_line").session(root)), http.StatusNotFound)
	// The administrator passes, and so does an API token of a user with the rights.
	wantStatus(t, e.trExport(root, ""), http.StatusOK)
	token := e.apiToken("exporter")
	wantStatus(t, e.send(get("/api/v1/export/parcel?format=json").token(token)), http.StatusOK)
	// Without the right, nothing was streamed: a normal JSON error envelope.
	rep := e.trExport(e.login("reader"), "?format=csv")
	if !strings.HasPrefix(rep.header.Get("Content-Type"), "application/json") {
		t.Error(rep.header)
	}
}

// Rust: transfer.rs::export_follows_if_owner
func TestExportFollowsIfOwner(t *testing.T) {
	e := trEnv(t)
	e.user("root2", "Administrator")
	root, other := e.login("root"), e.login("root2")
	e.trInsert(other, J{"title": "not mine"})
	// `own` is "Own Exporter": read and export on own documents; it cannot create, so the administrator
	// creates a document it owns by being it: use an Administrator named `ownroot`.
	e.user("ownroot", "Administrator", "Own Exporter")
	e.trInsert(root, J{"title": "root's"})
	rep := e.trExport(e.login("own"), "?fields=title")
	if !reflect.DeepEqual(csvRows(rep.text), [][]string{{"title"}}) || rep.header.Get("X-Forgebase-Row-Count") != "0" {
		t.Errorf("%q %v", rep.text, rep.header)
	}
	// An administrator is not limited by if_owner rules of its other roles.
	rep = e.trExport(e.login("ownroot"), "?fields=title&sort=title")
	if n := len(csvRows(rep.text)); n != 3 {
		t.Errorf("%d rows", n)
	}
}

// Rust: transfer.rs::csv_export_headers_and_line_ends
func TestCSVExportHeadersAndLineEnds(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.trInsert(root, J{"title": "a"})
	rep := e.trExport(root, "?format=csv")
	wantStatus(t, rep, http.StatusOK)
	if rep.header.Get("Content-Type") != "text/csv; charset=utf-8" || rep.header.Get("Cache-Control") != "no-store" {
		t.Error(rep.header)
	}
	disposition := rep.header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="parcel-`) || !strings.HasSuffix(disposition, `Z.csv"`) {
		t.Error(disposition)
	}
	if rep.header.Get("X-Forgebase-Row-Count") != "1" {
		t.Error(rep.header)
	}
	// No BOM, CRLF everywhere, a header of `name` and the fields (no child table).
	if strings.HasPrefix(rep.text, "\ufeff") {
		t.Error("BOM")
	}
	lines := strings.Split(rep.text, "\r\n")
	if len(lines) != 3 || lines[2] != "" || strings.Contains(strings.ReplaceAll(rep.text, "\r\n", ""), "\n") {
		t.Errorf("%q", rep.text)
	}
	if lines[0] != "name,title,note,qty,price,due,seen,done,tier,item" {
		t.Error(lines[0])
	}
	// The default format is CSV.
	if !strings.HasPrefix(e.trExport(root, "").header.Get("Content-Type"), "text/csv") {
		t.Error("default format")
	}
	// Not a format: 400.
	wantCode(t, e.trExport(root, "?format=xml"), http.StatusBadRequest, "invalid_query")
}

// Rust: transfer.rs::csv_cells_are_quoted_and_text_that_could_be_a_formula_is_prefixed
func TestCSVCellsAreQuotedAndTextThatCouldBeAFormulaIsPrefixed(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.adminInsert(root, "item", J{"code": "I-1"})
	for _, c := range [][2]string{
		{"comma, here", "x"}, {`say "hi"`, "x"}, {"two\nlines", "carriage\r\nreturn"}, {" lead", "trail "},
		{"héllo ✓ 日本", "x"}, {"=1+1", "+cmd"}, {"-5", "@SUM(A1)"}, {"\tTab", "\rCR"}, {"plain", "mid - dash"},
	} {
		e.trInsert(root, J{"title": c[0], "note": c[1]})
	}
	e.trInsert(root, J{"title": "numbers", "qty": -5, "price": "-1.50", "due": "2026-10-31",
		"seen": "2026-10-31T10:20:30Z", "done": false, "tier": "gold", "item": "I-1"})
	e.trInsert(root, J{"title": "zero", "price": "0.10", "done": true})
	rep := e.trExport(root, "?fields=title,note,qty,price,due,seen,done,tier,item&sort=title")
	wantStatus(t, rep, http.StatusOK)
	rows := csvRows(rep.text)
	byTitle := func(title string) []string {
		for _, r := range rows {
			if r[0] == title {
				return r
			}
		}
		t.Fatalf("no row %q in %q", title, rows)
		return nil
	}
	// Quoting round-trips through a real CSV reader.
	for title, note := range map[string]string{"comma, here": "x", `say "hi"`: "x", "two\nlines": "carriage\r\nreturn",
		" lead": "trail ", "héllo ✓ 日本": "x", "'=1+1": "'+cmd", "'-5": "'@SUM(A1)", "'\tTab": "'\rCR",
		"plain": "mid - dash"} {
		if got := byTitle(title)[1]; got != note {
			t.Errorf("%q: %q, want %q", title, got, note)
		}
	}
	// ... and the raw text quotes exactly what needs it.
	for _, want := range []string{"\"comma, here\",x,", "\"say \"\"hi\"\"\",x,", "\"two\nlines\",\"carriage\r\nreturn\",",
		"\" lead\",\"trail \",", "héllo ✓ 日本,x,"} {
		if !strings.Contains(rep.text, want) {
			t.Errorf("no %q in %q", want, rep.text)
		}
	}
	// Numbers, decimals, dates and booleans are never prefixed; null is empty.
	if got := byTitle("numbers")[1:]; !reflect.DeepEqual(got, []string{"", "-5", "-1.50", "2026-10-31", "2026-10-31T10:20:30Z", "false", "gold", "I-1"}) {
		t.Errorf("%q", got)
	}
	if got := byTitle("zero")[1:]; !reflect.DeepEqual(got, []string{"", "", "0.10", "", "", "true", "", ""}) {
		t.Errorf("%q", got)
	}
}

// Rust: transfer.rs::export_applies_filters_sort_and_fields
func TestExportAppliesFiltersSortAndFields(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	for _, c := range []struct {
		title string
		qty   int
	}{{"b", 2}, {"a", 5}, {"c", 2}, {"d", 9}} {
		e.trInsert(root, J{"title": c.title, "qty": c.qty})
	}
	filters := encQuery(`[{"field":"qty","op":"<","value":9}]`)
	rep := e.trExport(root, "?filters="+filters+"&sort=qty:desc,title&fields=title,qty")
	if got := csvRows(rep.text); !reflect.DeepEqual(got, [][]string{{"title", "qty"}, {"a", "5"}, {"b", "2"}, {"c", "2"}}) {
		t.Errorf("%q", got)
	}
	if rep.header.Get("X-Forgebase-Row-Count") != "3" {
		t.Error(rep.header)
	}
	// The standard columns can be named.
	if got := csvRows(e.trExport(root, "?fields=owner,version&filters=%5B%5D").text)[1]; !reflect.DeepEqual(got, []string{"root", "1"}) {
		t.Errorf("%q", got)
	}
	// Bad input is 400 invalid_query, before anything is sent.
	for _, query := range []string{"?fields=nope", "?fields=lines", "?fields=title,title", "?fields=",
		"?filters=%5B%7B%22field%22%3A%22nope%22%2C%22op%22%3A%22%3D%22%2C%22value%22%3A1%7D%5D", "?filters=notjson",
		"?sort=nope", "?limit=5", "?offset=1", "?format=csv&format=json"} {
		wantCode(t, e.trExport(root, query), http.StatusBadRequest, "invalid_query", query)
	}
	// A table field is fine in JSON.
	rep = e.trExport(root, "?format=json&fields=title,lines&sort=title")
	wantStatus(t, rep, http.StatusOK, rep.text)
	wantJSON(t, rep.obj().([]any)[0], `{"title": "a", "lines": []}`)
}

// Rust: transfer.rs::export_over_the_limit_is_422_with_the_count_and_the_limit
func TestExportOverTheLimitIs422WithTheCountAndTheLimit(t *testing.T) {
	config := DefaultConfig()
	config.ExportMaxRows = 3
	e := trEnvWith(t, config)
	root := e.login("root")
	for i := range 4 {
		e.trInsert(root, J{"title": fmt.Sprintf("p%d", i)})
	}
	rep := e.trExport(root, "")
	wantCode(t, rep, http.StatusUnprocessableEntity, "export_too_large")
	wantJSON(t, rep.errorObj()["details"], `{"count": 4, "limit": 3}`)
	// Narrowing the filters gets under the limit.
	filters := encQuery(`[{"field":"title","op":"!=","value":"p0"}]`)
	wantStatus(t, e.trExport(root, "?filters="+filters), http.StatusOK)
}

// Rust: transfer.rs::json_export_has_child_rows_and_streams_every_page
func TestJSONExportHasChildRowsAndStreamsEveryPage(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.trInsert(root, J{"title": "with lines", "lines": []J{{"what": "a"}, {"what": "b"}}})
	rep := e.trExport(root, "?format=json")
	wantStatus(t, rep, http.StatusOK)
	if rep.header.Get("Content-Type") != "application/json" || !strings.HasSuffix(rep.header.Get("Content-Disposition"), `Z.json"`) {
		t.Error(rep.header)
	}
	documents, ok := rep.obj().([]any)
	if !ok || len(documents) != 1 {
		t.Fatalf("a top-level array, not an envelope: %s", rep.text)
	}
	wantJSON(t, at(documents[0], "lines"), `[{"what": "a"}, {"what": "b"}]`)
	if str(documents[0], "title") != "with lines" {
		t.Error(documents[0])
	}
	// The same shape as GET of the document.
	one := e.send(get("/api/v1/resource/parcel/" + str(documents[0], "name")).session(root))
	wantJSON(t, one.data(), rep.text[1:len(rep.text)-3])
	// Empty: a valid empty array.
	e2 := trEnv(t)
	empty := e2.trExport(e2.login("root"), "?format=json")
	wantJSON(t, empty.obj(), `[]`)

	// More than two pages of 1000: complete, ordered, one array.
	e.db.Exec("INSERT INTO doc_parcel (id, name, owner, created_at, modified_at, modified_by, title) " +
		"SELECT gen_random_uuid(), 'S' || lpad(g::text, 5, '0'), 'seed', now(), now(), 'seed', 'T' || g FROM generate_series(1, 2500) g")
	big := e.trExport(root, "?format=json&sort=name")
	if n := len(big.obj().([]any)); n != 2501 || big.header.Get("X-Forgebase-Row-Count") != "2501" {
		t.Errorf("%d documents, count %s", n, big.header.Get("X-Forgebase-Row-Count"))
	}
	big = e.trExport(root, "?format=csv&fields=name&sort=name")
	if n := len(csvRows(big.text)); n != 2502 {
		t.Errorf("%d rows", n)
	}
}

// strip removes the metadata that an import does not take back.
func strip(d any) any {
	out := map[string]any{}
	for k, v := range d.(map[string]any) {
		switch k {
		case "id", "owner", "created_at", "modified_at", "modified_by", "version":
		default:
			out[k] = v
		}
	}
	return out
}

func stripAll(docs any) []any {
	var out []any
	for _, d := range docs.([]any) {
		out = append(out, strip(d))
	}
	return out
}

// Rust: transfer.rs::a_json_export_can_be_imported_again_and_gives_equal_documents
func TestAJSONExportCanBeImportedAgainAndGivesEqualDocuments(t *testing.T) {
	// Into another site (a fresh database): a DocType named by a field takes the given names.
	e := trEnv(t)
	root := e.login("root")
	for _, c := range []struct {
		code   string
		weight int
	}{{"T-1", 1}, {"T-2", 2}} {
		e.adminInsert(root, "tag", J{"code": c.code, "weight": c.weight})
	}
	exported := e.send(get("/api/v1/export/tag?format=json&sort=name").session(root))
	fresh := trEnv(t)
	importer := fresh.login("importer")
	rep := fresh.send(post("/api/v1/import/tag?format=json").session(importer).raw("application/json", []byte(exported.text)))
	wantStatus(t, rep, http.StatusOK, rep.text)
	if at(rep.data(), "inserted").(interface{ String() string }).String() != "2" {
		t.Error(rep.text)
	}
	again := fresh.send(get("/api/v1/export/tag?format=json&sort=name").session(fresh.login("root")))
	if !reflect.DeepEqual(stripAll(exported.obj()), stripAll(again.obj())) {
		t.Errorf("%s\n%s", exported.text, again.text)
	}
	wantJSON(t, stripAll(exported.obj())[1], `{"name": "T-2", "code": "T-2", "weight": 2}`)
}

// Rust: transfer.rs::a_json_export_of_a_randomly_named_doctype_upserts_back_to_equal_documents
func TestAJSONExportOfARandomlyNamedDoctypeUpsertsBackToEqualDocuments(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.adminInsert(root, "item", J{"code": "I-1"})
	e.trInsert(root, J{"title": "one", "note": "n", "qty": 3, "price": "1.50", "due": "2026-10-31",
		"seen": "2026-10-31T10:20:30Z", "done": true, "tier": "gold", "item": "I-1", "lines": []J{{"what": "a"}, {"what": "b"}}})
	e.trInsert(root, J{"title": "two", "lines": []J{{"what": "c"}}})
	exported := e.trExport(root, "?format=json&sort=title").text
	// Insert mode would give the random names: refused per row, nothing imported.
	rep := e.trImport(root, "?format=json", "application/json", exported)
	wantCode(t, rep, http.StatusUnprocessableEntity, "import_failed")
	first := at(rep.errorObj(), "details", "errors").([]any)[0]
	if !strings.Contains(at(first, "details").([]any)[0].(string), "random naming") {
		t.Error(rep.text)
	}
	// Upsert finds the documents by name and writes the same values back.
	rep = e.trImport(root, "?format=json&mode=upsert", "application/json", exported)
	wantStatus(t, rep, http.StatusOK, rep.text)
	if fmt.Sprintf("%v %v", at(rep.data(), "updated"), at(rep.data(), "inserted")) != "2 0" {
		t.Error(rep.text)
	}
	again := e.trExport(root, "?format=json&sort=title")
	before := parseForTest(t, exported)
	if !reflect.DeepEqual(stripAll(before), stripAll(again.obj())) {
		t.Errorf("%s\n%s", exported, again.text)
	}
	doc0 := again.obj().([]any)[0]
	wantJSON(t, at(doc0, "version"), `2`)
	wantJSON(t, at(doc0, "lines"), `[{"what": "a"}, {"what": "b"}]`)
}

func parseForTest(t *testing.T, text string) any {
	t.Helper()
	return reply{t: t, text: text}.obj()
}

// ---- import ----------------------------------------------------------------------------------------

// Rust: transfer.rs::import_needs_authentication_csrf_and_the_rights
func TestImportNeedsAuthenticationCSRFAndTheRights(t *testing.T) {
	e := trEnv(t)
	csv := "title\r\na\r\n"
	wantStatus(t, e.send(post("/api/v1/import/parcel?format=csv").raw("text/csv", []byte(csv))), http.StatusUnauthorized)
	importer := e.login("importer")
	// A cookie without the CSRF token is refused.
	wantCode(t, e.send(post("/api/v1/import/parcel?format=csv").cookie(importer).raw("text/csv", []byte(csv))),
		http.StatusForbidden, "csrf_failed")
	// No import right (but export, read, create elsewhere): 403 permission_denied.
	for _, user := range []string{"exporter", "reader", "exportonly", "own"} {
		wantCode(t, e.trImport(e.login(user), "?format=csv", "text/csv", csv), http.StatusForbidden, "permission_denied", user)
	}
	// Import without create.
	rep := e.trImport(e.login("nocreate"), "?format=csv", "text/csv", csv)
	wantStatus(t, rep, http.StatusForbidden)
	if !strings.Contains(str(rep.errorObj(), "message"), "create") {
		t.Error(rep.text)
	}
	// An upsert needs write too.
	rep = e.trImport(importer, "?format=csv&mode=upsert", "text/csv", "name,title\r\nx,a\r\n")
	wantStatus(t, rep, http.StatusForbidden)
	if !strings.Contains(str(rep.errorObj(), "message"), "write") {
		t.Error(rep.text)
	}
	// Import and create inserts.
	rep = e.trImport(importer, "?format=csv", "text/csv", csv)
	wantStatus(t, rep, http.StatusOK, rep.text)
	// An unknown DocType is 404, a child DocType too.
	wantStatus(t, e.send(post("/api/v1/import/nope?format=csv").session(importer).raw("text/csv", []byte(csv))), http.StatusNotFound)
}

// Rust: transfer.rs::import_checks_the_parameters_and_the_content_type
func TestImportChecksTheParametersAndTheContentType(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	csv := "title\r\na\r\n"
	for _, query := range []string{"", "?format=xml", "?format=csv&mode=replace", "?format=csv&dry_run=yes", "?format=csv&extra=1",
		"?format=csv&format=csv"} {
		wantCode(t, e.trImport(root, query, "text/csv", csv), http.StatusBadRequest, "invalid_query", query)
	}
	// The type must be that of the format.
	for _, c := range [][2]string{{"?format=csv", "application/json"}, {"?format=json", "text/csv"}, {"?format=csv", "text/plain"}} {
		wantCode(t, e.trImport(root, c[0], c[1], csv), http.StatusUnsupportedMediaType, "unsupported_media_type", c)
	}
	// No Content-Type at all.
	wantStatus(t, e.send(post("/api/v1/import/parcel?format=csv").session(root)), http.StatusUnsupportedMediaType)
	// Parameters of the media type and its case are fine; nothing was created by the refusals.
	rep := e.trImport(root, "?format=csv", "Text/CSV; charset=utf-8", csv)
	wantStatus(t, rep, http.StatusOK, rep.text)
	if got := e.trTitles(root); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("%v", got)
	}
}

// Rust: transfer.rs::a_valid_csv_import_creates_the_documents_and_runs_the_hooks
func TestAValidCSVImportCreatesTheDocumentsAndRunsTheHooks(t *testing.T) {
	e := trEnv(t)
	importer, root := e.login("importer"), e.login("root")
	e.adminInsert(root, "item", J{"code": "I-1"})
	file := "\ufefftitle,note,qty,price,due,seen,done,tier,item\r\n" +
		"one,\"a, b\",3,1.50,2026-10-31,2026-10-31T10:20:30Z,true,gold,I-1\r\n" +
		"hook-me,,,,,,0,,\r\n" +
		"=danger,\"line\r\nbreak\",-7,-0.25,,,,silver,\r\n"
	rep := e.trImport(importer, "?format=csv", "text/csv", file)
	wantStatus(t, rep, http.StatusOK, rep.text)
	wantJSON(t, rep.data(), `{"dry_run": false, "mode": "insert", "rows": 3, "inserted": 3, "updated": 0, "errors": [], "error_count": 0}`)
	listed := e.send(get("/api/v1/resource/parcel?sort=title&limit=50").session(root)).dataList()
	find := func(title string) any {
		for _, d := range listed {
			if str(d, "title") == title {
				return d
			}
		}
		t.Fatalf("no %q", title)
		return nil
	}
	one := find("one")
	wantJSON(t, at(one, "note"), `"a, b"`)
	wantJSON(t, at(one, "qty"), `3`)
	wantJSON(t, at(one, "price"), `"1.50"`)
	wantJSON(t, at(one, "done"), `true`)
	wantJSON(t, at(one, "item"), `"I-1"`)
	wantJSON(t, at(one, "owner"), `"importer"`)
	// The hook ran (note set), and an empty cell was left out.
	hooked := find("hook-me")
	wantJSON(t, at(hooked, "note"), `"hooked"`)
	wantJSON(t, at(hooked, "done"), `false`)
	wantJSON(t, at(hooked, "qty"), `null`)
	// CSV import does not strip the ' an export adds, and keeps a formula-looking text as is.
	danger := find("=danger")
	wantJSON(t, at(danger, "note"), `"line\r\nbreak"`)
	wantJSON(t, at(danger, "qty"), `-7`)
}

// Rust: transfer.rs::a_dry_run_reports_and_creates_nothing
func TestADryRunReportsAndCreatesNothing(t *testing.T) {
	e := trEnv(t)
	importer, root := e.login("importer"), e.login("root")
	rep := e.trImport(importer, "?format=csv&dry_run=1", "text/csv", "title,qty\r\na,1\r\nb,2\r\n")
	wantStatus(t, rep, http.StatusOK, rep.text)
	wantJSON(t, rep.data(), `{"dry_run": true, "mode": "insert", "rows": 2, "inserted": 2, "updated": 0, "errors": [], "error_count": 0}`)
	if len(e.trTitles(root)) != 0 {
		t.Error("a dry run created documents")
	}
	// With errors a dry run is still a 200 with the list; and still nothing is created.
	rep = e.trImport(importer, "?format=csv&dry_run=1", "text/csv", "title,qty\r\na,1\r\nreject,2\r\n,3\r\n")
	wantStatus(t, rep, http.StatusOK)
	wantJSON(t, at(rep.data(), "error_count"), `2`)
	if len(e.trTitles(root)) != 0 {
		t.Error("a dry run created documents")
	}
}

// Rust: transfer.rs::row_errors_are_listed_with_their_rows_and_nothing_is_imported
func TestRowErrorsAreListedWithTheirRowsAndNothingIsImported(t *testing.T) {
	e := trEnv(t)
	importer, root := e.login("importer"), e.login("root")
	file := "title,qty,due,item\r\nok,1,2026-01-01,\r\nreject,1,,\r\n,2,,\r\nbad,x,31/10/2026,\r\nlinked,1,,no-such-item\r\nfine,1,,\r\n"
	rep := e.trImport(importer, "?format=csv", "text/csv", file)
	wantCode(t, rep, http.StatusUnprocessableEntity, "import_failed", rep.text)
	details := rep.errorObj()["details"]
	wantJSON(t, at(details, "error_count"), `4`)
	wantJSON(t, at(details, "rows"), `6`)
	wantJSON(t, at(details, "dry_run"), `false`)
	errs := at(details, "errors").([]any)
	var rows []string
	for _, x := range errs {
		rows = append(rows, fmt.Sprint(at(x, "row")))
	}
	if strings.Join(rows, ",") != "2,3,4,5" {
		t.Errorf("%v", rows)
	}
	// A hook refusal, a missing required field, a value that cannot be parsed, an unknown link.
	wantJSON(t, at(errs[0], "code"), `"hook_rejected"`)
	if !strings.Contains(str(errs[0], "message"), "rejected by the app") {
		t.Error(errs[0])
	}
	wantJSON(t, at(errs[0], "line"), `3`)
	wantJSON(t, at(errs[1], "code"), `"validation_failed"`)
	if !strings.Contains(at(errs[1], "details").([]any)[0].(string), "title") {
		t.Error(errs[1])
	}
	wantJSON(t, at(errs[2], "code"), `"validation_failed"`)
	d2 := at(errs[2], "details").([]any)
	if len(d2) != 2 || !strings.Contains(d2[0].(string), "qty") || !strings.Contains(d2[1].(string), "due") {
		t.Error(d2)
	}
	wantJSON(t, at(errs[3], "code"), `"validation_failed"`)
	if !strings.Contains(at(errs[3], "details").([]any)[0].(string), "no-such-item") {
		t.Error(errs[3])
	}
	// Nothing from the good rows was created.
	if len(e.trTitles(root)) != 0 {
		t.Error("something was created")
	}
}

// Rust: transfer.rs::a_file_with_bad_columns_is_refused_before_anything_runs
func TestAFileWithBadColumnsIsRefusedBeforeAnythingRuns(t *testing.T) {
	e := trEnv(t)
	importer := e.login("importer")
	refused := func(rep reply) string {
		t.Helper()
		wantCode(t, rep, http.StatusUnprocessableEntity, "invalid_import", rep.text)
		return fmt.Sprint(rep.errorObj()["details"])
	}
	if got := refused(e.trImport(importer, "?format=csv", "text/csv", "title,color\r\na,red\r\n")); !strings.Contains(got, "unknown column `color`") {
		t.Error(got)
	}
	if got := refused(e.trImport(importer, "?format=csv", "text/csv", "title,title\r\na,b\r\n")); !strings.Contains(got, "duplicate column `title`") {
		t.Error(got)
	}
	got := refused(e.trImport(importer, "?format=csv", "text/csv", "title,lines\r\na,b\r\n"))
	if !strings.Contains(got, "child tables is not supported") || !strings.Contains(got, "use JSON") {
		t.Error(got)
	}
	refused(e.trImport(importer, "?format=csv", "text/csv", "title,qty\r\na\r\n"))
	refused(e.trImport(importer, "?format=json", "application/json", `{"title": "x"}`))
	refused(e.trImport(importer, "?format=json", "application/json", "[{"))
	got = refused(e.send(post("/api/v1/import/parcel?format=csv").session(importer).raw("text/csv", []byte("title\r\n\xff\xfe\r\n"))))
	if !strings.Contains(got, "UTF-8") {
		t.Error(got)
	}
	if len(e.trTitles(e.login("root"))) != 0 {
		t.Error("something was created")
	}
}

// Rust: transfer.rs::the_row_limit_and_the_body_limit_are_the_imports_own
func TestTheRowLimitAndTheBodyLimitAreTheImportsOwn(t *testing.T) {
	config := DefaultConfig()
	config.ImportMaxRows, config.ImportBodyLimitBytes, config.BodyLimitBytes = 3, 400, 100
	e := trEnvWith(t, config)
	importer, root := e.login("importer"), e.login("root")
	// More rows than the limit: 422 before anything runs.
	rep := e.trImport(importer, "?format=csv", "text/csv", "title\r\na\r\nb\r\nc\r\nd\r\n")
	wantCode(t, rep, http.StatusUnprocessableEntity, "import_too_large")
	wantJSON(t, rep.errorObj()["details"], `{"limit": 3}`)
	rep = e.trImport(importer, "?format=json", "application/json", `[{"title":"a"},{"title":"b"},{"title":"c"},{"title":"d"}]`)
	wantCode(t, rep, http.StatusUnprocessableEntity, "import_too_large")
	// At the limit it passes.
	rep = e.trImport(importer, "?format=csv", "text/csv", "title\r\na\r\nb\r\nc\r\n")
	wantStatus(t, rep, http.StatusOK, rep.text)
	// A body over the import limit is 413; one over the ordinary limit but under the import limit is fine,
	// while the other routes keep the ordinary limit.
	long := "title\r\n" + strings.Repeat("x", 300) + "\r\n"
	rep = e.trImport(importer, "?format=csv&dry_run=1", "text/csv", long)
	wantStatus(t, rep, http.StatusOK, rep.text)
	huge := "title\r\n" + strings.Repeat("x", 500) + "\r\n"
	wantCode(t, e.trImport(importer, "?format=csv", "text/csv", huge), http.StatusRequestEntityTooLarge, "payload_too_large")
	wantStatus(t, e.send(post("/api/v1/resource/parcel").session(root).json(J{"title": strings.Repeat("x", 300)})),
		http.StatusRequestEntityTooLarge)
}

// Rust: transfer.rs::upsert_updates_existing_documents_and_inserts_new_ones
func TestUpsertUpdatesExistingDocumentsAndInsertsNewOnes(t *testing.T) {
	e := trEnv(t)
	upserter, root := e.login("upserter"), e.login("root")
	existing := e.trInsert(root, J{"title": "old", "qty": 1, "note": "keep"})
	name := str(existing, "name")
	// The existing name updates (an empty cell clears).
	rep := e.trImport(upserter, "?format=csv&mode=upsert", "text/csv", fmt.Sprintf("name,title,qty,note\r\n%s,new,,\r\n", name))
	wantStatus(t, rep, http.StatusOK, rep.text)
	wantJSON(t, at(rep.data(), "mode"), `"upsert"`)
	if fmt.Sprintf("%v %v", at(rep.data(), "updated"), at(rep.data(), "inserted")) != "1 0" {
		t.Error(rep.text)
	}
	now := e.send(get("/api/v1/resource/parcel/" + name).session(root)).data()
	wantJSON(t, at(now, "title"), `"new"`)
	wantJSON(t, at(now, "qty"), `null`) // an empty cell sets the field to null
	wantJSON(t, at(now, "note"), `null`)
	wantJSON(t, at(now, "version"), `2`)
	// An upsert file needs a name column.
	wantCode(t, e.trImport(upserter, "?format=csv&mode=upsert", "text/csv", "title\r\nx\r\n"), http.StatusUnprocessableEntity, "invalid_import")
	// A JSON upsert replaces the rows it is given.
	body := fmt.Sprintf(`[{"name": %q, "lines": [{"what": "z"}]}]`, name)
	rep = e.trImport(upserter, "?format=json&mode=upsert", "application/json", body)
	wantStatus(t, rep, http.StatusOK, rep.text)
	now = e.send(get("/api/v1/resource/parcel/" + name).session(root)).data()
	wantJSON(t, at(now, "lines"), `[{"what": "z"}]`)
	wantJSON(t, at(now, "title"), `"new"`)
	// A name given where naming is automatic is a row error in insert mode too.
	rep = e.trImport(upserter, "?format=csv", "text/csv", "name,title\r\nmine,x\r\n")
	wantCode(t, rep, http.StatusUnprocessableEntity, "import_failed")
	wantJSON(t, at(rep.errorObj(), "details", "errors").([]any)[0].(map[string]any)["name"], `"mine"`)
}

// ---- report export ---------------------------------------------------------------------------------

func trReport(name string, maxRows uint32) *core.ReportDef {
	return &core.ReportDef{App: "t", Name: name, Label: "Parcels", SQL: "SELECT title, qty, note, due FROM doc_parcel ORDER BY title",
		Roles: []string{"Report Reader"},
		Columns: []core.ReportColumn{
			{Name: "title", Label: "Title", Kind: core.ColumnKind{Kind: core.ColumnData}},
			{Name: "qty", Label: "Qty", Kind: core.ColumnKind{Kind: core.ColumnInt}},
			{Name: "note", Label: "Note", Kind: core.ColumnKind{Kind: core.ColumnData}},
			{Name: "due", Label: "Due", Kind: core.ColumnKind{Kind: core.ColumnDate}},
		},
		Timeout: core.DefaultTimeout, MaxRows: maxRows}
}

// Rust: transfer.rs::a_report_exports_for_its_roles_with_the_same_csv_rules_and_marks_a_truncated_result
func TestAReportExportsForItsRolesWithTheSameCSVRulesAndMarksATruncatedResult(t *testing.T) {
	registry := testRegistry(t, trParcel, trParcelLine)
	for _, r := range []*core.ReportDef{trReport("parcels", 2), trReport("all_parcels", core.DefaultMaxRows)} {
		if !registry.AddReport(r) {
			t.Fatal("duplicate")
		}
	}
	e := newEnvRegistry(t, DefaultConfig(), registry)
	e.user("rr", "Report Reader")
	e.user("nobody", "Reader")
	e.user("root", "Administrator")
	root := e.login("root")
	for _, c := range []struct {
		title string
		qty   int
		note  string
	}{{"a", 1, "=1+1"}, {"b", -5, "x, y"}, {"c", 3, "z"}} {
		e.adminInsert(root, "parcel", J{"title": c.title, "qty": c.qty, "note": c.note})
	}
	rr := e.login("rr")
	url := "/api/v1/report/t.parcels/export"
	run := func(query string) reply {
		return e.send(post(url + query).session(rr).json(J{"params": J{}}))
	}
	// The role gate: a user without the role gets the same 404 as for a report that is not there.
	wantStatus(t, e.send(post(url+"?format=csv").session(e.login("nobody")).json(J{})), http.StatusNotFound)
	wantStatus(t, e.send(post("/api/v1/report/t.nope/export").session(rr).json(J{})), http.StatusNotFound)
	wantStatus(t, e.send(post(url+"?format=csv").json(J{})), http.StatusUnauthorized)

	// Cut at the report's own row limit (2): marked, and the file has two rows.
	rep := run("?format=csv")
	wantStatus(t, rep, http.StatusOK, rep.text)
	if rep.header.Get("Content-Type") != "text/csv; charset=utf-8" || rep.header.Get("X-Forgebase-Truncated") != "true" ||
		!strings.Contains(rep.header.Get("Content-Disposition"), "t.parcels-") {
		t.Error(rep.header)
	}
	want := [][]string{{"title", "qty", "note", "due"}, {"a", "1", "'=1+1", ""}, {"b", "-5", "x, y", ""}}
	if got := csvRows(rep.text); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
	rep = run("?format=json")
	if rep.header.Get("X-Forgebase-Truncated") != "true" {
		t.Error(rep.header)
	}
	body := rep.obj()
	wantJSON(t, at(body, "truncated"), `true`)
	wantJSON(t, at(body, "rows"), `[["a", 1, "=1+1", null], ["b", -5, "x, y", null]]`)
	wantJSON(t, at(body, "columns").([]any)[0], `{"name": "title", "label": "Title", "kind": "data"}`)
	// A report within its limit has no marker.
	rep = e.send(post("/api/v1/report/t.all_parcels/export?format=csv").session(root).json(J{}))
	wantStatus(t, rep, http.StatusOK)
	if _, has := rep.header["X-Forgebase-Truncated"]; has || len(csvRows(rep.text)) != 4 {
		t.Errorf("%v %q", rep.header, rep.text)
	}
	// The parameters are checked like a run: an unknown one is a 422.
	rep = e.send(post(url + "?format=csv").session(rr).json(J{"params": J{"nope": 1}}))
	wantStatus(t, rep, http.StatusUnprocessableEntity, rep.text)
	wantStatus(t, run("?format=pdf"), http.StatusBadRequest)
}

// ---- limits of the echo ----------------------------------------------------------------------------

// Rust: transfer.rs::a_header_with_a_hundred_thousand_columns_gets_a_short_answer
func TestAHeaderWithAHundredThousandColumnsGetsAShortAnswer(t *testing.T) {
	e := trEnv(t)
	importer := e.login("importer")
	file := strings.Repeat("a,", 99_999) + "a\r\nx\r\n"
	started := time.Now()
	rep := e.trImport(importer, "?format=csv", "text/csv", file)
	wantCode(t, rep, http.StatusUnprocessableEntity, "invalid_import")
	if len(rep.text) >= 2000 || time.Since(started) > 5*time.Second {
		t.Errorf("%d bytes in %v", len(rep.text), time.Since(started))
	}
	// The same with a few more columns than the DocType can have, none of them known.
	var names []string
	for i := range 60 {
		names = append(names, fmt.Sprintf("c%d", i))
	}
	rep = e.trImport(importer, "?format=csv", "text/csv", strings.Join(names, ",")+"\r\n")
	wantCode(t, rep, http.StatusUnprocessableEntity, "invalid_import")
	if n := len(rep.errorObj()["details"].([]any)); n > 101 {
		t.Errorf("%d details", n)
	}
}

// Rust: transfer.rs::a_csv_with_the_standard_columns_of_an_export_imports
func TestACSVWithTheStandardColumnsOfAnExportImports(t *testing.T) {
	e := trEnv(t)
	importer, root := e.login("importer"), e.login("root")
	file := "title,owner,modified_at,version,docstatus\r\nfrom export,ann,2026-01-01T00:00:00Z,4,0\r\n"
	rep := e.trImport(importer, "?format=csv", "text/csv", file)
	wantStatus(t, rep, http.StatusOK, rep.text)
	if got := e.trTitles(root); !reflect.DeepEqual(got, []string{"from export"}) {
		t.Errorf("%v", got)
	}
	owner := e.send(get("/api/v1/resource/parcel?limit=5").session(root)).dataList()[0]
	wantJSON(t, at(owner, "owner"), `"importer"`) // the owner column is read, not used
}

// Rust: transfer.rs::child_rows_count_toward_the_row_limit
func TestHTTPChildRowsCountTowardTheRowLimit(t *testing.T) {
	config := DefaultConfig()
	config.ImportMaxRows = 4
	e := trEnvWith(t, config)
	// 2 documents and 3 child rows are 5 rows.
	body := `[{"title":"a","lines":[{"what":"1"},{"what":"2"}]},{"title":"b","lines":[{"what":"3"}]}]`
	wantCode(t, e.trImport(e.login("importer"), "?format=json", "application/json", body), http.StatusUnprocessableEntity, "import_too_large")
}

// Rust: transfer.rs::a_field_called_workflow_state_on_a_doctype_without_a_workflow_is_kept
func TestAFieldCalledWorkflowStateOnADoctypeWithoutAWorkflowIsKept(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	e.adminInsert(root, "stateful", J{"title": "a", "workflow_state": "open"})
	// The export carries the value, in CSV and in JSON.
	rep := e.send(get("/api/v1/export/stateful?fields=title,workflow_state").session(root))
	if got := csvRows(rep.text); !reflect.DeepEqual(got, [][]string{{"title", "workflow_state"}, {"a", "open"}}) {
		t.Errorf("%q", got)
	}
	rep = e.send(get("/api/v1/export/stateful?format=json").session(root))
	wantJSON(t, at(rep.obj().([]any)[0], "workflow_state"), `"open"`)
	// The import sets it, from CSV and from JSON.
	importer := e.login("importer")
	rep = e.send(post("/api/v1/import/stateful?format=csv").session(importer).raw("text/csv", []byte("title,workflow_state\r\nb,closed\r\n")))
	wantStatus(t, rep, http.StatusOK, rep.text)
	rep = e.send(post("/api/v1/import/stateful?format=json").session(importer).raw("application/json",
		[]byte(`[{"title":"c","workflow_state":"waiting"}]`)))
	wantStatus(t, rep, http.StatusOK, rep.text)
	var states []string
	for _, d := range e.send(get("/api/v1/resource/stateful?sort=title").session(root)).dataList() {
		states = append(states, str(d, "workflow_state"))
	}
	if !reflect.DeepEqual(states, []string{"open", "closed", "waiting"}) {
		t.Errorf("%v", states)
	}
}

// Rust: transfer.rs::an_echoed_value_is_shortened_whatever_the_size_of_the_input
func TestAnEchoedValueIsShortenedWhateverTheSizeOfTheInput(t *testing.T) {
	config := DefaultConfig()
	e := trEnvWith(t, config)
	importer := e.login("importer")
	junk := strings.Repeat("\x01", 4*1024*1024)
	// A header, and cells of an integer, a date and a select field, each of 4 MiB of control bytes.
	for _, file := range []string{junk + "\r\nx\r\n", "title,qty,due,tier\r\na," + junk + "," + junk + "," + junk + "\r\n"} {
		started := time.Now()
		rep := e.trImport(importer, "?format=csv", "text/csv", file)
		wantStatus(t, rep, http.StatusUnprocessableEntity)
		if len(rep.text) >= 16_000 || time.Since(started) > 10*time.Second {
			t.Errorf("%d bytes in %v", len(rep.text), time.Since(started))
		}
	}
	// JSON values and keys.
	body := marshalForTest(t, []J{{"title": "a", "due": junk, "price": junk, "tier": junk, junk: 1}})
	rep := e.trImport(importer, "?format=json", "application/json", body)
	if len(rep.text) >= 16_000 {
		t.Errorf("%d bytes", len(rep.text))
	}
}

func marshalForTest(t *testing.T, v any) string {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Rust: transfer.rs::the_name_of_an_error_entry_is_shortened_too
func TestTheNameOfAnErrorEntryIsShortenedToo(t *testing.T) {
	e := trEnv(t)
	importer := e.login("importer")
	junk := strings.Repeat("\x01", 2*1024*1024)
	check := func(rep reply) {
		t.Helper()
		wantStatus(t, rep, http.StatusUnprocessableEntity, rep.text[:min(200, len(rep.text))])
		if len(rep.text) >= 16_000 {
			t.Errorf("%d bytes", len(rep.text))
		}
	}
	// A CSV name column, a JSON name key, and the value of a naming field.
	check(e.trImport(importer, "?format=csv", "text/csv", "name,title,qty\r\n"+junk+",a,x\r\n"))
	check(e.trImport(importer, "?format=json", "application/json", marshalForTest(t, []J{{"name": junk, "title": "a", "qty": "x"}})))
	check(e.send(post("/api/v1/import/tag?format=csv").session(importer).raw("text/csv", []byte("code,weight\r\n"+junk+",x\r\n"))))
	// A name that the database refuses (random naming) is shortened as well.
	rep := e.trImport(importer, "?format=csv", "text/csv", "name,title\r\n"+junk+",a\r\n")
	check(rep)
	entry := at(rep.errorObj(), "details", "errors").([]any)[0]
	if n := len([]rune(str(entry, "name"))); n > 65 {
		t.Errorf("%d characters", n)
	}
}

// Rust: transfer.rs::workflow_state_is_ignored_on_import_for_a_doctype_with_a_workflow
func TestWorkflowStateIsIgnoredOnImportForADoctypeWithAWorkflow(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	post0 := func(query, contentType, body string) reply {
		return e.send(post("/api/v1/import/purchase"+query).session(root).raw(contentType, []byte(body)))
	}
	rep := post0("?format=csv", "text/csv", "title,amount,workflow_state\r\nA,1,Done\r\n")
	wantStatus(t, rep, http.StatusOK, rep.text)
	rep = post0("?format=json", "application/json", `[{"title":"B","workflow_state":"Done","docstatus":1}]`)
	wantStatus(t, rep, http.StatusOK, rep.text)
	listed := e.send(get("/api/v1/resource/purchase?sort=title").session(root)).dataList()
	if len(listed) != 2 {
		t.Fatalf("%d documents", len(listed))
	}
	for _, doc := range listed {
		wantJSON(t, at(doc, "workflow_state"), `"Draft"`)
		wantJSON(t, at(doc, "docstatus"), `0`)
	}
	// An upsert updates the other fields and leaves the state alone.
	name := str(listed[0], "name")
	rep = post0("?format=csv&mode=upsert", "text/csv", fmt.Sprintf("name,amount,workflow_state\r\n%s,9,Done\r\n", name))
	wantStatus(t, rep, http.StatusOK, rep.text)
	wantJSON(t, at(rep.data(), "updated"), `1`)
	after := e.send(get("/api/v1/resource/purchase/" + name).session(root)).data()
	wantJSON(t, at(after, "amount"), `9`)
	wantJSON(t, at(after, "workflow_state"), `"Draft"`)
}

// Rust: transfer.rs::request_body_input_is_not_echoed_in_full_outside_import_and_export
func TestRequestBodyInputIsNotEchoedInFullOutsideImportAndExport(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	key := strings.Repeat("k", 900*1024)
	small := func(rep reply) {
		t.Helper()
		if len(rep.text) >= 16_000 {
			t.Errorf("%d bytes", len(rep.text))
		}
	}
	small(e.send(post("/api/v1/resource/parcel").session(root).json(J{"title": "a", key: 1})))
	// object() of the lifecycle routes and the login body.
	rep := e.send(post("/api/v1/login").json(J{"username": "a", "password": "b", key: 1}))
	wantStatus(t, rep, http.StatusBadRequest)
	small(rep)
	// A saved view with a huge filter field, key and operator, and a huge sort.
	for _, filter := range []J{{"field": key, "op": "=", "value": 1}, {"field": "title", "op": key, "value": 1}, {"field": "title", key: 1}} {
		small(e.send(post("/api/v1/views/parcel").session(root).json(J{"label": "v", "definition": J{"filters": []J{filter}}})))
	}
	small(e.send(get("/api/v1/resource/parcel?sort=" + strings.Repeat("s", 7000)).session(root)))
}

// Rust: transfer.rs::every_error_response_is_small_whatever_the_request_echoed
func TestEveryErrorResponseIsSmallWhateverTheRequestEchoed(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	// The answer to anything is under 100 KB (the budget is 64 KiB plus the envelope).
	small := func(what string, rep reply) {
		t.Helper()
		if len(rep.text) >= 100_000 {
			t.Errorf("%s: %d bytes", what, len(rep.text))
		}
	}
	// A dry run answers 200 with the errors of its rows, an import 422.
	expected := func(query string) int {
		if strings.Contains(query, "dry_run") {
			return http.StatusOK
		}
		return http.StatusUnprocessableEntity
	}
	// A JSON import of one row with about a million unknown keys, top level and in a child row, as an import
	// and as a dry run (the 200 summary lists the errors of its rows too).
	var keys strings.Builder
	for i := range 1_000_000 {
		fmt.Fprintf(&keys, `"k%d":1,`, i)
	}
	for _, c := range [][2]string{{"import", "?format=json"}, {"dry run", "?format=json&dry_run=1"}} {
		what, query := c[0], c[1]
		rep := e.trImport(root, query, "application/json", `[{`+keys.String()+`"title":"a"}]`)
		wantStatus(t, rep, expected(query), what, len(rep.text))
		small(what+", top level", rep)
		rep = e.trImport(root, query, "application/json", `[{"title":"a","lines":[{`+keys.String()+`"what":"x"}]}]`)
		wantStatus(t, rep, expected(query), what, len(rep.text))
		small(what+", child row", rep)
	}
	// 100 rows with 1000 unknown keys each, and 100 rows with 101 keys of 4-byte characters.
	row := func(key func(int) string, n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, `"%s":1,`, key(i))
		}
		return `{` + b.String() + `"title":"a"}`
	}
	plain := row(func(i int) string { return fmt.Sprintf("k%d", i) }, 1000)
	wide := row(func(i int) string { return strings.Repeat("\U0001D11E", 40) + fmt.Sprint(i) }, 101)
	for _, c := range [][2]string{{"1000 keys", plain}, {"4-byte keys", wide}} {
		body := "[" + strings.TrimSuffix(strings.Repeat(c[1]+",", 100), ",") + "]"
		for _, query := range []string{"?format=json", "?format=json&dry_run=1"} {
			rep := e.trImport(root, query, "application/json", body)
			wantStatus(t, rep, expected(query), c[0], len(rep.text))
			small("100 rows of "+c[0]+query, rep)
		}
	}
	// A CSV of 100 rows with 7 bad cells of 10 KB of control bytes each.
	junk := strings.Repeat("\x01", 10_000)
	sevenJunk := strings.TrimSuffix(strings.Repeat(junk+",", 7), ",")
	csv := "title,qty,price,due,seen,done,tier,item\n" + strings.TrimSuffix(strings.Repeat("a,"+sevenJunk+"\n", 100), "\n")
	var upsert strings.Builder
	upsert.WriteString("name,title,qty,price,due,seen,done,tier,item\n")
	for i := range 100 {
		if i > 0 {
			upsert.WriteByte('\n')
		}
		fmt.Fprintf(&upsert, "u%d,a,%s", i, sevenJunk)
	}
	for _, c := range []struct {
		query, csv, mode string
		status           int
	}{
		{"?format=csv", csv, "insert", http.StatusUnprocessableEntity},
		{"?format=csv&dry_run=1", csv, "insert", http.StatusOK},
		{"?format=csv&mode=upsert", upsert.String(), "upsert", http.StatusUnprocessableEntity},
		{"?format=csv&mode=upsert&dry_run=1", upsert.String(), "upsert", http.StatusOK},
	} {
		rep := e.trImport(root, c.query, "text/csv", c.csv)
		wantStatus(t, rep, c.status, rep.text[:min(300, len(rep.text))])
		if len(rep.text) <= 10_000 {
			t.Errorf("the errors are reported: %d bytes", len(rep.text))
		}
		small("bad CSV cells"+c.query, rep)
		if len(rep.text) >= 66*1024 {
			t.Errorf("%s: %d bytes", c.query, len(rep.text))
		}
		// The mode and the counts survive the bound, and a row error keeps its code and message.
		summary := at(rep.obj(), "data")
		if c.status != http.StatusOK {
			summary = at(rep.obj(), "error", "details")
		}
		wantJSON(t, at(summary, "mode"), `"`+c.mode+`"`)
		wantJSON(t, at(summary, "rows"), `100`)
		wantJSON(t, at(summary, "error_count"), `100`)
		if _, ok := at(summary, "errors").([]any)[0].(map[string]any)["message"].(string); !ok {
			t.Errorf("%s: no message", c.query)
		}
	}
	// The same through POST /resource, within its body limit.
	var keys2 strings.Builder
	for i := range 60_000 {
		fmt.Fprintf(&keys2, `"k%d":1,`, i)
	}
	rep := e.send(post("/api/v1/resource/parcel").session(root).raw("application/json", []byte(`{`+keys2.String()+`"title":"a"}`)))
	wantStatus(t, rep, http.StatusUnprocessableEntity)
	small("create, top level", rep)
	rep = e.send(post("/api/v1/resource/parcel").session(root).raw("application/json", []byte(`{"title":"a","lines":[{`+keys2.String()+`"what":"x"}]}`)))
	small("create, child row", rep)
	// A transition with a huge action.
	name := e.adminInsert(root, "purchase", J{"title": "p"})
	rep = e.send(post("/api/v1/resource/purchase/" + name + "/transition").session(root).json(J{"action": strings.Repeat("a", 900*1024), "version": 1}))
	wantStatus(t, rep, http.StatusConflict)
	small("transition", rep)
	// A huge name in the path.
	rep = e.send(get("/api/v1/resource/parcel/" + strings.Repeat("n", 60_000)).session(root))
	wantStatus(t, rep, http.StatusNotFound)
	small("path name", rep)
}

// ---- streaming -------------------------------------------------------------------------------------

// trBigExport is an environment with 60 000 parcels of 2 KB each (over 100 MB of CSV), served by a real
// server, so that a client can stop reading.
func trBigExport(t *testing.T, timeout time.Duration) (*env, *httptest.Server, session) {
	t.Helper()
	config := DefaultConfig()
	config.ExportSendTimeout = timeout
	e := trEnvWith(t, config)
	root := e.login("root")
	e.db.Exec("INSERT INTO doc_parcel (id, name, owner, created_at, modified_at, modified_by, title, note) " +
		"SELECT gen_random_uuid(), 'B' || lpad(g::text, 6, '0'), 'seed', now(), now(), 'seed', 'T' || g, repeat('x', 2000) " +
		"FROM generate_series(1, 60000) g")
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	return e, srv, root
}

// trRawExport sends the export request on a connection of its own and returns the connection and a reader
// that has read the response head.
func trRawExport(t *testing.T, srv *httptest.Server, root session) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET /api/v1/export/parcel?fields=title,note HTTP/1.1\r\nHost: localhost\r\nCookie: fb_session=%s\r\n\r\n", root.token)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("%q %v", status, err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			return conn, r
		}
	}
}

func trWaitReleased(t *testing.T, e *env, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e.state.pool.Stat().AcquiredConns() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the export still holds %d connection(s)", e.state.pool.Stat().AcquiredConns())
}

// A client that never reads: the socket buffers fill and the next chunk gives up after the timeout, so the
// export can end and release its connection and snapshot; the body is aborted, not ended.
//
// Rust: transfer_routes.rs::tests::a_chunk_for_a_client_that_stops_reading_times_out
func TestAChunkForAClientThatStopsReadingTimesOut(t *testing.T) {
	e, srv, root := trBigExport(t, 300*time.Millisecond)
	conn, r := trRawExport(t, srv, root)
	// Not read: the server writes until the buffers are full and then waits for the client.
	trWaitReleased(t, e, 30*time.Second)
	// What the client got is an incomplete body: the connection ends without the last chunk of the body.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	data, _ := io.ReadAll(r)
	if len(data) == 0 || strings.HasSuffix(string(data), "\r\n0\r\n\r\n") {
		t.Errorf("the body was ended, not aborted (%d bytes)", len(data))
	}
}

// Rust: transfer_routes.rs::tests::a_chunk_for_a_client_that_went_away_fails_at_once
func TestAChunkForAClientThatWentAwayFailsAtOnce(t *testing.T) {
	e, srv, root := trBigExport(t, 30*time.Second)
	conn, _ := trRawExport(t, srv, root)
	_ = conn.Close()
	trWaitReleased(t, e, 20*time.Second)
}

// Rust: a JSON row with "name": "" gives the name Some(""): a naming error (the name of a randomly named DocType
// cannot be given), in insert mode and in upsert mode (no document has that name, so it is an insert), and the
// error entry has the name "" (not null).
func TestAnEmptyJSONNameIsAGivenNameAndANamingError(t *testing.T) {
	e := trEnv(t)
	root := e.login("root")
	for _, query := range []string{"?format=json", "?format=json&mode=upsert"} {
		rep := e.trImport(root, query, "application/json", `[{"name":"","title":"x"}]`)
		wantCode(t, rep, http.StatusUnprocessableEntity, "import_failed", query)
		entry := at(rep.errorObj(), "details", "errors").([]any)[0].(map[string]any)
		name, isString := entry["name"].(string)
		if !isString || name != "" || !strings.Contains(at(entry, "details").([]any)[0].(string), "random naming") {
			t.Errorf("%s: %v", query, entry)
		}
	}
	// A row without a name still has a null name in its error.
	rep := e.trImport(root, "?format=json", "application/json", `[{"qty":"x"}]`)
	wantJSON(t, at(rep.errorObj(), "details", "errors").([]any)[0].(map[string]any)["name"], `null`)
	if len(e.trTitles(root)) != 0 {
		t.Error("something was created")
	}
}
