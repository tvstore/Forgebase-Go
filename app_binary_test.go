package forgebase_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// A binary with apps, end to end (M5.1): forgebase.New().App(..).Run() with real command-line arguments
// against a real PostgreSQL. Ported from crates/forgebase/tests/app_binary.rs (the parts that need only the
// app model: migrate and its dry run, fb_app, hooks of an extending app for the doc commands, the
// --doctypes directory as the app local, a startup error for every command). Deferred: the jobs (P5),
// the reports (P6) and the fixtures (P7) of that file.
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

func binaryCRM() *forgebase.App {
	return forgebase.NewApp("crm").Version("1.2.3").
		DocTypes(docTypes("doctypes/customer.toml", binaryCustomer)).
		Migrations(migrations(mig{1, "seed", crmSeed}))
}

func binarySales() *forgebase.App {
	return forgebase.NewApp("sales").DependsOn("crm").Hooks("customer", noBanned{})
}

func TestMain(m *testing.M) {
	switch os.Getenv(binaryVar) {
	case "good":
		os.Exit(forgebase.New().App(binaryCRM()).App(binarySales()).Run())
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
