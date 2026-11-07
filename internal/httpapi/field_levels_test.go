package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Ported from crates/forgebase-http/tests/field_levels.rs: field permission levels over HTTP (M10.2,
// ADR 0024): what each caller reads and writes, the metadata API (hidden fields left out, read_only for
// fields that can be read but not written), 422 field_not_permitted, and that a hidden field is unknown
// everywhere: the answer is compared, byte for byte, with the answer of a server whose DocType does not
// have the field at all. The parts that call export or import came with those routes (P7.2); the summary and
// the saved views are ported (P6).

// account is credit_limit (level 1), vault_code (level 2, nobody has a rule), a table ledger at level 1.
// Agent is a plain clerk, Boss reads and writes level 1, Auditor reads level 1.
func account(withLevels bool) string {
	hidden := ""
	if withLevels {
		hidden = "[[fields]]\nname = \"credit_limit\"\ntype = \"int\"\nperm_level = 1\n" +
			"[[fields]]\nname = \"vault_code\"\ntype = \"data\"\nperm_level = 2\n" +
			"[[fields]]\nname = \"ledger\"\ntype = \"table\"\ntarget = \"acct_line\"\nperm_level = 1\n"
	}
	return fmt.Sprintf(`
name = "account"
label = "Account"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "region"
type = "data"
[[fields]]
name = "revenue"
type = "int"
%s
[[permissions]]
role = "Agent"
read = true
write = true
create = true
delete = true
export = true
import = true
[[permissions]]
role = "Boss"
read = true
write = true
create = true
export = true
import = true
[[permissions]]
role = "Boss"
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
`, hidden)
}

const acctLineDoc = `
name = "acct_line"
label = "Acct Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

type levelSite struct {
	*env
	admin, agent, boss, auditor session
}

func newLevelSite(t *testing.T, withLevels bool) *levelSite {
	t.Helper()
	doctypes := []string{account(withLevels)}
	// Without the table there is no child DocType either.
	if withLevels {
		doctypes = append(doctypes, acctLineDoc)
	}
	e := newEnvWith(t, DefaultConfig(), doctypes...)
	e.user("root", "Administrator")
	e.user("agent", "Agent")
	e.user("boss", "Boss")
	// An auditor that also writes level 0: it can read level 1 but not write it.
	e.user("auditor", "Auditor", "Agent")
	return &levelSite{env: e, admin: e.login("root"), agent: e.login("agent"), boss: e.login("boss"), auditor: e.login("auditor")}
}

func (s *levelSite) get(who session, path string) reply { return s.send(get(path).session(who)) }

func (s *levelSite) post(who session, path string, body any) reply {
	return s.send(post(path).session(who).json(body))
}

func (s *levelSite) patch(who session, name string, version int64, data any) reply {
	return s.send(reqOf(http.MethodPatch, "/api/v1/resource/account/"+name).session(who).json(J{"version": version, "data": data}))
}

// full is the administrator's account with every level filled.
func (s *levelSite) full() (string, int64) {
	rep := s.post(s.admin, "/api/v1/resource/account", J{
		"title": "Acme", "region": "north", "revenue": 10, "credit_limit": 500, "vault_code": "1234",
		"ledger": []any{J{"qty": 1}, J{"qty": 2}},
	})
	wantStatus(s.t, rep, http.StatusCreated)
	version, _ := testInt64(at(rep.data(), "version"))
	return str(rep.data(), "name"), version
}

func fieldNames(rep reply) []string {
	var out []string
	for _, f := range rep.dataObj()["fields"].([]any) {
		out = append(out, str(f, "name"))
	}
	return out
}

// Rust: field_levels.rs::a_user_without_the_level_sees_no_trace_of_the_field
func TestAUserWithoutTheLevelSeesNoTraceOfTheField(t *testing.T) {
	s := newLevelSite(t, true)
	name, _ := s.full()
	// Get and list.
	got := s.get(s.agent, "/api/v1/resource/account/"+name)
	wantStatus(t, got, http.StatusOK)
	keys := got.dataObj()
	for _, hidden := range []string{"credit_limit", "vault_code", "ledger"} {
		if _, ok := keys[hidden]; ok {
			t.Errorf("%s in %v", hidden, keys)
		}
	}
	if _, ok := keys["title"]; !ok {
		t.Error(keys)
	}
	list := s.get(s.agent, "/api/v1/resource/account")
	// (Rust also looks for the text "500" in the body, which a random id or timestamp can contain; the
	// key is what matters.)
	if strings.Contains(list.text, "credit_limit") || strings.Contains(list.text, `:500`) {
		t.Error(list.text)
	}
	// Export: CSV columns and JSON keys.
	csv := s.get(s.agent, "/api/v1/export/account?format=csv")
	wantStatus(t, csv, http.StatusOK, csv.text)
	if first, _, _ := strings.Cut(csv.text, "\r\n"); first != "name,title,region,revenue" {
		t.Error(csv.text)
	}
	export := s.get(s.agent, "/api/v1/export/account?format=json")
	if strings.Contains(export.text, "credit_limit") || strings.Contains(export.text, "ledger") {
		t.Error(export.text)
	}
	// Meta.
	meta := s.get(s.agent, "/api/v1/meta/account")
	sameList(t, fieldNames(meta), "title", "region", "revenue")
	if meta.header.Get("Cache-Control") != "no-store" {
		t.Error(meta.header)
	}
	for _, f := range meta.dataObj()["fields"].([]any) {
		if at(f, "read_only") != false {
			t.Error(f)
		}
	}
	// The administrator sees everything, level 2 included.
	all := s.get(s.admin, "/api/v1/resource/account/"+name)
	eqJSON(t, at(all.data(), "credit_limit"), `500`)
	eqJSON(t, at(all.data(), "vault_code"), `"1234"`)
	if n := len(at(all.data(), "ledger").([]any)); n != 2 {
		t.Error(all.text)
	}
	if n := len(s.get(s.admin, "/api/v1/meta/account").dataObj()["fields"].([]any)); n != 6 {
		t.Errorf("%d fields", n)
	}
}

// Rust: field_levels.rs::a_boss_reads_and_writes_level_one_and_never_sees_level_two
func TestABossReadsAndWritesLevelOneAndNeverSeesLevelTwo(t *testing.T) {
	s := newLevelSite(t, true)
	name, version := s.full()
	got := s.get(s.boss, "/api/v1/resource/account/"+name)
	eqJSON(t, at(got.data(), "credit_limit"), `500`)
	if n := len(at(got.data(), "ledger").([]any)); n != 2 {
		t.Error(got.text)
	}
	if _, ok := got.dataObj()["vault_code"]; ok {
		t.Error(got.text)
	}
	updated := s.patch(s.boss, name, version, J{"credit_limit": 900, "ledger": []any{J{"qty": 5}}})
	wantStatus(t, updated, http.StatusOK)
	eqJSON(t, at(updated.data(), "credit_limit"), `900`)
	if _, ok := updated.dataObj()["vault_code"]; ok {
		t.Error(updated.text)
	}
	// The meta of a boss: level 1 fields are writable, level 2 is not there.
	meta := s.get(s.boss, "/api/v1/meta/account")
	sameList(t, fieldNames(meta), "title", "region", "revenue", "credit_limit", "ledger")
	for _, f := range meta.dataObj()["fields"].([]any) {
		if at(f, "read_only") != false {
			t.Error(f)
		}
	}
	// Level 2 is unknown, the list filter refused as an unknown field.
	filters := encQuery(`[{"field":"vault_code","op":"=","value":"1"}]`)
	refused := s.get(s.boss, "/api/v1/resource/account?filters="+filters)
	wantCode(t, refused, http.StatusBadRequest, "invalid_query")
}

// Rust: field_levels.rs::a_reader_of_level_one_gets_field_not_permitted_and_read_only_in_the_meta
func TestAReaderOfLevelOneGetsFieldNotPermittedAndReadOnlyInTheMeta(t *testing.T) {
	s := newLevelSite(t, true)
	name, version := s.full()
	got := s.get(s.auditor, "/api/v1/resource/account/"+name)
	eqJSON(t, at(got.data(), "credit_limit"), `500`)

	refused := s.patch(s.auditor, name, version, J{"credit_limit": 1})
	wantCode(t, refused, http.StatusUnprocessableEntity, "field_not_permitted")
	eqJSON(t, refused.errorObj()["details"], `["field `+"`credit_limit`"+` cannot be changed with your permissions"]`)
	wantCode(t, s.patch(s.auditor, name, version, J{"ledger": []any{}}), http.StatusUnprocessableEntity, "field_not_permitted")
	created := s.post(s.auditor, "/api/v1/resource/account", J{"title": "x", "credit_limit": 3})
	wantCode(t, created, http.StatusUnprocessableEntity, "field_not_permitted")

	// Sending the stored value back is allowed, with another change.
	ok := s.patch(s.auditor, name, version, J{"credit_limit": 500, "title": "Acme 2", "ledger": []any{J{"qty": 1}, J{"qty": 2}}})
	wantStatus(t, ok, http.StatusOK)

	// The meta says read_only for the fields it cannot write, and only for those.
	meta := s.get(s.auditor, "/api/v1/meta/account")
	var readOnly []string
	for _, f := range meta.dataObj()["fields"].([]any) {
		readOnly = append(readOnly, fmt.Sprintf("%s=%v", str(f, "name"), at(f, "read_only")))
	}
	sameList(t, readOnly, "title=false", "region=false", "revenue=false", "credit_limit=true", "ledger=true")
}

// same: both servers answer the same request as the agent; the answers are equal in status and in text.
func same(t *testing.T, hidden, plain *levelSite, request func() *req) {
	t.Helper()
	a := hidden.send(request().session(hidden.agent))
	b := plain.send(request().session(plain.agent))
	if a.status != b.status || a.text != b.text {
		t.Errorf("%s: %d %s vs %d %s", request().uri, a.status, a.text, b.status, b.text)
	}
}

// Rust: field_levels.rs::a_hidden_field_is_refused_exactly_like_a_field_that_does_not_exist
func TestAHiddenFieldIsRefusedExactlyLikeAFieldThatDoesNotExist(t *testing.T) {
	hidden := newLevelSite(t, true)
	plain := newLevelSite(t, false)
	for _, field := range []string{"credit_limit", "vault_code", "ledger"} {
		filters := encQuery(fmt.Sprintf(`[{"field":%q,"op":"=","value":1}]`, field))
		same(t, hidden, plain, func() *req { return get("/api/v1/resource/account?filters=" + filters) })
		same(t, hidden, plain, func() *req { return get("/api/v1/resource/account?sort=" + field + ":desc") })
		same(t, hidden, plain, func() *req { return get("/api/v1/summary/account?group_by=" + field) })
		same(t, hidden, plain, func() *req {
			return get("/api/v1/summary/account?group_by=region&aggregates=sum:" + field)
		})
		same(t, hidden, plain, func() *req { return get("/api/v1/export/account?format=csv&fields=title," + field) })
		same(t, hidden, plain, func() *req { return get("/api/v1/export/account?format=json&fields=" + field) })
		same(t, hidden, plain, func() *req { return get("/api/v1/export/account?format=csv&filters=" + filters) })
		// A value of the wrong type is not even looked at: no type error names the field.
		for _, value := range []any{"not a number", 1, nil, []any{J{"qty": "x"}}} {
			body := J{"title": "t", field: value}
			same(t, hidden, plain, func() *req { return post("/api/v1/resource/account").json(body) })
		}
		// A saved view.
		view := J{"label": "v", "definition": J{"filters": []any{J{"field": field, "op": "=", "value": 1}}, "sort": []any{}, "columns": []any{}}}
		same(t, hidden, plain, func() *req { return post("/api/v1/views/account").json(view) })
		columns := J{"label": "c", "definition": J{"filters": []any{}, "sort": []any{field}, "columns": []any{field}}}
		same(t, hidden, plain, func() *req { return post("/api/v1/views/account").json(columns) })
	}
	// Update: also with the stored value, and with a wrong type.
	name, version := hidden.full()
	rep := plain.post(plain.admin, "/api/v1/resource/account", J{"title": "Acme", "region": "north", "revenue": 10})
	plainName := str(rep.data(), "name")
	plainVersion, _ := testInt64(at(rep.data(), "version"))
	for _, value := range []any{500, "zzz", nil} {
		a := hidden.patch(hidden.agent, name, version, J{"credit_limit": value})
		b := plain.patch(plain.agent, plainName, plainVersion, J{"credit_limit": value})
		if a.status != b.status || a.text != b.text {
			t.Errorf("%v: %s vs %s", value, a.text, b.text)
		}
		if a.code() != "validation_failed" {
			t.Error(a.text)
		}
	}
}

// Rust: field_levels.rs::import_columns_of_hidden_fields_are_unknown
func TestImportColumnsOfHiddenFieldsAreUnknown(t *testing.T) {
	hidden := newLevelSite(t, true)
	plain := newLevelSite(t, false)
	const csv = "title,credit_limit\nA,5\n"
	importAs := func(s *levelSite, who session, format, contentType, body string) reply {
		return s.send(post("/api/v1/import/account?format="+format+"&mode=insert").session(who).raw(contentType, []byte(body)))
	}
	for _, s := range []*levelSite{hidden, plain} {
		wantCode(t, importAs(s, s.agent, "csv", "text/csv", csv), http.StatusUnprocessableEntity, "invalid_import")
	}
	a, b := importAs(hidden, hidden.agent, "csv", "text/csv", csv), importAs(plain, plain.agent, "csv", "text/csv", csv)
	if a.text != b.text {
		t.Errorf("%s vs %s", a.text, b.text)
	}
	// JSON rows: the row error is the one of an unknown field.
	rows := `[{"title": "A", "credit_limit": 5}]`
	a, b = importAs(hidden, hidden.agent, "json", "application/json", rows), importAs(plain, plain.agent, "json", "application/json", rows)
	wantStatus(t, a, http.StatusUnprocessableEntity, a.text)
	if a.text != b.text {
		t.Errorf("%s vs %s", a.text, b.text)
	}
	// A boss imports the column.
	ok := importAs(hidden, hidden.boss, "csv", "text/csv", csv)
	wantStatus(t, ok, http.StatusOK, ok.text)
	// And the reader of level 1 only cannot: not permitted, as a row error.
	denied := importAs(hidden, hidden.auditor, "csv", "text/csv", csv)
	wantStatus(t, denied, http.StatusUnprocessableEntity, denied.text)
	if !strings.Contains(denied.text, "cannot be changed with your permissions") {
		t.Error(denied.text)
	}
}

// testInt64 is the integer of a number of a decoded reply (the tests decode replies with encoding/json).
func testInt64(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}
