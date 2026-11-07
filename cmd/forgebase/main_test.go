package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/webhooktest"
)

// The tests of the built binary: its exit codes, its standard streams and its environment, which the
// in-process tests of internal/app cannot show. The binary is built once per test run. Ported from
// crates/forgebase/tests/cli.rs, where every test runs the real binary.

var binary string

// doctypes is the DOCTYPES fixture of the Rust CLI tests, byte-identical (internal/app/testdata).
const doctypes = "../../internal/app/testdata/quickstart"

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "forgebase-binary-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "forgebase")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Stderr = os.Stderr
	code := 1
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot build the binary:", err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type output struct {
	code           int
	stdout, stderr string
}

// forgebase runs the binary with exactly the environment given (no inherited variables, so a
// FORGEBASE_* variable of the developer's shell never changes a test).
func runBinary(t *testing.T, env []string, stdin string, args ...string) output {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append([]string{}, env...) // never nil: nil would inherit the environment
	var out, errOut bytes.Buffer
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("cannot run the binary: %v", err)
		}
		code = exit.ExitCode()
	}
	return output{code: code, stdout: out.String(), stderr: errOut.String()}
}

// Rust: cli.rs::version_and_usage_errors
func TestVersionAndUsageErrors(t *testing.T) {
	r := runBinary(t, nil, "", "--version")
	if r.code != 0 || r.stdout != "forgebase "+forgebase.Version+"\n" || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	if r := runBinary(t, nil, "", "-V"); r.code != 0 || r.stdout != "forgebase "+forgebase.Version+"\n" {
		t.Fatalf("%+v", r)
	}
	for _, args := range [][]string{
		{"migrate"},
		{"doc", "get", "customer"},
		{"doc", "update", "customer", "x", "--doctypes", doctypes, "--data", "{}"},
		{"doc", "delete", "customer", "x", "--doctypes", doctypes},
		{"doc", "delete", "customer", "x", "--doctypes", doctypes, "--version", "abc"},
		{"bogus"},
		{},
	} {
		r := runBinary(t, nil, "", args...)
		if r.code != 2 || r.stdout != "" {
			t.Errorf("%v: exit code %d: %s", args, r.code, r.stderr)
		}
	}
	// Help on standard output with code 0; with no arguments the help is on standard error with code 2.
	if r := runBinary(t, nil, "", "doc", "--help"); r.code != 0 || !strings.Contains(r.stdout, "Usage: forgebase doc [OPTIONS] <COMMAND>") || r.stderr != "" {
		t.Errorf("%+v", r)
	}
	if r := runBinary(t, nil, ""); r.code != 2 || !strings.Contains(r.stderr, "Usage: forgebase [OPTIONS] <COMMAND>") || r.stdout != "" {
		t.Errorf("%+v", r)
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
		r := runBinary(t, nil, "", c.args...)
		if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "--doctypes <DIR>") || !strings.Contains(r.stderr, c.usage) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	help := runBinary(t, nil, "", "migrate", "--help")
	if help.code != 0 || !strings.Contains(help.stdout, "Required when the binary has no apps") {
		t.Errorf("%+v", help)
	}
}

// Rust: cli.rs::missing_database_url_is_a_clear_error
func TestMissingDatabaseURLIsAClearError(t *testing.T) {
	r := runBinary(t, nil, "", "migrate", "--doctypes", doctypes)
	if r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "error: FORGEBASE_DATABASE_URL is not set") {
		t.Fatalf("%+v", r)
	}
	// An empty variable is not set.
	r = runBinary(t, []string{"FORGEBASE_DATABASE_URL="}, "", "migrate", "--doctypes", doctypes)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: FORGEBASE_DATABASE_URL is not set") {
		t.Fatalf("%+v", r)
	}
}

// Rust: cli.rs::migrate_creates_tables_and_a_rerun_changes_nothing, insert_get_update_delete and
// insert_reads_data_from_standard_input_with_default_actor, through the real process and a real pipe.
func TestTheBinaryMigratesAndTakesDocumentsThroughStandardInput(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	run := func(stdin string, args ...string) output {
		return runBinary(t, env, stdin, append(args, "--doctypes", doctypes)...)
	}

	first := run("", "migrate")
	if first.code != 0 || !strings.Contains(first.stdout, "framework migrations: 13 applied") || first.stderr != "" {
		t.Fatalf("%+v", first)
	}
	second := run("", "migrate")
	if second.code != 0 || second.stdout != "framework migrations: up to date\nschema sync: up to date\n" {
		t.Fatalf("%+v", second)
	}

	inserted := run(`{"email":"s@example.com","full_name":"Sam"}`, "doc", "insert", "customer", "--data", "-")
	if inserted.code != 0 {
		t.Fatalf("%+v", inserted)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(inserted.stdout), &doc); err != nil || doc["owner"] != "cli" || doc["name"] != "s@example.com" {
		t.Fatalf("%v %s", err, inserted.stdout)
	}

	updated := run(`{"loyalty_points":6,"notes":null}`, "doc", "update", "customer", "s@example.com", "--version", "1", "--actor", "bob", "--data", "-")
	if updated.code != 0 || !strings.Contains(updated.stdout, `"modified_by": "bob"`) || !strings.Contains(updated.stdout, `"version": 2`) {
		t.Fatalf("%+v", updated)
	}
	got := run("", "doc", "get", "customer", "s@example.com")
	if got.code != 0 || got.stdout != updated.stdout {
		t.Fatalf("%+v", got)
	}
	// Standard input that never arrives is not needed without `-`: a closed stdin works.
	gone := run("", "doc", "delete", "customer", "s@example.com", "--version", "2", "--actor", "dee")
	if gone.code != 0 || gone.stdout != "" {
		t.Fatalf("%+v", gone)
	}
	missing := run("", "doc", "get", "customer", "s@example.com")
	if missing.code != 1 || !strings.HasPrefix(missing.stderr, "error: customer `s@example.com` not found") {
		t.Fatalf("%+v", missing)
	}
}

// Rust: cli.rs::user_commands_manage_users_and_roles and token_create_prints_the_secret_once, through
// the real process: the password comes from a real pipe, and the list reads what the commands wrote.
func TestTheBinaryReadsPasswordsFromAPipeAndPrintsTheTokenOnce(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	if r := runBinary(t, env, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	r := runBinary(t, env, "a long password\nignored\n", "user", "create", "alice", "--role", "Sales")
	if r.code != 0 || r.stdout != "created user alice\n" || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	r = runBinary(t, env, "", "token", "create", "alice")
	key, secret, ok := strings.Cut(strings.TrimSuffix(r.stdout, "\n"), ":")
	if r.code != 0 || !ok || len(key) != 32 || len(secret) != 64 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	if r := runBinary(t, env, "", "token", "revoke", key); r.code != 0 || r.stdout != "revoked API token "+key+"\n" {
		t.Fatalf("%+v", r)
	}
	r = runBinary(t, env, "", "doc", "list", "customer", "--doctypes", doctypes)
	if r.code != 0 || r.stdout != "[]\n" {
		t.Fatalf("%+v", r)
	}
}

// Rust: cli.rs::a_secret_from_standard_input_is_used_and_not_printed_and_rotation_prints_once and
// webhook_test_sends_one_signed_delivery_and_exits_by_the_answer, through the real process: the secret comes
// from a real pipe, and the exit code of `webhook test` follows the answer of a real receiver.
func TestTheBinaryTakesAWebhookSecretFromAPipeAndTestExitsByTheAnswer(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	if r := runBinary(t, env, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	add := runBinary(t, env, "a secret of more than 16 bytes\nignored\n", "webhook", "add", "w", "--doctype", "customer",
		"--events", "insert", "--url", receiver.URL("/hook?token=TOPSECRET"), "--allow-private", "--insecure-http",
		"--secret-stdin", "--doctypes", doctypes)
	if add.code != 0 || strings.Contains(add.stdout, "a secret of more") || !strings.Contains(add.stdout, "the secret is the one read from standard input") ||
		!strings.Contains(add.stdout, "warning: --insecure-http") || add.stderr != "" {
		t.Fatalf("%+v", add)
	}
	ok := runBinary(t, env, "", "webhook", "test", "w")
	if ok.code != 0 || !strings.Contains(ok.stdout, "answered HTTP 200") || ok.stderr != "" {
		t.Fatalf("%+v", ok)
	}
	requests := receiver.Requests()
	if len(requests) != 1 || requests[0].Header("x-forgebase-signature") !=
		webhooktest.ExpectedSignature("a secret of more than 16 bytes", requests[0].Header("x-forgebase-timestamp"), requests[0].Body) {
		t.Fatalf("%v", requests)
	}
	receiver.SetReply(webhooktest.Status(503))
	failed := runBinary(t, env, "", "webhook", "test", "w")
	if failed.code != 1 || !strings.Contains(failed.stdout, "answered HTTP 503") || !strings.Contains(failed.stderr, "error: the test delivery was not accepted: HTTP 503") {
		t.Fatalf("%+v", failed)
	}
	for _, text := range []string{failed.stdout, failed.stderr} {
		if strings.Contains(text, "TOPSECRET") || strings.Contains(text, "a secret of more") {
			t.Errorf("%s", text)
		}
	}
}

// Rust: cli.rs::invalid_doctype_files_exit_1_naming_the_file, through the real process.
func TestInvalidDoctypeFilesExit1NamingTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("name = "), 0o600); err != nil {
		t.Fatal(err)
	}
	// Loading fails before any connection is made, so the URL is never used.
	r := runBinary(t, []string{"FORGEBASE_DATABASE_URL=postgres://unused:unused@127.0.0.1:1/unused"}, "", "migrate", "--doctypes", dir)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: cannot load DocType files:\n") || !strings.Contains(r.stderr, "broken.toml") {
		t.Fatalf("%+v", r)
	}
}
