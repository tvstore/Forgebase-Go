package forgebase_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// A binary with apps, end to end (M5.1): forgebase.New().App(..).Run() with real command-line arguments
// against a real PostgreSQL. Ported from crates/forgebase/tests/app_binary.rs (the parts that need only the
// app model: migrate and its dry run, fb_app, hooks of an extending app for the doc commands, the
// --doctypes directory as the app local, a startup error for every command) and, since P5, the job commands
// and the worker, and, since P6, the check of the reports at migrate. Deferred: the fixtures (P7) of that file.
//
// The test binary runs itself again as the application binary (selected by the environment variable
// FORGEBASE_TEST_BINARY) for every command: TestMain below.

const binaryVar = "FORGEBASE_TEST_BINARY"

const binaryCustomer = "name = \"customer\"\nlabel = \"Customer\"\nnaming = { field = \"code\" }\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n" +
	"[[fields]]\nname = \"title\"\ntype = \"data\"\n"

const crmSeed = "CREATE TABLE crm_seed (entry text); INSERT INTO crm_seed VALUES ('seeded')"

// noBanned rejects a customer titled banned.
type noBanned struct{ forgebase.BaseHooks }

func (noBanned) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if doc.Values["title"].Equal(forgebase.Text("banned")) {
		return forgebase.NewHookError("a banned customer is not allowed")
	}
	return nil
}

// touch is crm.touch: it inserts a customer with the code of its payload. fail is crm.fail: it never works.
func touch(ctx context.Context, job *forgebase.JobContext) error {
	var p struct {
		Code string `json:"code"`
	}
	code := "none"
	if err := job.DecodePayload(&p); err == nil && p.Code != "" {
		code = p.Code
	}
	_, err := job.Documents().Insert(ctx, "customer", forgebase.Document{Values: map[string]forgebase.Value{"code": forgebase.Text(code)}})
	return err
}

func fail(context.Context, *forgebase.JobContext) error {
	return forgebase.NewPermanentJobError("this job never works")
}

func binaryCRM() *forgebase.App {
	return forgebase.NewApp("crm").
		Job("touch", touch).
		Job("fail", fail).
		Version("1.2.3").
		DocTypes(docTypes("doctypes/customer.toml", binaryCustomer)).
		Migrations(migrations(mig{1, "seed", crmSeed}))
}

func binarySales() *forgebase.App {
	return forgebase.NewApp("sales").DependsOn("crm").Hooks("customer", noBanned{})
}

// binaryCRMWithReport is crm with a report on its customers. The bad one declares one column where the SQL
// returns two.
func binaryCRMWithReport(valid bool) *forgebase.App {
	report := forgebase.NewReport("SELECT name, title FROM doc_customer WHERE ($1::text IS NULL OR name = $1);\n").
		Roles("Manager").
		Param(forgebase.LinkParam("customer", "customer")).
		Column(forgebase.LinkColumn("customer", "customer"))
	if valid {
		report.Column(forgebase.DataColumn("title"))
	}
	return binaryCRM().Report("totals", report)
}

func TestMain(m *testing.M) {
	switch os.Getenv(binaryVar) {
	case "reports_ok":
		os.Exit(forgebase.New().App(binaryCRMWithReport(true)).Run())
	case "reports_bad":
		os.Exit(forgebase.New().App(binaryCRMWithReport(false)).Run())
	case "good":
		os.Exit(forgebase.New().App(binaryCRM()).App(binarySales()).Run())
	case "sites": // two apps for the sites of site_workers_test.go
		os.Exit(forgebase.New().App(siteBase()).App(siteExtra()).Run())
	case "stubborn": // a job whose handler ignores its context, for stubborn_test.go
		os.Exit(forgebase.New().App(forgebase.NewApp("lab").Job("stubborn", func(context.Context, *forgebase.JobContext) error {
			time.Sleep(time.Minute)
			return nil
		})).Run())
	case "broken": // sales before crm: a startup error
		os.Exit(forgebase.New().App(binarySales()).App(binaryCRM()).Run())
	}
	os.Exit(m.Run())
}

type binaryRun struct {
	t    *testing.T
	mode string
	url  string
}

type output struct {
	code           int
	stdout, stderr string
}

func (r binaryRun) run(stdin string, args ...string) output {
	r.t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), binaryVar+"="+r.mode, "FORGEBASE_DATABASE_URL="+r.url)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			r.t.Fatalf("cannot start the app binary: %v", err)
		}
		code = exit.ExitCode()
	}
	return output{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r binaryRun) succeeds(stdin string, args ...string) string {
	r.t.Helper()
	o := r.run(stdin, args...)
	if o.code != 0 {
		r.t.Fatalf("%v: exit code %d, stderr: %s", args, o.code, o.stderr)
	}
	return o.stdout
}

func contains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("missing %q in:\n%s", p, text)
		}
	}
}

// Rust: app_binary.rs::drive (the app model parts)
func TestABinaryWithAppsRunsItsCommands(t *testing.T) {
	db := fbtest.NewDB(t)
	good := binaryRun{t: t, mode: "good", url: db.URL()}

	// A dry run on an empty database lists the app migration, and keeps nothing.
	out := good.succeeds("", "migrate", "--dry-run")
	contains(t, out, "framework migrations: 13 pending", "app migrations: 1 pending\n  crm 0001 seed\n")
	if strings.Contains(out, "applied") {
		t.Errorf("a dry run applied something:\n%s", out)
	}

	// migrate needs no --doctypes: the apps bring their DocTypes.
	out = good.succeeds("", "migrate")
	contains(t, out, "schema sync: applied", "app migrations: 1 applied\n  crm 0001 seed\n")
	got := db.Rows("SELECT name, version FROM fb_app ORDER BY name")
	if len(got) != 2 || got[0][0] != "crm" || got[0][1] != "1.2.3" || got[1][0] != "sales" || got[1][1] != "NULL" {
		t.Errorf("fb_app %v", got)
	}
	contains(t, good.succeeds("", "migrate"), "app migrations: up to date\n")

	// Documents: the hook of sales (registered on crm's DocType) runs for doc insert.
	rejected := good.run("", "doc", "insert", "customer", "--data", `{"code":"c-1","title":"banned"}`)
	if rejected.code != 1 || !strings.HasPrefix(rejected.stderr, "error: ") || !strings.Contains(rejected.stderr, "a banned customer is not allowed") {
		t.Errorf("%+v", rejected)
	}
	contains(t, good.succeeds("", "doc", "insert", "customer", "--data", `{"code":"c-2","title":"fine"}`), `"name": "c-2"`)
	contains(t, good.succeeds("", "doc", "get", "customer", "c-2"), `"title": "fine"`)

	// --doctypes DIR is one more app, local, that may link to every app's DocTypes.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.toml"),
		"name = \"note\"\nlabel = \"Note\"\n[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"customer\"\n")
	contains(t, good.succeeds("", "migrate", "--doctypes", dir), "schema sync: applied")
	contains(t, good.succeeds("", "doc", "insert", "note", "--doctypes", dir, "--data", `{"customer":"c-2"}`), `"customer": "c-2"`)
	// A command that does not read DocTypes works too; an invalid file is a startup error.
	good.succeeds("a long password\n", "user", "create", "ann")
	writeFile(t, filepath.Join(dir, "broken.toml"), "name = ")
	o := good.run("", "migrate", "--doctypes", dir)
	if o.code != 1 || !strings.HasPrefix(o.stderr, "error: cannot load DocType files:\n  - ") || !strings.Contains(o.stderr, "broken.toml") {
		t.Errorf("%+v", o)
	}

	// Apps in the wrong order: exit code 1 and the problem, for every command, before any database work (the
	// URL here does not even point to a server).
	broken := binaryRun{t: t, mode: "broken", url: "postgres://unused:unused@127.0.0.1:1/unused"}
	for _, args := range [][]string{{"migrate"}, {"user", "create", "ann"}, {"serve"}, {"token", "create", "ann"}, {"doc", "list", "customer"}} {
		o := broken.run("a long password\n", args...)
		if o.code != 1 || !strings.HasPrefix(o.stderr, "error: invalid app setup:\n  - app `sales` depends on `crm`, which is registered after it;") || o.stdout != "" {
			t.Errorf("%v: %+v", args, o)
		}
	}
}

// enqueuedID is the id in `enqueued job <id>`.
func enqueuedID(t *testing.T, out string) string {
	t.Helper()
	id, ok := strings.CutPrefix(strings.TrimSpace(out), "enqueued job ")
	if !ok {
		t.Fatalf("unexpected output: %s", out)
	}
	return id
}

// Rust: app_binary.rs::jobs (the job commands and the worker, with the jobs of crm)
func TestTheJobCommandsAndTheWorkerOfABinaryWithApps(t *testing.T) {
	db := fbtest.NewDB(t)
	good := binaryRun{t: t, mode: "good", url: db.URL()}
	good.succeeds("", "migrate")

	// An unknown job is refused; the payload must be JSON.
	out := good.run("", "job", "enqueue", "crm.nobody")
	if out.code != 1 || !strings.HasPrefix(out.stderr, "error: unknown job `crm.nobody`") {
		t.Errorf("%+v", out)
	}
	out = good.run("", "job", "enqueue", "crm.touch", "--payload", "{")
	if out.code != 1 || !strings.Contains(out.stderr, "not valid JSON") {
		t.Errorf("%+v", out)
	}
	expect(t, db.Rows("SELECT count(*)::text FROM fb_job"), [][]string{{"0"}})

	// The payload as an argument, from standard input and the default; --actor is recorded.
	first := enqueuedID(t, good.succeeds("", "job", "enqueue", "crm.touch", "--payload", `{"code":"j-1"}`))
	second := enqueuedID(t, good.succeeds("{\"code\":\"j-2\"}\n", "job", "enqueue", "crm.touch", "--payload", "-", "--actor", "ann"))
	dying := enqueuedID(t, good.succeeds("", "job", "enqueue", "crm.fail"))
	expect(t, db.Rows("SELECT enqueued_by FROM fb_job ORDER BY id"), [][]string{{"cli"}, {"ann"}, {"cli"}})

	// List: a table, newest first, with filters.
	list := good.succeeds("", "job", "list")
	lines := strings.Split(strings.TrimSuffix(list, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "LAST_ERROR") {
		t.Fatalf("%s", list)
	}
	if !strings.Contains(lines[1], dying) || !strings.Contains(lines[1], "crm.fail") || !strings.Contains(lines[1], "queued") {
		t.Errorf("%s", list)
	}
	if n := strings.Count(good.succeeds("", "job", "list", "--name", "crm.touch", "--limit", "1"), "\n"); n != 2 {
		t.Errorf("%d lines", n)
	}
	if n := strings.Count(good.succeeds("", "job", "list", "--status", "dead"), "\n"); n != 1 {
		t.Errorf("%d lines", n)
	}
	for _, args := range [][]string{{"job", "list", "--status", "bogus"}, {"job", "list", "--limit", "0"}} {
		if code := good.run("", args...).code; code != 2 {
			t.Errorf("%v: exit code %d", args, code)
		}
	}

	// Show: every field as JSON, the payload included.
	var shown map[string]any
	if err := json.Unmarshal([]byte(good.succeeds("", "job", "show", first)), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["id"] != first || shown["status"] != "queued" || shown["finished_at"] != nil || shown["payload"].(map[string]any)["code"] != "j-1" {
		t.Errorf("%v", shown)
	}
	if code := good.run("", "job", "show", "00000000-0000-7000-8000-000000000000").code; code != 1 {
		t.Errorf("exit code %d", code)
	}
	if o := good.run("", "job", "show", "nonsense"); o.code != 1 || o.stderr != "error: job: `nonsense` is not a job id (a UUID)\n" {
		t.Errorf("%+v", o)
	}

	// Only a dead job is retried.
	out = good.run("", "job", "retry", first)
	if out.code != 1 || !strings.Contains(out.stderr, "only a dead job can be retried") {
		t.Errorf("%+v", out)
	}

	// The worker runs them, and stops cleanly on SIGTERM. The schedules are removed first: a built-in
	// clean-up job that falls due while this test runs (at the turn of the hour) would add a job to the
	// counts below.
	db.Exec("DELETE FROM fb_schedule")
	cmd := exec.Command(os.Args[0], "worker", "--concurrency", "2")
	cmd.Env = append(os.Environ(), binaryVar+"=good", "FORGEBASE_DATABASE_URL="+db.URL())
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		done := db.Rows("SELECT count(*)::text FROM fb_job WHERE status IN ('done', 'dead')")[0][0]
		if done == "3" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker did not finish: %s\n%s", done, log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err != nil {
		t.Fatalf("the worker exited with %v:\n%s", err, log.String())
	}
	text := log.String()
	contains(t, text, "started ", "concurrency 2", "stopped", fmt.Sprintf("job crm.touch %s done", first), "dead after attempt 1: this job never works")
	expect(t, db.Rows("SELECT code FROM doc_customer WHERE code LIKE 'j-%' ORDER BY code"), [][]string{{"j-1"}, {"j-2"}})
	contains(t, good.succeeds("", "job", "list", "--status", "dead"), dying, "this job never works")
	contains(t, good.succeeds("", "job", "show", second), `"status": "done"`)

	// Retry: dead to queued.
	good.succeeds("", "job", "retry", dying)
	if err := json.Unmarshal([]byte(good.succeeds("", "job", "show", dying)), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["status"] != "queued" || shown["attempts"] != float64(0) || shown["last_error"] != nil || shown["finished_at"] != nil {
		t.Errorf("%v", shown)
	}
	// And a job of a done status is refused.
	if code := good.run("", "job", "retry", second).code; code != 1 {
		t.Errorf("exit code %d", code)
	}
}

// Rust: app_binary.rs::reports: the check of the reports' SQL at migrate (M8.4), after the migrations, never in a
// dry run; a mismatch is exit code 1 with the migrations kept.
func TestTheReportCheckAtMigrate(t *testing.T) {
	db := fbtest.NewDB(t)
	ok := binaryRun{t: t, mode: "reports_ok", url: db.URL()}
	// A dry run keeps nothing, so the tables the SQL reads may not exist: it checks nothing.
	if out := ok.succeeds("", "migrate", "--dry-run"); strings.Contains(out, "reports:") {
		t.Errorf("%s", out)
	}
	if out := ok.succeeds("", "migrate"); !strings.HasSuffix(out, "reports: 1 checked\n") {
		t.Errorf("%s", out)
	}

	db = fbtest.NewDB(t)
	bad := binaryRun{t: t, mode: "reports_bad", url: db.URL()}
	out := bad.run("", "migrate")
	if out.code != 1 {
		t.Fatalf("exit code %d", out.code)
	}
	// What was applied is reported, and it stays applied.
	contains(t, out.stdout, "framework migrations: 13 applied", "app migrations: 1 applied\n  crm 0001 seed\n")
	if !strings.HasPrefix(out.stderr, "error: the migrations were applied, but 1 report(s) do not fit their SQL:\n") {
		t.Errorf("%s", out.stderr)
	}
	contains(t, out.stderr, "report `crm.totals`: the SQL returns 2 column(s) but 1 are declared", "fix the report and run migrate again")
	if strings.Contains(out.stderr, "doc_customer") {
		t.Errorf("the SQL is printed: %s", out.stderr)
	}
	if got := db.Rows("SELECT name FROM fb_app"); len(got) != 1 || got[0][0] != "crm" {
		t.Errorf("%v", got)
	}
	again := bad.run("", "migrate")
	if again.code != 1 {
		t.Fatalf("exit code %d", again.code)
	}
	contains(t, again.stdout, "app migrations: up to date")
}
