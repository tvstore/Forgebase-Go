package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// End-to-end tests of the commands against a real PostgreSQL (see dbtest), run in process: the same
// code path as the binary (Run), with the environment and the streams captured. Ported from
// crates/forgebase/tests/cli.rs and sites.rs; the exec-based ones (exit codes of the real process,
// standard input of a pipe) are in cmd/forgebase.
//
// The serve_* tests and the worker tests (the_worker_of_a_binary_without_app_jobs_..., a_worker_claims_only_...)
// run the real binary (cmd/forgebase, serve_test.go). Ported in P6: doc_transition_takes_a_workflow_transition_as_the_system_context
// and migrate_gives_documents_their_state_and_warns_about_a_state_that_is_gone. Deferred, with the phase that
// brings the command: the webhook_*, data_*, customize_* and user_permission_* tests (P7).

// doctypes is the DOCTYPES fixture of the Rust CLI tests (tests/fixtures/quickstart), byte-identical.
const doctypes = "testdata/quickstart"

const dropCustomer = "DROP TABLE doc_sales_order_item, doc_sales_order, doc_customer; " +
	"DELETE FROM fb_doctype WHERE name IN ('customer', 'sales_order', 'sales_order_item')"

type result struct {
	code           int
	stdout, stderr string
}

// json decodes the standard output of a command.
func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	return v
}

// env runs commands with one database.
type env struct {
	t    *testing.T
	d    *dbtest.DB
	vars map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	return &env{t: t, d: d, vars: map[string]string{"FORGEBASE_DATABASE_URL": d.URL()}}
}

func (e *env) run(args ...string) result { return e.runStdin("", args...) }

func (e *env) runStdin(stdin string, args ...string) result {
	e.t.Helper()
	return runCLI(e.vars, stdin, args...)
}

// runCLI runs one command line with the given environment.
func runCLI(vars map[string]string, stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	code := Run(Options{
		Version: "9.9.9",
		Args:    args,
		Getenv:  func(name string) string { return vars[name] },
		Stdin:   strings.NewReader(stdin),
		Stdout:  &out,
		Stderr:  &errOut,
	})
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// withTypes adds --doctypes to a command line.
func withTypes(args ...string) []string { return append(args, "--doctypes", doctypes) }

func (e *env) migrate() {
	e.t.Helper()
	if r := e.run("migrate", "--doctypes", doctypes); r.code != 0 {
		e.t.Fatalf("migrate: %d %s", r.code, r.stderr)
	}
}

// doc runs a doc command with the DOCTYPES fixture.
func (e *env) doc(args ...string) result {
	return e.run(withTypes(append([]string{"doc"}, args...)...)...)
}

func (e *env) scalar(sql string) string {
	e.t.Helper()
	return e.d.Rows(sql)[0][0]
}

func expectCode(t *testing.T, r result, code int, context string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: exit code %d, want %d; stdout %q; stderr %q", context, r.code, code, r.stdout, r.stderr)
	}
}

func expectContains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("missing %q in:\n%s", p, text)
		}
	}
}

// Rust: cli.rs::version_and_usage_errors (the usage errors; the version of the real binary is
// checked in cmd/forgebase)
func TestVersionAndUsageErrors(t *testing.T) {
	r := runCLI(nil, "", "--version")
	if r.code != 0 || r.stdout != "forgebase 9.9.9\n" {
		t.Fatalf("%+v", r)
	}
	for _, args := range [][]string{
		{"migrate"},
		{"doc", "get", "customer"},
		// update and delete need --version.
		{"doc", "update", "customer", "x", "--doctypes", doctypes, "--data", "{}"},
		{"doc", "delete", "customer", "x", "--doctypes", doctypes},
		{"doc", "delete", "customer", "x", "--doctypes", doctypes, "--version", "abc"},
		{"bogus"},
		{},
	} {
		if r := runCLI(nil, "", args...); r.code != 2 {
			t.Errorf("%v: exit code %d, stderr %s", args, r.code, r.stderr)
		}
	}
}

// Rust: cli.rs::the_stock_binary_without_doctypes_is_a_usage_error_that_names_the_subcommand
func TestTheStockBinaryWithoutDoctypesIsAUsageErrorThatNamesTheSubcommand(t *testing.T) {
	for _, c := range []struct {
		args  []string
		usage string
	}{
		{[]string{"migrate"}, "Usage: forgebase migrate "},
		{[]string{"serve"}, "Usage: forgebase serve "},
		{[]string{"doc", "get", "customer", "x"}, "Usage: forgebase doc get "},
	} {
		r := runCLI(nil, "", c.args...)
		if r.code != 2 || r.stdout != "" {
			t.Errorf("%v: %+v", c.args, r)
		}
		expectContains(t, r.stderr, "--doctypes <DIR>", c.usage)
	}
	// The help says when the option is required.
	help := runCLI(nil, "", "migrate", "--help")
	if help.code != 0 {
		t.Fatalf("%+v", help)
	}
	expectContains(t, help.stdout, "Required when the binary has no apps")
}

// Rust: cli.rs::missing_database_url_is_a_clear_error
func TestMissingDatabaseURLIsAClearError(t *testing.T) {
	r := runCLI(nil, "", "migrate", "--doctypes", doctypes)
	if r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "error: FORGEBASE_DATABASE_URL is not set") {
		t.Fatalf("%+v", r)
	}
}

// Rust: cli.rs::dry_run_creates_nothing
func TestDryRunCreatesNothing(t *testing.T) {
	e := newEnv(t)
	r := e.run("migrate", "--doctypes", doctypes, "--dry-run")
	expectCode(t, r, 0, "dry run")
	expectContains(t, r.stdout, "framework migrations: 13 pending",
		"0001 init", "0002 series", "0003 auth", "0004 apps", "0005 jobs", "0006 schedules",
		"0007 finished_jobs_index", "0008 workflow_log", "0009 saved_views", "0010 webhooks")
	if got := e.scalar("SELECT coalesce(to_regclass('fb_migration')::text, '-')"); got != "-" {
		t.Fatalf("fb_migration: %s", got)
	}
	if got := e.scalar("SELECT coalesce(to_regclass('doc_customer')::text, '-')"); got != "-" {
		t.Fatalf("doc_customer: %s", got)
	}

	// After the framework migrations, a dry run shows the CREATE TABLE and still changes nothing.
	expectCode(t, e.run("migrate", "--doctypes", doctypes), 0, "migrate")
	e.d.Exec(dropCustomer)
	r = e.run("migrate", "--doctypes", doctypes, "--dry-run")
	expectCode(t, r, 0, "dry run after migrate")
	expectContains(t, r.stdout, "would apply", `CREATE TABLE "doc_customer"`,
		`ALTER TABLE "doc_customer" ADD FOREIGN KEY ("customer_group") REFERENCES "doc_customer_group" ("id") ON DELETE RESTRICT;`,
		`CREATE INDEX ON "doc_customer" ("customer_group");`,
		"dry run: nothing was changed")
	if got := e.scalar("SELECT coalesce(to_regclass('doc_customer')::text, '-')"); got != "-" {
		t.Fatalf("a dry run created doc_customer: %s", got)
	}
}

// Rust: cli.rs::migrate_creates_tables_and_a_rerun_changes_nothing
func TestMigrateCreatesTablesAndARerunChangesNothing(t *testing.T) {
	e := newEnv(t)
	first := e.run("migrate", "--doctypes", doctypes)
	expectCode(t, first, 0, "first migrate")
	expectContains(t, first.stdout, "framework migrations: 13 applied", "schema sync: applied")
	if got := e.scalar("SELECT coalesce(to_regclass('doc_customer')::text, '-')"); got != "doc_customer" {
		t.Fatalf("%s", got)
	}
	// customer -> customer_group, sales_order -> customer, sales_order_item -> sales_order
	if got := e.scalar(`SELECT count(*)::text FROM pg_constraint WHERE contype = 'f' AND conrelid::regclass::text LIKE 'doc\_%'`); got != "3" {
		t.Fatalf("foreign keys: %s", got)
	}
	second := e.run("migrate", "--doctypes", doctypes)
	expectCode(t, second, 0, "second migrate")
	if second.stdout != "framework migrations: up to date\nschema sync: up to date\n" {
		t.Fatalf("%q", second.stdout)
	}
}

const ann = `{"email":"a@example.com","full_name":"Ann","loyalty_points":5,
            "credit_limit":"1234.50","active":true,"joined_on":"2024-03-15",
            "last_seen_at":"2024-03-15T17:20:30+07:00","tier":"gold"}`

// Rust: cli.rs::insert_get_update_delete
func TestInsertGetUpdateDelete(t *testing.T) {
	e := newEnv(t)
	e.migrate()

	inserted := e.doc("insert", "customer", "--actor", "alice", "--data", ann)
	expectCode(t, inserted, 0, "insert")
	doc := inserted.json(t)
	for key, want := range map[string]any{
		"name": "a@example.com", "owner": "alice", "modified_by": "alice", "version": float64(1),
		"loyalty_points": float64(5), "credit_limit": "1234.50", "active": true, "joined_on": "2024-03-15",
		"last_seen_at": "2024-03-15T10:20:30Z",
	} {
		if doc[key] != want {
			t.Errorf("%s = %v, want %v", key, doc[key], want)
		}
	}
	if v, ok := doc["notes"]; !ok || v != nil {
		t.Errorf("notes: %v %v", v, ok)
	}
	if id, _ := doc["id"].(string); len(id) != 36 {
		t.Errorf("id: %v", doc["id"])
	}
	if created, _ := doc["created_at"].(string); !strings.HasSuffix(created, "Z") {
		t.Errorf("created_at: %v", doc["created_at"])
	}

	got := e.doc("get", "customer", "a@example.com")
	expectCode(t, got, 0, "get")
	if got.stdout != inserted.stdout {
		t.Fatalf("get differs from insert:\n%s\n%s", got.stdout, inserted.stdout)
	}

	// Update through standard input.
	updated := e.runStdin(`{"loyalty_points":6,"credit_limit":"0.5","notes":null}`,
		withTypes("doc", "update", "customer", "a@example.com", "--version", "1", "--actor", "bob", "--data", "-")...)
	expectCode(t, updated, 0, "update")
	after := updated.json(t)
	for key, want := range map[string]any{
		"version": float64(2), "modified_by": "bob", "owner": "alice", "loyalty_points": float64(6),
		"credit_limit": "0.50", "created_at": doc["created_at"],
	} {
		if after[key] != want {
			t.Errorf("%s = %v, want %v", key, after[key], want)
		}
	}

	// An empty actor is refused for delete too, and nothing is deleted.
	refused := e.doc("delete", "customer", "a@example.com", "--version", "2", "--actor", "")
	expectCode(t, refused, 1, "delete with an empty actor")
	deleted := e.doc("delete", "customer", "a@example.com", "--version", "2", "--actor", "dee")
	expectCode(t, deleted, 0, "delete")
	if deleted.stdout != "" {
		t.Fatalf("delete prints nothing: %q", deleted.stdout)
	}

	gone := e.doc("get", "customer", "a@example.com")
	expectCode(t, gone, 1, "get after delete")
	if !strings.HasPrefix(gone.stderr, "error: customer `a@example.com` not found") {
		t.Fatalf("%s", gone.stderr)
	}
}

// The output is the pretty JSON of serde_json: two spaces, keys sorted, one document.
func TestTheOutputIsPrettyJSONWithSortedKeys(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	r := e.doc("insert", "customer", "--data", `{"email":"p@example.com","full_name":"Pat <&>"}`)
	expectCode(t, r, 0, "insert")
	lines := strings.Split(r.stdout, "\n")
	if lines[0] != "{" || lines[1] != `  "active": null,` || !strings.HasSuffix(r.stdout, "\n}\n") {
		t.Fatalf("%q", r.stdout)
	}
	var keys []string
	for _, line := range lines[1 : len(lines)-2] {
		keys = append(keys, strings.SplitN(strings.TrimSpace(line), `"`, 3)[1])
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys are not sorted: %v", keys)
		}
	}
	// No HTML escaping.
	expectContains(t, r.stdout, `"full_name": "Pat <&>"`)
}

// Rust: cli.rs::insert_reads_data_from_standard_input_with_default_actor
func TestInsertReadsDataFromStandardInputWithDefaultActor(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	r := e.runStdin(`{"email":"s@example.com","full_name":"Sam"}`, withTypes("doc", "insert", "customer", "--data", "-")...)
	expectCode(t, r, 0, "insert")
	if r.json(t)["owner"] != "cli" {
		t.Fatalf("%s", r.stdout)
	}
}

// Rust: cli.rs::validation_errors_exit_1_with_one_message_per_line
func TestValidationErrorsExit1WithOneMessagePerLine(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	r := e.doc("insert", "customer", "--data", `{"email":"v@example.com","bogus":1,"tier":"platinum"}`)
	expectCode(t, r, 1, "validation")
	if r.stdout != "" || !strings.HasPrefix(r.stderr, "error: validation failed:\n") {
		t.Fatalf("%+v", r)
	}
	expectContains(t, r.stderr, "\n  - field `full_name` is required\n", "\n  - unknown field `bogus`\n", "platinum")
	if got := e.scalar("SELECT count(*)::text FROM doc_customer"); got != "0" {
		t.Fatalf("rows: %s", got)
	}
}

// Rust: cli.rs::bad_input_and_unique_violations_exit_1
func TestBadInputAndUniqueViolationsExit1(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	insert := func(data string) result { return e.doc("insert", "customer", "--data", data) }
	for _, c := range [][2]string{
		{"{", "invalid JSON"},
		{"[1]", "must be a JSON object"},
		{`{"credit_limit":12.5}`, "must be a JSON string"},
		{`{"loyalty_points":1.5}`, "field `loyalty_points`"},
	} {
		r := insert(c[0])
		expectCode(t, r, 1, c[0])
		if !strings.HasPrefix(r.stderr, "error: ") || !strings.Contains(r.stderr, c[1]) {
			t.Errorf("%s: %s", c[0], r.stderr)
		}
	}
	ok := `{"email":"u@example.com","full_name":"U"}`
	expectCode(t, insert(ok), 0, "insert")
	duplicate := insert(ok)
	expectCode(t, duplicate, 1, "duplicate")
	expectContains(t, duplicate.stderr, "unique")

	unknown := e.doc("get", "nope", "x")
	expectCode(t, unknown, 1, "unknown doctype")
	expectContains(t, unknown.stderr, "unknown DocType `nope`")

	nothing := e.doc("update", "customer", "u@example.com", "--version", "1", "--data", "{}")
	expectCode(t, nothing, 1, "no change")
	expectContains(t, nothing.stderr, "at least one")

	rename := e.doc("update", "customer", "u@example.com", "--version", "1", "--data", `{"email":"new@example.com"}`)
	expectCode(t, rename, 1, "rename")
	expectContains(t, rename.stderr, "cannot be changed")
}

// Rust: cli.rs::schema_conflicts_exit_1_and_change_nothing
func TestSchemaConflictsExit1AndChangeNothing(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	e.d.Exec(dropCustomer + "; CREATE TABLE doc_customer (x integer)")
	r := e.run("migrate", "--doctypes", doctypes)
	expectCode(t, r, 1, "conflict")
	if r.stdout != "" || !strings.HasPrefix(r.stderr, "error: schema sync refused, 1 conflict(s)") {
		t.Fatalf("%+v", r)
	}
	expectContains(t, r.stderr, "\n  - table doc_customer already exists")
	if got := e.scalar("SELECT count(*)::text FROM information_schema.columns WHERE table_name = 'doc_customer'"); got != "1" {
		t.Fatalf("columns: %s", got)
	}
}

// The directory of a test that writes DocType files.
func scratch(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	return dir
}

// unusedURL points at nothing: loading the DocTypes fails before any connection is made, so it is
// never used.
var unusedURL = map[string]string{"FORGEBASE_DATABASE_URL": "postgres://unused:unused@127.0.0.1:1/unused"}

// Rust: cli.rs::invalid_doctype_files_exit_1_naming_the_file
func TestInvalidDoctypeFilesExit1NamingTheFile(t *testing.T) {
	dir := scratch(t, map[string]string{"broken.toml": "name = "})
	r := runCLI(unusedURL, "", "migrate", "--doctypes", dir)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: cannot load DocType files:\n") {
		t.Fatalf("%+v", r)
	}
	expectContains(t, r.stderr, "broken.toml")
}

// Rust: cli.rs::links_and_versions_end_to_end
func TestLinksAndVersionsEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.migrate()

	// A link to a group that does not exist is a validation error naming the field.
	customer := `{"email":"l@example.com","full_name":"L","customer_group":"retail"}`
	missing := e.doc("insert", "customer", "--data", customer)
	expectCode(t, missing, 1, "missing group")
	expectContains(t, missing.stderr, "field `customer_group`: customer_group `retail` does not exist")

	expectCode(t, e.doc("insert", "customer_group", "--data", `{"title":"retail"}`), 0, "insert group")
	inserted := e.doc("insert", "customer", "--data", customer)
	expectCode(t, inserted, 0, "insert customer")
	// A link is the target's name in the output.
	if inserted.json(t)["customer_group"] != "retail" {
		t.Fatalf("%s", inserted.stdout)
	}

	// A linked group cannot be deleted; the message names the linking DocType.
	blocked := e.doc("delete", "customer_group", "retail", "--version", "1")
	expectCode(t, blocked, 1, "blocked delete")
	if blocked.stderr != "error: customer_group `retail` cannot be deleted: it is linked from customer\n" {
		t.Fatalf("%q", blocked.stderr)
	}

	// A stale version is refused and reports the current one.
	updated := e.doc("update", "customer", "l@example.com", "--version", "1", "--data", `{"customer_group":null}`)
	expectCode(t, updated, 0, "unlink")
	if updated.json(t)["customer_group"] != nil {
		t.Fatalf("%s", updated.stdout)
	}
	stale := e.doc("update", "customer", "l@example.com", "--version", "1", "--data", `{"loyalty_points":1}`)
	expectCode(t, stale, 1, "stale update")
	expectContains(t, stale.stderr, "expected version 1, current version 2")
	staleDelete := e.doc("delete", "customer", "l@example.com", "--version", "1")
	expectCode(t, staleDelete, 1, "stale delete")
	expectContains(t, staleDelete.stderr, "current version 2")
	if got := e.scalar("SELECT count(*)::text FROM doc_customer"); got != "1" {
		t.Fatalf("rows: %s", got)
	}

	// Now nothing links to the group any more.
	expectCode(t, e.doc("delete", "customer_group", "retail", "--version", "1"), 0, "delete group")
}

// Rust: cli.rs::unknown_link_targets_are_reported_with_the_file
func TestUnknownLinkTargetsAreReportedWithTheFile(t *testing.T) {
	dir := scratch(t, map[string]string{
		"order.toml": "name = \"sales_order\"\nlabel = \"O\"\n[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"customer\"\n",
	})
	r := runCLI(unusedURL, "", "migrate", "--doctypes", dir)
	if r.code != 1 {
		t.Fatalf("%+v", r)
	}
	expectContains(t, r.stderr, "order.toml", "field `customer` links to unknown doctype `customer`")
}

// Rust: cli.rs::child_tables_end_to_end
func TestChildTablesEndToEnd(t *testing.T) {
	e := newEnv(t)
	migrate := e.run("migrate", "--doctypes", doctypes)
	expectCode(t, migrate, 0, "migrate")
	expectContains(t, migrate.stdout, `CREATE TABLE "doc_sales_order_item"`)
	expectCode(t, e.doc("insert", "customer", "--data", `{"email":"c@example.com","full_name":"C"}`), 0, "customer")

	// Insert with rows: the output has the rows in order, with their fields.
	data := `{"customer":"c@example.com","order_date":"2026-10-28","items":[
        {"description":"Widget","qty":2,"rate":"9.50"},
        {"description":"Gadget","qty":1}]}`
	inserted := e.doc("insert", "sales_order", "--data", data)
	expectCode(t, inserted, 0, "insert order")
	order := inserted.json(t)
	name := order["name"].(string)
	if order["version"] != float64(1) || order["customer"] != "c@example.com" {
		t.Fatalf("%s", inserted.stdout)
	}
	wantItems := `[{"description":"Widget","qty":2,"rate":"9.50"},{"description":"Gadget","qty":1,"rate":null}]`
	asJSON := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if asJSON(order["items"]) != wantItems {
		t.Fatalf("items: %s", asJSON(order["items"]))
	}
	if fetched := e.doc("get", "sales_order", name); fetched.stdout != inserted.stdout {
		t.Fatalf("get differs:\n%s", fetched.stdout)
	}
	if got := e.scalar("SELECT count(*)::text FROM doc_sales_order_item"); got != "2" {
		t.Fatalf("rows: %s", got)
	}

	// An update that leaves items out keeps the rows; one that has it replaces them all.
	kept := e.doc("update", "sales_order", name, "--version", "1", "--data", `{"order_date":"2026-10-29"}`)
	expectCode(t, kept, 0, "update without items")
	if asJSON(kept.json(t)["items"]) != wantItems {
		t.Fatalf("%s", kept.stdout)
	}
	replaced := e.doc("update", "sales_order", name, "--version", "2", "--data", `{"items":[{"description":"Only","qty":5}]}`)
	expectCode(t, replaced, 0, "replace items")
	doc := replaced.json(t)
	if doc["version"] != float64(3) || asJSON(doc["items"]) != `[{"description":"Only","qty":5,"rate":null}]` {
		t.Fatalf("%s", replaced.stdout)
	}

	// The table is required: no rows is refused, and nothing changed.
	empty := e.doc("update", "sales_order", name, "--version", "3", "--data", `{"items":[]}`)
	expectCode(t, empty, 1, "no rows")
	if empty.stderr != "error: validation failed:\n  - field `items` is required\n" {
		t.Fatalf("%q", empty.stderr)
	}

	// Row problems are listed with the row position, one per line; nothing is written.
	bad := e.doc("insert", "sales_order", "--data",
		`{"customer":"c@example.com","items":[{"description":"ok","qty":1},{"qty":1},{"description":"x","qty":2,"nope":1}]}`)
	expectCode(t, bad, 1, "bad rows")
	if bad.stderr != "error: validation failed:\n  - field `items` row 2: field `description` is required\n  - field `items` row 3: unknown field `nope`\n" {
		t.Fatalf("%q", bad.stderr)
	}
	if got := e.scalar("SELECT count(*)::text FROM doc_sales_order"); got != "1" {
		t.Fatalf("orders: %s", got)
	}

	// Wrong JSON shapes fail before the database is used.
	for _, c := range [][2]string{
		{`{"items":"x"}`, "error: field `items`: expected an array of rows, found a string\n"},
		{`{"items":[1]}`, "error: field `items`: row 1: expected an object, found a number\n"},
		{`{"items":[{"qty":"two"}]}`, "error: field `items`: row 1: field `qty`: expected an integer, found a string\n"},
	} {
		r := e.doc("insert", "sales_order", "--data", c[0])
		expectCode(t, r, 1, c[0])
		if r.stderr != c[1] {
			t.Errorf("%s: %q", c[0], r.stderr)
		}
	}

	// A child DocType has no commands of its own.
	child := e.doc("insert", "sales_order_item", "--data", `{"description":"x","qty":1}`)
	expectCode(t, child, 1, "child doctype")
	if !strings.HasPrefix(child.stderr, "error: DocType `sales_order_item` is a child DocType") {
		t.Fatalf("%s", child.stderr)
	}

	// Deleting the order deletes its rows.
	expectCode(t, e.doc("delete", "sales_order", name, "--version", "3"), 0, "delete order")
	if got := e.scalar("SELECT count(*)::text FROM doc_sales_order_item"); got != "0" {
		t.Fatalf("rows: %s", got)
	}
}

// Rust: cli.rs::submit_cancel_and_amend_end_to_end
func TestSubmitCancelAndAmendEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	year := e.scalar("SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY')")
	customer := e.doc("insert", "customer", "--data", `{"email":"c@example.com","full_name":"C"}`)
	expectCode(t, customer, 0, "customer")
	// A customer has no docstatus, so its output has no such keys.
	if _, ok := customer.json(t)["docstatus"]; ok {
		t.Fatal("a customer has no docstatus")
	}

	// The series names the order, and it starts as a draft.
	data := `{"customer":"c@example.com","items":[{"description":"Widget","qty":2}]}`
	inserted := e.doc("insert", "sales_order", "--data", data)
	expectCode(t, inserted, 0, "order")
	order := inserted.json(t)
	name := fmt.Sprintf("SO-%s-00001", year)
	if order["name"] != name || order["docstatus"] != float64(0) || order["amended_from"] != nil {
		t.Fatalf("%s", inserted.stdout)
	}
	if _, ok := order["amended_from"]; !ok {
		t.Fatal("amended_from is null, not absent")
	}

	// docstatus and amended_from cannot be set through the data.
	forced := e.doc("update", "sales_order", name, "--version", "1", "--data", `{"docstatus":1}`)
	expectCode(t, forced, 1, "forced docstatus")
	expectContains(t, forced.stderr, "unknown field `docstatus`")

	// Submit: needs --version; the output shows docstatus 1 and the next version.
	stale := e.doc("submit", "sales_order", name, "--version", "9")
	expectCode(t, stale, 1, "stale submit")
	expectContains(t, stale.stderr, "current version 1")
	submitted := e.doc("submit", "sales_order", name, "--version", "1", "--actor", "bob")
	expectCode(t, submitted, 0, "submit")
	s := submitted.json(t)
	if s["docstatus"] != float64(1) || s["version"] != float64(2) || s["modified_by"] != "bob" {
		t.Fatalf("%s", submitted.stdout)
	}
	// Submitting again, deleting and changing a locked field are refused ...
	again := e.doc("submit", "sales_order", name, "--version", "2")
	expectCode(t, again, 1, "submit again")
	expectContains(t, again.stderr, "it is submitted")
	del := e.doc("delete", "sales_order", name, "--version", "2")
	expectCode(t, del, 1, "delete submitted")
	expectContains(t, del.stderr, "cannot delete")
	locked := e.doc("update", "sales_order", name, "--version", "2", "--data", `{"order_date":"2026-10-30"}`)
	expectCode(t, locked, 1, "locked field")
	expectContains(t, locked.stderr, "`order_date` cannot be changed")
	// ... while the allow_on_submit field can change.
	remark := e.doc("update", "sales_order", name, "--version", "2", "--data", `{"remarks":"rush"}`)
	expectCode(t, remark, 0, "remarks")
	if remark.json(t)["docstatus"] != float64(1) {
		t.Fatalf("%s", remark.stdout)
	}

	// Amend needs a cancelled document; cancel, then amend.
	early := e.doc("amend", "sales_order", name, "--version", "3")
	expectCode(t, early, 1, "early amend")
	expectContains(t, early.stderr, "it is submitted")
	cancelled := e.doc("cancel", "sales_order", name, "--version", "3")
	expectCode(t, cancelled, 0, "cancel")
	c := cancelled.json(t)
	if c["docstatus"] != float64(2) || c["version"] != float64(4) {
		t.Fatalf("%s", cancelled.stdout)
	}
	amended := e.doc("amend", "sales_order", name, "--version", "4")
	expectCode(t, amended, 0, "amend")
	a := amended.json(t)
	if a["name"] != name+"-1" || a["amended_from"] != name || a["docstatus"] != float64(0) || a["version"] != float64(1) || a["remarks"] != "rush" {
		t.Fatalf("%s", amended.stdout)
	}
	if fmt.Sprint(a["items"]) != fmt.Sprint(c["items"]) {
		t.Fatalf("the amendment copies the rows: %v %v", a["items"], c["items"])
	}
	// The cancelled document can be amended once.
	twice := e.doc("amend", "sales_order", name, "--version", "4")
	expectCode(t, twice, 1, "amend twice")
	expectContains(t, twice.stderr, "already amended")

	// A cancelled document and a draft can be deleted.
	expectCode(t, e.doc("delete", "sales_order", name, "--version", "4"), 0, "delete cancelled")
	expectCode(t, e.doc("delete", "sales_order", name+"-1", "--version", "1"), 0, "delete draft")

	// A DocType that is not submittable says so; amend takes no data.
	plain := e.doc("submit", "customer", "c@example.com", "--version", "1")
	expectCode(t, plain, 1, "not submittable")
	expectContains(t, plain.stderr, "not submittable")
	withData := e.doc("amend", "sales_order", name, "--version", "1", "--data", "{}")
	expectCode(t, withData, 2, "a usage error")
	if got := e.scalar("SELECT current::text FROM fb_series"); got != "1" {
		t.Fatalf("series counter: %s", got)
	}
}

// Rust: cli.rs::malformed_json_and_filters_fail_before_the_database_is_used (the doc insert, update
// and list cases; the data export case arrives in P7)
func TestMalformedJSONFailsBeforeTheDatabaseIsUsed(t *testing.T) {
	// The URL points at nothing: a connection error would show if the database were opened.
	vars := map[string]string{"FORGEBASE_DATABASE_URL": "postgres://nobody:nothing@127.0.0.1:1/none"}
	for _, args := range [][]string{
		{"doc", "insert", "customer", "--data", "{bad", "--doctypes", doctypes},
		{"doc", "update", "customer", "a", "--version", "1", "--data", "[1]", "--doctypes", doctypes},
		{"doc", "insert", "customer", "--data", `{"a":1} x`, "--doctypes", doctypes},
		{"doc", "insert", "customer", "--data", "", "--doctypes", doctypes},
		{"doc", "list", "customer", "--filter", "{bad", "--doctypes", doctypes},
		{"doc", "list", "customer", "--filter", `{"field":"a"}`, "--doctypes", doctypes},
		{"doc", "list", "customer", "--filter", `[{"field":"a"}]`, "--doctypes", doctypes},
	} {
		r := runCLI(vars, "", args...)
		if r.code != 1 {
			t.Errorf("%v: %+v", args, r)
			continue
		}
		if !strings.HasPrefix(r.stderr, "error: invalid JSON") && !strings.HasPrefix(r.stderr, "error: the data must be a JSON object") &&
			!strings.HasPrefix(r.stderr, "error: invalid filter") {
			t.Errorf("%v: %s", args, r.stderr)
		}
		if strings.Contains(r.stderr, "site `") {
			t.Errorf("%v: the database was opened: %s", args, r.stderr)
		}
	}
	// An unknown DocType never touches the database either.
	r := runCLI(vars, "", "doc", "get", "nope", "x", "--doctypes", doctypes)
	if r.code != 1 || strings.Contains(r.stderr, "site `") {
		t.Errorf("%+v", r)
	}
	expectContains(t, r.stderr, "unknown DocType `nope`; known DocTypes: customer, customer_group, sales_order, sales_order_item")
	empty := scratch(t, nil)
	r = runCLI(vars, "", "doc", "get", "nope", "x", "--doctypes", empty)
	expectContains(t, r.stderr, "known DocTypes: none")
}

// jsonArray decodes the standard output of a command that prints a JSON array.
func (r result) jsonArray(t *testing.T) []any {
	t.Helper()
	var v []any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	return v
}

// Rust: cli.rs::list_filters_sorts_and_pages_as_json
func TestListFiltersSortsAndPagesAsJSON(t *testing.T) {
	e := newEnv(t)
	e.migrate()
	for _, c := range []struct {
		email  string
		points int
		tier   string
	}{{"a@x.y", 30, "gold"}, {"b@x.y", 10, "silver"}, {"c@x.y", 20, "gold"}} {
		data := fmt.Sprintf(`{"email":"%s","full_name":"N","loyalty_points":%d,"tier":"%s"}`, c.email, c.points, c.tier)
		r := e.doc("insert", "customer", "--data", data)
		expectCode(t, r, 0, "insert "+c.email)
	}
	list := func(extra ...string) result { return e.doc(append([]string{"list", "customer"}, extra...)...) }
	names := func(r result) []string {
		t.Helper()
		expectCode(t, r, 0, "list")
		out := []string{}
		for _, d := range r.jsonArray(t) {
			out = append(out, d.(map[string]any)["name"].(string))
		}
		return out
	}
	same := func(got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("got %v, want %v", got, want)
		}
	}
	// Default: everything (three is below the page size), ordered by name.
	same(names(list()), "a@x.y", "b@x.y", "c@x.y")
	// A document in the list has the same shape as doc get (standard columns and values).
	first := list("--limit", "1").jsonArray(t)[0].(map[string]any)
	if first["owner"] != "cli" || first["loyalty_points"] != float64(30) || first["version"] != float64(1) {
		t.Errorf("%v", first)
	}
	// Sorting, :desc, several keys.
	same(names(list("--sort", "loyalty_points:desc")), "a@x.y", "c@x.y", "b@x.y")
	same(names(list("--sort", "tier:desc", "--sort", "loyalty_points")), "b@x.y", "c@x.y", "a@x.y")
	// Filters, all operators.
	filter := func(text string) []string { return names(list("--filter", text)) }
	same(filter(`[{"field":"tier","op":"=","value":"gold"},{"field":"loyalty_points","op":">","value":20}]`), "a@x.y")
	same(filter(`[{"field":"tier","op":"!=","value":"gold"}]`), "b@x.y")
	same(filter(`[{"field":"loyalty_points","op":"in","value":[10,30]}]`), "a@x.y", "b@x.y")
	same(filter(`[{"field":"loyalty_points","op":"<=","value":10}]`), "b@x.y")
	if n := len(filter(`[{"field":"name","op":"like","value":"%@x.y"}]`)); n != 3 {
		t.Error(n)
	}
	if n := len(filter(`[{"field":"notes","op":"is null"}]`)); n != 3 {
		t.Error(n)
	}
	same(filter(`[{"field":"notes","op":"is not null"}]`))
	same(filter(`[{"field":"customer_group","op":"=","value":"nope"}]`))
	if n := len(filter("[]")); n != 3 {
		t.Error(n)
	}
	// Paging.
	same(names(list("--limit", "2")), "a@x.y", "b@x.y")
	same(names(list("--limit", "2", "--offset", "2")), "c@x.y")
	same(names(list("--offset", "3")))
	// Every kind of mistake is an exit-1 error that names the problem.
	for _, c := range []struct {
		args   []string
		needle string
	}{
		{[]string{"--limit", "501"}, "maximum"},
		{[]string{"--limit", "0"}, "at least 1"},
		{[]string{"--sort", "nope"}, "unknown field"},
		{[]string{"--sort", "name:sideways"}, "FIELD:desc"},
		{[]string{"--filter", "not json"}, "invalid JSON"},
		{[]string{"--filter", "{}"}, "JSON array"},
		{[]string{"--filter", `[{"field":"nope","op":"=","value":1}]`}, "unknown field"},
		{[]string{"--filter", `[{"field":"tier","op":"~","value":1}]`}, "unknown operator"},
		{[]string{"--filter", `[{"field":"tier","op":"=","value":1}]`}, "tier"},
		{[]string{"--filter", `[{"field":"tier","op":"="}]`}, "needs a `value`"},
		{[]string{"--filter", `[{"field":"tier","op":"is null","value":1}]`}, "takes no `value`"},
		{[]string{"--filter", `[{"field":"loyalty_points","op":"like","value":"1%"}]`}, "text fields only"},
		{[]string{"--filter", `[{"field":"tier","op":"=","value":"gold","extra":1}]`}, "unknown key"},
	} {
		r := list(c.args...)
		if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, c.needle) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	// The messages are Rust's, word for word.
	expectContains(t, list("--limit", "501").stderr, "error: invalid list query: limit 501 is above the maximum of 500")
	expectContains(t, list("--sort", "nope").stderr, `error: invalid list query: unknown field "nope" for DocType `+"`customer`")
	expectContains(t, list("--filter", `[{"field":"tier","op":"~","value":1}]`).stderr,
		"error: invalid filter: filter 1: unknown operator `~` (use =, !=, <, <=, >, >=, in, like, is null, is not null)")
	// An unknown DocType is reported before the database is touched.
	r := e.doc("list", "nope")
	expectCode(t, r, 1, "unknown DocType")
	expectContains(t, r.stderr, "unknown DocType")
}

const secretPassword = "s3cret pass phrase"

// migratedEnv is an environment whose database is migrated.
func migratedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.migrate()
	return e
}

// Rust: cli.rs::user_commands_manage_users_and_roles
func TestUserCommandsManageUsersAndRoles(t *testing.T) {
	e := migratedEnv(t)

	// The password comes from the first line of standard input only.
	r := e.runStdin(secretPassword+"\nsecond line is ignored\n", "user", "create", "alice", "--role", "Sales", "--role", "Admin")
	expectCode(t, r, 0, "create")
	if r.stdout != "created user alice\n" || strings.Contains(r.stdout+r.stderr, secretPassword) {
		t.Errorf("%+v", r)
	}
	if got := e.scalar("SELECT string_agg(role, ',' ORDER BY role) FROM fb_user_role"); got != "Admin,Sales" {
		t.Error(got)
	}
	hash := e.scalar("SELECT password_hash FROM fb_user")
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, secretPassword) {
		t.Error(hash)
	}

	// A duplicate, a bad password and a missing password are errors that never show the password.
	r = e.runStdin("another password\n", "user", "create", "alice")
	expectCode(t, r, 1, "duplicate")
	expectContains(t, r.stderr, "already exists")
	r = e.runStdin("short\n", "user", "create", "bob")
	expectCode(t, r, 1, "short")
	expectContains(t, r.stderr, "invalid password")
	if strings.Contains(r.stderr, "short") {
		t.Errorf("the password is shown: %s", r.stderr)
	}
	r = e.runStdin("", "user", "create", "bob")
	expectCode(t, r, 1, "missing")
	expectContains(t, r.stderr, "invalid password")
	// The password is never an argument: a second positional argument is a usage error.
	expectCode(t, e.run("user", "create", "bob", "hunter2hunter2"), 2, "a password as an argument")

	for _, c := range []struct {
		args []string
		out  string
	}{
		{[]string{"user", "add-role", "alice", "Accounts"}, "added role Accounts to user alice\n"},
		{[]string{"user", "add-role", "alice", "Accounts"}, "user alice has role Accounts already\n"},
		{[]string{"user", "remove-role", "alice", "Admin"}, "removed role Admin from user alice\n"},
		{[]string{"user", "remove-role", "alice", "Admin"}, "user alice does not have role Admin\n"},
	} {
		if r := e.run(c.args...); r.code != 0 || r.stdout != c.out {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	if got := e.scalar("SELECT string_agg(role, ',' ORDER BY role) FROM fb_user_role"); got != "Accounts,Sales" {
		t.Error(got)
	}
	r = e.run("user", "add-role", "nobody", "Admin")
	expectCode(t, r, 1, "unknown user")
	expectContains(t, r.stderr, "user `nobody` not found")

	r = e.run("user", "disable", "alice")
	expectCode(t, r, 0, "disable")
	if r.stdout != "disabled user alice; its sessions were ended\n" || e.scalar("SELECT enabled::text FROM fb_user") != "false" {
		t.Errorf("%+v", r)
	}
	r = e.run("user", "enable", "alice")
	expectCode(t, r, 0, "enable")
	if r.stdout != "enabled user alice\n" || e.scalar("SELECT enabled::text FROM fb_user") != "true" {
		t.Errorf("%+v", r)
	}

	// Set the password (CRLF line ending stripped) and end the sessions.
	e.d.Exec("INSERT INTO fb_session (token_hash, csrf_hash, user_id, expires_at) " +
		"SELECT '\\x00', '\\x01', id, now() + interval '1 day' FROM fb_user")
	before := e.scalar("SELECT password_hash FROM fb_user")
	r = e.runStdin("a new password phrase\r\n", "user", "set-password", "alice")
	expectCode(t, r, 0, "set-password")
	if strings.Contains(r.stdout, "a new password phrase") || r.stdout != "changed the password of user alice; its sessions were ended\n" {
		t.Errorf("%+v", r)
	}
	if e.scalar("SELECT password_hash FROM fb_user") == before || e.scalar("SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("the password or the sessions did not change")
	}
	// The new password is the one in the database: it logs in (the CRLF was stripped).
	if _, err := db.Login(context.Background(), e.d.Pool, "alice", "a new password phrase"); err != nil {
		t.Errorf("the new password does not log in: %v", err)
	}
}

// Rust: cli.rs::token_create_prints_the_secret_once_and_revoke_works
func TestTokenCreatePrintsTheSecretOnceAndRevokeWorks(t *testing.T) {
	e := migratedEnv(t)
	expectCode(t, e.runStdin("an ordinary password\n", "user", "create", "alice"), 0, "create user")

	r := e.run("token", "create", "alice")
	expectCode(t, r, 0, "token create")
	if r.stderr != "" || !strings.HasSuffix(r.stdout, "\n") || strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("%+v", r)
	}
	key, secret, ok := strings.Cut(strings.TrimSuffix(r.stdout, "\n"), ":")
	if !ok || len(key) != 32 || len(secret) != 64 {
		t.Fatalf("%q", r.stdout)
	}
	// The secret is not stored; the digest is.
	if e.scalar("SELECT key FROM fb_api_token") != key || strings.Contains(e.scalar("SELECT t::text FROM fb_api_token t"), secret) {
		t.Error("what is stored")
	}
	if e.scalar("SELECT (secret_hash = sha256(convert_to('"+secret+"', 'UTF8')))::text FROM fb_api_token") != "true" {
		t.Error("the digest")
	}

	r = e.run("token", "revoke", key)
	expectCode(t, r, 0, "revoke")
	if r.stdout != "revoked API token "+key+"\n" || strings.Contains(r.stdout, secret) {
		t.Errorf("%+v", r)
	}
	if e.scalar("SELECT (revoked_at IS NOT NULL)::text FROM fb_api_token") != "true" {
		t.Error("not revoked")
	}

	// A well-formed but unknown key.
	r = e.run("token", "revoke", "00000000000000000000000000000000")
	expectCode(t, r, 1, "unknown key")
	expectContains(t, r.stderr, "not found")
	// A malformed key, also a pasted key:secret, is rejected without being echoed.
	for _, bad := range []string{"nope", key + ":" + secret} {
		r := e.run("token", "revoke", bad)
		expectCode(t, r, 1, "malformed key")
		expectContains(t, r.stderr, "invalid API token key")
		if strings.Contains(r.stderr, bad) || strings.Contains(r.stderr, secret) || r.stdout != "" {
			t.Errorf("%q: %+v", bad, r)
		}
	}
	r = e.run("token", "create", "nobody")
	if r.code != 1 || r.stdout != "" {
		t.Errorf("%+v", r)
	}
	// A disabled user gets no token.
	expectCode(t, e.run("user", "disable", "alice"), 0, "disable")
	r = e.run("token", "create", "alice")
	expectCode(t, r, 1, "disabled")
	expectContains(t, r.stderr, "is disabled")
	if r.stdout != "" {
		t.Error(r.stdout)
	}
}

// Standard input that is not UTF-8 is refused before the database is used.
func TestStandardInputMustBeUTF8(t *testing.T) {
	vars := map[string]string{"FORGEBASE_DATABASE_URL": "postgres://nobody:nothing@127.0.0.1:1/none"}
	r := runCLI(vars, "{\"a\":\"\xff\"}", withTypes("doc", "insert", "customer", "--data", "-")...)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: cannot read standard input: ") {
		t.Fatalf("%+v", r)
	}
}

// ---- sites (crates/forgebase/tests/sites.rs, the parts that apply to migrate and doc) -----------

const (
	aURL = "FB_TEST_A_URL"
	bURL = "FB_TEST_B_URL"
)

// pair is two sites, a and b, each with its own database, and a config file that lists them.
type pair struct {
	t    *testing.T
	a, b *dbtest.DB
	dir  string
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{t: t, a: dbtest.New(t), b: dbtest.New(t), dir: t.TempDir()}
	config := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\", \"B2.localhost\"]\ndatabase_url_env = \"%s\"\npool_size = 4\n", aURL, bURL)
	if err := os.WriteFile(filepath.Join(p.dir, "forgebase.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *pair) vars() map[string]string {
	return map[string]string{aURL: p.a.URL(), bURL: p.b.URL()}
}

func (p *pair) run(args ...string) result { return p.runStdin("", args...) }

func (p *pair) runStdin(stdin string, args ...string) result {
	return runCLI(p.vars(), stdin, append([]string{"--config", filepath.Join(p.dir, "forgebase.toml")}, args...)...)
}

func scalar(t *testing.T, d *dbtest.DB, sql string) string {
	t.Helper()
	return d.Rows(sql)[0][0]
}

// Rust: sites.rs::site_is_required_on_every_database_command_but_serve_with_two_sites (migrate, doc,
// user, token and job)
func TestSiteIsRequiredOnEveryDatabaseCommandWithTwoSites(t *testing.T) {
	p := newPair(t)
	for _, args := range [][]string{
		{"migrate", "--doctypes", doctypes},
		{"doc", "get", "customer", "x", "--doctypes", doctypes},
		{"user", "disable", "root"},
		{"token", "revoke", "key"},
		{"job", "list"},
	} {
		r := p.run(args...)
		if r.code != 2 || r.stdout != "" {
			t.Errorf("%v: %+v", args, r)
		}
		expectContains(t, r.stderr, "--site NAME", "a, b", "Usage: forgebase [OPTIONS] <COMMAND>")
	}
	// An unknown site lists the known ones.
	r := p.run("migrate", "--site", "nope", "--doctypes", doctypes)
	expectCode(t, r, 2, "unknown site")
	expectContains(t, r.stderr, "unknown site `nope`", "a, b")
	// Nothing was touched.
	for _, d := range []*dbtest.DB{p.a, p.b} {
		if got := scalar(t, d, "SELECT coalesce(to_regclass('fb_migration')::text, '-')"); got != "-" {
			t.Errorf("fb_migration: %s", got)
		}
	}
}

// Rust: sites.rs::one_site_is_the_default_and_no_config_means_the_implicit_default_site
func TestOneSiteIsTheDefaultAndNoConfigMeansTheImplicitDefaultSite(t *testing.T) {
	p := newPair(t)
	single := filepath.Join(p.dir, "single.toml")
	text := fmt.Sprintf("[[sites]]\nname = \"only\"\nhosts = [\"x.localhost\"]\ndatabase_url_env = \"%s\"\n", aURL)
	if err := os.WriteFile(single, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runCLI(map[string]string{aURL: p.a.URL()}, "", "migrate", "--doctypes", doctypes, "--config", single)
	expectCode(t, r, 0, "the only site")
	if got := scalar(t, p.a, "SELECT coalesce(to_regclass('fb_migration')::text, '-')"); got != "fb_migration" {
		t.Errorf("a: %s", got)
	}
	if got := scalar(t, p.b, "SELECT coalesce(to_regclass('fb_migration')::text, '-')"); got != "-" {
		t.Errorf("b: %s", got)
	}

	// Without a config file: the implicit site default, from FORGEBASE_DATABASE_URL.
	for _, site := range [][]string{nil, {"--site", "default"}} {
		args := append([]string{"migrate", "--doctypes", doctypes, "--dry-run"}, site...)
		r := runCLI(map[string]string{"FORGEBASE_DATABASE_URL": p.b.URL()}, "", args...)
		expectCode(t, r, 0, fmt.Sprint(site))
	}
	r = runCLI(map[string]string{"FORGEBASE_DATABASE_URL": p.b.URL()}, "", "migrate", "--doctypes", doctypes, "--site", "a")
	expectCode(t, r, 2, "unknown site")
	expectContains(t, r.stderr, "only site is `default`")
}

// Rust: sites.rs::the_config_comes_from_the_option_or_the_environment_and_the_option_wins
func TestTheConfigComesFromTheOptionOrTheEnvironmentAndTheOptionWins(t *testing.T) {
	p := newPair(t)
	config := filepath.Join(p.dir, "forgebase.toml")
	dry := []string{"migrate", "--site", "b", "--doctypes", doctypes, "--dry-run"}
	// Only through the environment variable.
	r := runCLI(map[string]string{"FORGEBASE_CONFIG": config, bURL: p.b.URL()}, "", dry...)
	expectCode(t, r, 0, "environment")
	// A wrong path in the environment does not matter when the option is given.
	r = runCLI(map[string]string{"FORGEBASE_CONFIG": filepath.Join(p.dir, "missing.toml"), bURL: p.b.URL()}, "",
		append([]string{"--config", config}, dry...)...)
	expectCode(t, r, 0, "option")
	// A file that cannot be read is exit code 1, naming the path.
	r = runCLI(nil, "", "migrate", "--doctypes", doctypes, "--config", filepath.Join(p.dir, "missing.toml"))
	expectCode(t, r, 1, "missing file")
	expectContains(t, r.stderr, "missing.toml", "No such file or directory (os error 2)")
	// The current directory is never searched: a forgebase.toml there changes nothing.
	old, _ := os.Getwd()
	if err := os.Chdir(p.dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	r = runCLI(map[string]string{"FORGEBASE_DATABASE_URL": p.b.URL()}, "", "migrate", "--site", "a", "--doctypes", filepath.Join(old, doctypes))
	expectCode(t, r, 2, "no discovery")
}

// Rust: sites.rs::an_invalid_config_reports_every_problem_and_exits_1
func TestAnInvalidConfigReportsEveryProblemAndExits1(t *testing.T) {
	p := newPair(t)
	bad := filepath.Join(p.dir, "bad.toml")
	text := "[[sites]]\nname = \"Bad\"\nhosts = [\"a.test:80\"]\ndatabase_url_env = \"X Y\"\n\n" +
		"[[sites]]\nname = \"ok\"\nhosts = [\"a.test:80\"]\ndatabase_url_env = \"OK_URL\"\npool_size = 0\n"
	if err := os.WriteFile(bad, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runCLI(map[string]string{"FORGEBASE_DATABASE_URL": p.a.URL()}, "", "migrate", "--site", "ok", "--doctypes", doctypes, "--config", bad)
	expectCode(t, r, 1, "invalid config")
	expectContains(t, r.stderr,
		"invalid config file "+bad+":\n",
		"site `Bad`: the name must be",
		"site `Bad`: host \"a.test:80\"",
		"site `Bad`: `database_url_env` must be",
		"site `ok`: host \"a.test:80\"",
		"site `ok`: `pool_size` is 0")
	if strings.Contains(r.stderr, p.a.URL()) {
		t.Errorf("the URL is printed: %s", r.stderr)
	}
}

// Rust: sites.rs::an_unset_url_variable_is_an_error_naming_the_site_and_never_a_value
func TestAnUnsetURLVariableIsAnErrorNamingTheSiteAndNeverAValue(t *testing.T) {
	p := newPair(t)
	vars := map[string]string{aURL: p.a.URL()}
	config := filepath.Join(p.dir, "forgebase.toml")
	r := runCLI(vars, "", "--config", config, "migrate", "--site", "b", "--doctypes", doctypes)
	expectCode(t, r, 1, "unset")
	expectContains(t, r.stderr, "site `b`", bURL)
	// The URL of the other site is in the environment, and is not printed.
	if strings.Contains(r.stderr, p.a.URL()) || strings.Contains(r.stderr, "forgebase:forgebase") {
		t.Errorf("%s", r.stderr)
	}
	// An empty value is the same error.
	vars[bURL] = ""
	r = runCLI(vars, "", "--config", config, "migrate", "--site", "b", "--doctypes", doctypes)
	expectCode(t, r, 1, "empty")
	expectContains(t, r.stderr, "site `b`")
}

// Rust: sites.rs::commands_touch_only_the_database_of_their_site
func TestCommandsTouchOnlyTheDatabaseOfTheirSite(t *testing.T) {
	p := newPair(t)
	expectCode(t, p.run("migrate", "--site", "a", "--doctypes", doctypes), 0, "migrate a")
	const migrations = "SELECT count(*)::text FROM fb_migration"
	if got := scalar(t, p.a, migrations); got != "13" {
		t.Fatalf("a: %s", got)
	}
	for _, table := range []string{"fb_migration", "doc_customer"} {
		if got := scalar(t, p.b, "SELECT coalesce(to_regclass('"+table+"')::text, '-')"); got != "-" {
			t.Fatalf("b has %s", table)
		}
	}
	expectCode(t, p.run("migrate", "--site", "b", "--doctypes", doctypes), 0, "migrate b")
	if got := scalar(t, p.b, migrations); got != "13" {
		t.Fatalf("b: %s", got)
	}

	customer := `{"email":"ann@example.com","full_name":"Ann"}`
	expectCode(t, p.run("doc", "insert", "customer", "--doctypes", doctypes, "--site", "a", "--data", customer), 0, "insert on a")
	const count = "SELECT count(*)::text FROM doc_customer"
	if scalar(t, p.a, count) != "1" || scalar(t, p.b, count) != "0" {
		t.Fatalf("a %s b %s", scalar(t, p.a, count), scalar(t, p.b, count))
	}
	get := func(site string) result {
		return p.run("doc", "get", "customer", "ann@example.com", "--doctypes", doctypes, "--site", site)
	}
	expectCode(t, get("a"), 0, "get on a")
	expectCode(t, get("b"), 1, "get on b")
	// Users and tokens too: ann exists on A only.
	expectCode(t, p.runStdin("a password 123\n", "user", "create", "ann", "--site", "a"), 0, "user on a")
	expectCode(t, p.run("token", "create", "ann", "--site", "a"), 0, "token on a")
	expectCode(t, p.run("token", "create", "ann", "--site", "b"), 1, "no user ann on site b")
	// And jobs: a job enqueued on A is not in the queue of B.
	expectCode(t, p.run("job", "enqueue", "forgebase.purge_sessions", "--site", "a"), 0, "job on a")
	if got := scalar(t, p.a, "SELECT count(*)::text FROM fb_job"); got != "1" {
		t.Errorf("a has %s jobs", got)
	}
	if got := scalar(t, p.b, "SELECT count(*)::text FROM fb_job"); got != "0" {
		t.Errorf("b has %s jobs", got)
	}
}

// workflowDoctypes is the WORKFLOW_DOCTYPES fixture of the Rust CLI tests (tests/fixtures/workflow).
const workflowDoctypes = "testdata/workflow"

// Rust: cli.rs::doc_transition_takes_a_workflow_transition_as_the_system_context
func TestDocTransitionTakesAWorkflowTransitionAsTheSystemContext(t *testing.T) {
	e := newEnv(t)
	expectCode(t, e.run("migrate", "--doctypes", workflowDoctypes), 0, "migrate")
	run := func(args ...string) result { return e.run(append(args, "--doctypes", workflowDoctypes)...) }
	inserted := run("doc", "insert", "purchase", "--data", `{"title":"p"}`)
	expectCode(t, inserted, 0, "insert")
	purchase := inserted.json(t)
	name := purchase["name"].(string)
	if purchase["workflow_state"] != "Draft" || purchase["docstatus"] != float64(0) {
		t.Fatalf("%v", purchase)
	}

	// The roles are not checked: the actor has none, and Approve names a role nobody has.
	sent := run("doc", "transition", "purchase", name, "Send", "--version", "1", "--actor", "ann")
	expectCode(t, sent, 0, "send")
	got := sent.json(t)
	if got["workflow_state"] != "Pending" || got["version"] != float64(2) || got["modified_by"] != "ann" {
		t.Errorf("%v", got)
	}
	// doc get prints the state too.
	if state := run("doc", "get", "purchase", name).json(t)["workflow_state"]; state != "Pending" {
		t.Errorf("%v", state)
	}

	// Errors and exit codes are those of doc submit: 1 with the message on standard error.
	stale := run("doc", "transition", "purchase", name, "Approve", "--version", "9")
	expectCode(t, stale, 1, "stale")
	expectContains(t, stale.stderr, "current version 2")
	wrong := run("doc", "transition", "purchase", name, "Cancel", "--version", "2")
	expectCode(t, wrong, 1, "wrong state")
	expectContains(t, wrong.stderr, "is in state `Pending`: no transition `Cancel` from it")
	// Without --version it is a usage error (exit 2).
	expectCode(t, run("doc", "transition", "purchase", name, "Approve"), 2, "no version")

	// Plain submit and cancel are refused on a DocType with a workflow.
	for _, step := range []string{"submit", "cancel"} {
		refused := run("doc", step, "purchase", name, "--version", "2")
		expectCode(t, refused, 1, step)
		expectContains(t, refused.stderr, "has a workflow; use a transition")
	}
	// Approve submits the document, Cancel cancels it.
	approved := run("doc", "transition", "purchase", name, "Approve", "--version", "2").json(t)
	if approved["workflow_state"] != "Approved" || approved["docstatus"] != float64(1) {
		t.Errorf("%v", approved)
	}
	cancelled := run("doc", "transition", "purchase", name, "Cancel", "--version", "3").json(t)
	if cancelled["workflow_state"] != "Cancelled" || cancelled["docstatus"] != float64(2) {
		t.Errorf("%v", cancelled)
	}
	// The log has every transition with its actor.
	if got := e.scalar("SELECT string_agg(action || ':' || actor, ',' ORDER BY id) FROM fb_workflow_log"); got != "Send:ann,Approve:cli,Cancel:cli" {
		t.Errorf("%s", got)
	}

	// A DocType without a workflow: transition says so.
	expectCode(t, e.run("migrate", "--doctypes", doctypes), 0, "migrate the other")
	none := e.run("doc", "transition", "customer", "x", "Send", "--version", "1", "--doctypes", doctypes)
	expectCode(t, none, 1, "no workflow")
}

// Rust: cli.rs::migrate_gives_documents_their_state_and_warns_about_a_state_that_is_gone
func TestMigrateGivesDocumentsTheirStateAndWarnsAboutAStateThatIsGone(t *testing.T) {
	e := newEnv(t)
	// The DocType without its workflow: a draft, a submitted and a cancelled document.
	dir := t.TempDir()
	text, err := os.ReadFile(workflowDoctypes + "/purchase.toml")
	if err != nil {
		t.Fatal(err)
	}
	without, _, _ := strings.Cut(string(text), "[workflow]")
	if err := os.WriteFile(dir+"/purchase.toml", []byte(without), 0o600); err != nil {
		t.Fatal(err)
	}
	expectCode(t, e.run("migrate", "--doctypes", dir), 0, "migrate without the workflow")
	for range 3 {
		expectCode(t, e.run("doc", "insert", "purchase", "--data", `{"title":"p"}`, "--doctypes", dir), 0, "insert")
	}
	e.d.Exec("UPDATE doc_purchase SET docstatus = 1 WHERE name = (SELECT name FROM doc_purchase ORDER BY created_at, id LIMIT 1 OFFSET 1)")
	e.d.Exec("UPDATE doc_purchase SET docstatus = 2 WHERE name = (SELECT name FROM doc_purchase ORDER BY created_at, id LIMIT 1 OFFSET 2)")

	// Adding the workflow: the column is added and the documents get a state per docstatus.
	migrate := e.run("migrate", "--doctypes", workflowDoctypes)
	expectCode(t, migrate, 0, "migrate with the workflow")
	expectContains(t, migrate.stdout, "workflow_state")
	if migrate.stderr != "" {
		t.Errorf("%q", migrate.stderr)
	}
	if got := e.scalar("SELECT string_agg(docstatus::text || '=' || workflow_state, ',' ORDER BY docstatus) FROM doc_purchase"); got != "0=Draft,1=Approved,2=Cancelled" {
		t.Errorf("%s", got)
	}
	// Again: no change, no warning.
	again := e.run("migrate", "--doctypes", workflowDoctypes)
	expectCode(t, again, 0, "migrate again")
	if again.stderr != "" {
		t.Errorf("%q", again.stderr)
	}

	// A state the workflow no longer has: one warning line on standard error, exit 0.
	e.d.Exec("UPDATE doc_purchase SET workflow_state = 'Gone' WHERE docstatus > 0")
	warned := e.run("migrate", "--doctypes", workflowDoctypes)
	expectCode(t, warned, 0, "migrate with a state that is gone")
	const want = "warning: site `default`: DocType `purchase`: 2 documents are in a workflow state that no longer exists; no transition applies to them\n"
	if warned.stderr != want {
		t.Errorf("%q", warned.stderr)
	}
}
