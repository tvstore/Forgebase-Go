package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `data export` and `data import` (M9.3, ADR 0021) against a real PostgreSQL, run in process. Ported from
// crates/forgebase/tests/cli.rs (the data_* tests and the data export case of
// malformed_json_and_filters_fail_before_the_database_is_used).

func (e *env) insertCustomer(email, name string, points int) {
	e.t.Helper()
	data, err := marshalForCLI(map[string]any{"email": email, "full_name": name, "loyalty_points": points})
	if err != nil {
		e.t.Fatal(err)
	}
	r := e.doc("insert", "customer", "--data", data)
	expectCode(e.t, r, 0, "insert customer")
}

func marshalForCLI(v any) (string, error) {
	text, err := prettyJSON(v)
	return strings.Join(strings.Fields(text), " "), err
}

func (e *env) migrated() *env {
	e.t.Helper()
	e.migrate()
	return e
}

// Rust: cli.rs::data_export_writes_csv_and_json_to_stdout_or_a_new_file
func TestDataExportWritesCSVAndJSONToStdoutOrANewFile(t *testing.T) {
	e := newEnv(t).migrated()
	e.insertCustomer("a@x.y", `Ann, "A"`, 30)
	e.insertCustomer("b@x.y", "=Bob", -5)
	e.insertCustomer("c@x.y", "Cy", 20)
	export := func(extra ...string) result {
		return e.run(append([]string{"data", "export", "customer", "--doctypes", doctypes}, extra...)...)
	}

	r := export("--fields", "name,full_name,loyalty_points", "--sort", "loyalty_points:desc",
		"--filter", `[{"field":"loyalty_points","op":">","value":0}]`)
	expectCode(t, r, 0, "export csv")
	if want := "name,full_name,loyalty_points\r\na@x.y,\"Ann, \"\"A\"\"\",30\r\nc@x.y,Cy,20\r\n"; r.stdout != want {
		t.Errorf("%q, want %q", r.stdout, want)
	}
	// The formula mitigation applies to text, not to the number -5.
	r = export("--fields", "full_name,loyalty_points", "--sort", "name")
	expectContains(t, r.stdout, "'=Bob,-5\r\n")
	// JSON: an array of documents.
	r = export("--format", "json", "--sort", "name")
	expectCode(t, r, 0, "export json")
	documents := r.jsonArray(t)
	if len(documents) != 3 || documents[0].(map[string]any)["name"] != "a@x.y" || documents[0].(map[string]any)["full_name"] != `Ann, "A"` {
		t.Errorf("%v", documents)
	}

	// --out writes a new file and never overwrites; a bad option writes nothing.
	path := filepath.Join(t.TempDir(), "customers.csv")
	r = export("--out", path, "--fields", "name")
	expectCode(t, r, 0, "export to a file")
	expectContains(t, r.stdout, "wrote 3 row(s)")
	read := func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := read(); got != "name\r\na@x.y\r\nb@x.y\r\nc@x.y\r\n" {
		t.Errorf("%q", got)
	}
	again := export("--out", path)
	expectCode(t, again, 1, "export over a file")
	expectContains(t, again.stderr, "cannot create")
	if n := len(strings.Split(strings.TrimSuffix(read(), "\r\n"), "\r\n")); n != 4 {
		t.Errorf("%d lines", n)
	}
	for _, bad := range [][]string{{"--fields", "nope"}, {"--fields", "name,name"}, {"--filter", "notjson"}, {"--sort", "nope"}} {
		r := export(bad...)
		expectCode(t, r, 1, fmt.Sprint(bad))
		if r.stdout != "" {
			t.Errorf("%v: %q", bad, r.stdout)
		}
	}
	// A failed export leaves no file: a limit refusal comes before the file is created.
	missing := filepath.Join(t.TempDir(), "limit.csv")
	r = export("--out", missing, "--limit-override", "2")
	expectCode(t, r, 1, "limit with a file")
	if _, err := os.Stat(missing); err == nil {
		t.Error("the file of a refused export exists")
	}
	// The limit refuses before anything is written, and --limit-override raises it.
	r = export("--limit-override", "2")
	expectCode(t, r, 1, "limit")
	if r.stdout != "" {
		t.Errorf("%q", r.stdout)
	}
	expectContains(t, r.stderr, "3 rows, over the limit of 2")
	expectCode(t, export("--limit-override", "3"), 0, "limit raised")
}

// Rust: cli.rs::data_import_dry_run_failure_exit_code_and_success
func TestDataImportDryRunFailureExitCodeAndSuccess(t *testing.T) {
	e := newEnv(t).migrated()
	dir := t.TempDir()
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(path string, extra ...string) result {
		return e.run(append([]string{"data", "import", "customer", path, "--doctypes", doctypes}, extra...)...)
	}
	count := func() string { return e.scalar("SELECT count(*)::text FROM doc_customer") }
	good := file("good.csv", "email,full_name,loyalty_points\r\na@x.y,Ann,3\r\nb@x.y,Bob,\r\n")
	bad := file("bad.csv", "email,full_name,loyalty_points\r\na@x.y,Ann,3\r\nb@x.y,,x\r\nc@x.y,Cy,1\r\n")

	// A dry run keeps nothing, exit 0.
	r := run(good, "--format", "csv", "--dry-run")
	expectCode(t, r, 0, "dry run")
	summary := r.json(t)
	if summary["dry_run"] != true || summary["rows"] != float64(2) || summary["inserted"] != float64(2) || count() != "0" {
		t.Errorf("%v %s", summary, count())
	}

	// A failing file: the summary with the row errors on stdout, exit 1, nothing imported.
	r = run(bad, "--format", "csv")
	expectCode(t, r, 1, "failing file")
	summary = r.json(t)
	first := summary["errors"].([]any)[0].(map[string]any)
	if summary["error_count"] != float64(1) || first["row"] != float64(2) || first["name"] != "b@x.y" || first["line"] != float64(3) {
		t.Errorf("%v", summary)
	}
	expectContains(t, r.stderr, "nothing was imported")
	if count() != "0" {
		t.Error("something was imported")
	}
	// A dry run of a failing file also exits 1.
	r = run(bad, "--format", "csv", "--dry-run")
	expectCode(t, r, 1, "dry run of a failing file")
	expectContains(t, r.stderr, "(dry run: nothing was imported)")

	// The real import, recorded as --actor.
	r = run(good, "--format", "csv", "--actor", "importer")
	expectCode(t, r, 0, "real import")
	if r.json(t)["inserted"] != float64(2) || count() != "2" {
		t.Errorf("%s", r.stdout)
	}
	if got := e.scalar("SELECT owner FROM doc_customer WHERE name = 'a@x.y'"); got != "importer" {
		t.Error(got)
	}

	// An upsert updates by name (the empty cell is null) and inserts the rest.
	upsert := file("upsert.csv", "name,full_name,loyalty_points\r\na@x.y,Ann B,\r\nz@x.y,Zed,9\r\n")
	expectCode(t, run(upsert, "--format", "csv", "--upsert"), 1, "z@x.y has no email")
	upsert = file("upsert2.csv", "name,email,full_name,loyalty_points\r\na@x.y,a@x.y,Ann B,\r\nz@x.y,z@x.y,Zed,9\r\n")
	r = run(upsert, "--format", "csv", "--upsert")
	expectCode(t, r, 0, "upsert")
	summary = r.json(t)
	if summary["updated"] != float64(1) || summary["inserted"] != float64(1) {
		t.Errorf("%v", summary)
	}
	if got := e.scalar("SELECT full_name || '/' || coalesce(loyalty_points::text, 'null') FROM doc_customer WHERE name = 'a@x.y'"); got != "Ann B/null" {
		t.Error(got)
	}

	// A JSON file; a file the format does not fit and a missing file are refused before the database is
	// touched.
	jsonFile := file("docs.json", `[{"email":"j@x.y","full_name":"Jay"}]`)
	expectCode(t, run(jsonFile, "--format", "json"), 0, "json file")
	if count() != "4" {
		t.Error(count())
	}
	r = run(jsonFile, "--format", "csv")
	expectCode(t, r, 1, "json as csv")
	expectContains(t, r.stderr, "the file cannot be imported")
	r = run(filepath.Join(dir, "missing.csv"), "--format", "csv")
	expectCode(t, r, 1, "missing file")
	expectContains(t, r.stderr, "cannot read")
	// --format is required.
	expectCode(t, run(good), 2, "no --format")
	if count() != "4" {
		t.Error(count())
	}
}

// Rust: cli.rs::malformed_json_and_filters_fail_before_the_database_is_used (the data export case)
func TestDataExportWithAMalformedFilterFailsBeforeTheDatabaseIsUsed(t *testing.T) {
	r := runCLI(unusedURL, "", "data", "export", "customer", "--filter", "{bad", "--doctypes", doctypes)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: invalid JSON") || strings.Contains(r.stderr, "site `") {
		t.Errorf("%+v", r)
	}
	// A missing file and an unknown DocType do not touch it either.
	r = runCLI(unusedURL, "", "data", "import", "customer", filepath.Join(t.TempDir(), "none.csv"), "--format", "csv", "--doctypes", doctypes)
	if r.code != 1 || !strings.Contains(r.stderr, "cannot read") || strings.Contains(r.stderr, "site `") {
		t.Errorf("%+v", r)
	}
	r = runCLI(unusedURL, "", "data", "export", "nope", "--doctypes", doctypes)
	if r.code != 1 || !strings.Contains(r.stderr, "unknown DocType `nope`") || strings.Contains(r.stderr, "site `") {
		t.Errorf("%+v", r)
	}
}
