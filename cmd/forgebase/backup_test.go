package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// `site backup` and `site restore` (M7.3, ADR 0014): the built binary against real PostgreSQL databases and
// the real pg_dump and pg_restore of the machine. Ported from crates/forgebase/tests/backup.rs.
//
// The PostgreSQL client tools of the server's major version (on PATH, or in the directory of
// FORGEBASE_PG_BIN, which the tests pass on to the binary) are required: a test that cannot find them fails
// with the tool's name (it never skips). Every test makes its own throwaway fb_test_backup_* databases and
// needs FORGEBASE_TEST_DATABASE_URL; see CONTRIBUTING.md.

const (
	srcURLVar = "FB_TEST_BACKUP_SRC_URL"
	dstURLVar = "FB_TEST_BACKUP_DST_URL"
	annPass   = "a password for ann"
)

var backupCounter atomic.Uint32

// uniqueName is a database name no test run uses twice.
func uniqueName() string {
	return fmt.Sprintf("fb_test_backup_%x_%x_%d", os.Getpid(), time.Now().Nanosecond(), backupCounter.Add(1)-1)
}

// bdb is the URL of a database that may or may not exist yet; it is dropped when the test ends.
type bdb struct {
	t     *testing.T
	admin *dbtest.DB
	name  string
	url   string
}

// namedDB is only the name: the database is made by `site create` or by created.
func namedDB(t *testing.T, admin *dbtest.DB) *bdb {
	t.Helper()
	name := uniqueName()
	d := &bdb{t: t, admin: admin, name: name, url: dbtest.URLForDatabase(t, name)}
	t.Cleanup(func() { _ = admin.TryExec("DROP DATABASE IF EXISTS " + db.QuoteIdent(name) + " WITH (FORCE)") })
	return d
}

// createdDB is a database that exists and is empty.
func createdDB(t *testing.T, admin *dbtest.DB) *bdb {
	t.Helper()
	d := namedDB(t, admin)
	admin.Exec("CREATE DATABASE " + db.QuoteIdent(d.name))
	return d
}

// scalar is the first column of the first row, or "<NULL>".
func (d *bdb) scalar(sql string) string {
	d.t.Helper()
	pool, err := db.Connect(context.Background(), d.url)
	if err != nil {
		d.t.Fatal(err)
	}
	defer pool.Close()
	var s *string
	if err := pool.QueryRow(context.Background(), sql).Scan(&s); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	if s == nil {
		return "<NULL>"
	}
	return *s
}

// backupWorld is two sites, src and dst, a config file and a directory for the backup files.
type backupWorld struct {
	t        *testing.T
	src, dst *bdb
	dir      string
}

// newBackupWorld: dst does not exist yet.
func newBackupWorld(t *testing.T) *backupWorld {
	t.Helper()
	admin := dbtest.New(t)
	w := &backupWorld{t: t, src: createdDB(t, admin), dst: namedDB(t, admin), dir: t.TempDir()}
	config := "[[sites]]\nname = \"src\"\nhosts = [\"src.localhost\"]\ndatabase_url_env = \"" + srcURLVar + "\"\n\n" +
		"[[sites]]\nname = \"dst\"\nhosts = [\"dst.localhost\"]\ndatabase_url_env = \"" + dstURLVar + "\"\n"
	if err := os.WriteFile(filepath.Join(w.dir, "forgebase.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *backupWorld) file(name string) string { return filepath.Join(w.dir, name) }

// env is the environment of the binary: no inherited FORGEBASE_* variable, the PATH, the tools' directory
// when the tests have one, and the URLs of the two sites; extra entries win.
func (w *backupWorld) env(extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), srcURLVar + "=" + w.src.url, dstURLVar + "=" + w.dst.url}
	if bin := os.Getenv("FORGEBASE_PG_BIN"); bin != "" {
		env = append(env, "FORGEBASE_PG_BIN="+bin)
	}
	return append(env, extra...)
}

func (w *backupWorld) args(args []string) []string {
	return append([]string{"--config", filepath.Join(w.dir, "forgebase.toml")}, args...)
}

func (w *backupWorld) run(args ...string) output {
	return runBinary(w.t, w.env(), "", w.args(args)...)
}

func (w *backupWorld) runWith(env []string, args ...string) output {
	return runBinary(w.t, w.env(env...), "", w.args(args)...)
}

func (w *backupWorld) ok(args ...string) string {
	w.t.Helper()
	r := w.run(args...)
	if r.code != 0 {
		w.t.Fatalf("%v: code %d: %s", args, r.code, r.stderr)
	}
	return r.stdout
}

// fails: the command failed with exit code 1; returns its error text.
func (w *backupWorld) fails(r output) string {
	w.t.Helper()
	if r.code != 1 {
		w.t.Fatalf("code %d, stdout: %s stderr: %s", r.code, r.stdout, r.stderr)
	}
	return r.stderr
}

// fillSrc migrates src, gives it a user and a few documents.
func (w *backupWorld) fillSrc() {
	w.t.Helper()
	w.ok("migrate", "--site", "src", "--doctypes", doctypes)
	r := runBinary(w.t, w.env(), annPass+"\n", w.args([]string{"user", "create", "ann", "--role", "Sales User", "--site", "src"})...)
	if r.code != 0 {
		w.t.Fatalf("user create: %s", r.stderr)
	}
	for _, data := range []string{
		`{"email":"a@example.com","full_name":"Ann","credit_limit":"1234.50","tier":"gold","joined_on":"2024-03-15"}`,
		`{"email":"b@example.com","full_name":"Bob","notes":"café ☕","loyalty_points":7}`,
	} {
		w.ok("doc", "insert", "customer", "--site", "src", "--doctypes", doctypes, "--data", data)
	}
	w.ok("doc", "insert", "sales_order", "--site", "src", "--doctypes", doctypes, "--data",
		`{"customer":"a@example.com","order_date":"2024-04-01","items":[{"description":"Pen","qty":3,"rate":"1.25"},{"description":"Ink","qty":1,"rate":"9.00"}]}`)
}

// documents is every document of the DocTypes, as `doc list` and `doc get` print them, for site.
func (w *backupWorld) documents(site string) string {
	w.t.Helper()
	var all strings.Builder
	for _, doctype := range []string{"customer", "sales_order"} {
		list := w.ok("doc", "list", doctype, "--site", site, "--doctypes", doctypes, "--limit", "500")
		for _, name := range docNames(w.t, list) {
			all.WriteString(w.ok("doc", "get", doctype, name, "--site", site, "--doctypes", doctypes))
		}
		all.WriteString(list)
	}
	return all.String()
}

func docNames(t *testing.T, list string) []string {
	t.Helper()
	var docs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(list), &docs); err != nil {
		t.Fatalf("%v: %s", err, list)
	}
	names := make([]string, len(docs))
	for i, d := range docs {
		names[i] = d.Name
	}
	return names
}

// Rust: backup.rs::a_backup_restored_into_an_empty_database_gives_the_same_documents
func TestABackupRestoredIntoAnEmptyDatabaseGivesTheSameDocuments(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	before := w.documents("src")
	if !strings.Contains(before, "a@example.com") || !strings.Contains(before, "Pen") {
		t.Fatalf("%s", before)
	}
	file := w.file("src.dump")
	out := w.ok("site", "backup", "src", "--out", file)
	if !strings.HasPrefix(out, "backed up site `src` to "+file+" (") || !strings.HasSuffix(out, " bytes)\n") || strings.Contains(out, "forgebase:forgebase") {
		t.Errorf("%s", out)
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() == 0 {
		t.Fatalf("%v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o: a backup holds password hashes", info.Mode().Perm())
	}

	// The new, empty database of the restore (made without migrating).
	out = w.ok("site", "create", "dst", "--empty")
	if !strings.HasPrefix(out, "created the database of site `dst`\n") || !strings.Contains(out, "site restore dst") {
		t.Errorf("%s", out)
	}
	if got := w.dst.scalar("SELECT to_regclass('fb_migration')::text"); got != "<NULL>" {
		t.Errorf("--empty migrated: %s", got)
	}
	r := w.run("site", "restore", "dst", "--from", file)
	if r.code != 0 {
		t.Fatalf("%s", r.stderr)
	}
	if !strings.Contains(r.stdout, "run `forgebase migrate --site dst` if this binary is newer than the backup") || strings.Contains(r.stdout, "forgebase:forgebase") {
		t.Errorf("%s", r.stdout)
	}

	// The same documents, with their child rows, and the same users; migrate has nothing to do.
	if after := w.documents("dst"); after != before {
		t.Errorf("the documents differ:\n%s\n---\n%s", before, after)
	}
	for _, sql := range []string{
		"SELECT count(*)::text FROM fb_user",
		"SELECT string_agg(username || ':' || password_hash, ',' ORDER BY username) FROM fb_user",
		"SELECT count(*)::text FROM fb_migration",
		"SELECT count(*)::text FROM doc_sales_order_item",
	} {
		if a, b := w.src.scalar(sql), w.dst.scalar(sql); a == "<NULL>" || a != b {
			t.Errorf("%s: %q against %q", sql, a, b)
		}
	}
	if report := w.ok("migrate", "--site", "dst", "--doctypes", doctypes); !strings.Contains(report, "framework migrations: up to date") {
		t.Errorf("%s", report)
	}
	// The restored site is usable: a new document goes in.
	w.ok("doc", "insert", "customer", "--site", "dst", "--doctypes", doctypes, "--data", `{"email":"c@example.com","full_name":"Cy"}`)
}

// Rust: backup.rs::restore_refuses_a_database_that_is_not_empty_and_changes_nothing
func TestRestoreRefusesADatabaseThatIsNotEmptyAndChangesNothing(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	file := w.file("src.dump")
	w.ok("site", "backup", "src", "--out", file)

	// `site create` without --empty migrates, so its database is not empty.
	w.ok("site", "create", "dst", "--doctypes", doctypes)
	const tables = "SELECT count(*)::text FROM pg_class WHERE relnamespace = 'public'::regnamespace"
	before := w.dst.scalar(tables)
	message := w.fails(w.run("site", "restore", "dst", "--from", file))
	if want := "error: the database of site `dst` is not empty; restore only into an empty database " +
		"(create one with `forgebase site create dst --empty`)\n"; message != want {
		t.Errorf("%q", message)
	}
	if w.dst.scalar(tables) != before {
		t.Error("the refused restore changed the database")
	}
	// The source database itself is not empty either.
	if message := w.fails(w.run("site", "restore", "src", "--from", file)); !strings.Contains(message, "is not empty") {
		t.Errorf("%s", message)
	}

	// A database that does not exist is told apart, with the way out.
	missing := namedDB(t, dbtest.New(t))
	message = w.fails(w.runWith([]string{dstURLVar + "=" + missing.url}, "site", "restore", "dst", "--from", file))
	if !strings.Contains(message, "does not exist") || !strings.Contains(message, "site create dst --empty") || strings.Contains(message, "forgebase:forgebase") {
		t.Errorf("%s", message)
	}

	// A file that is not a backup, and a path that is not a file.
	junk := w.file("junk.dump")
	if err := os.WriteFile(junk, []byte("this is not a dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := createdDB(t, dbtest.New(t))
	restore := func(from string) output {
		return w.runWith([]string{dstURLVar + "=" + empty.url}, "site", "restore", "dst", "--from", from)
	}
	if message := w.fails(restore(junk)); !strings.Contains(message, "is not a backup that pg_restore can read") {
		t.Errorf("%s", message)
	}
	if message := w.fails(restore(w.dir)); !strings.Contains(message, "is not a file") {
		t.Errorf("%s", message)
	}
	if got := empty.scalar("SELECT count(*)::text FROM pg_class WHERE relnamespace = 'public'::regnamespace"); got != "0" {
		t.Errorf("%s tables in the empty database", got)
	}
}

// Rust: backup.rs::a_backup_never_overwrites_a_file
func TestABackupNeverOverwritesAFile(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	file := w.file("keep.dump")
	if err := os.WriteFile(file, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	message := w.fails(w.run("site", "backup", "src", "--out", file))
	if message != "error: refusing to overwrite "+file+"\n" {
		t.Errorf("%q", message)
	}
	if data, _ := os.ReadFile(file); string(data) != "precious" {
		t.Errorf("%q", data)
	}

	// Neither does a second backup to the file of the first.
	second := w.file("second.dump")
	w.ok("site", "backup", "src", "--out", second)
	first, _ := os.ReadFile(second)
	if message := w.fails(w.run("site", "backup", "src", "--out", second)); !strings.Contains(message, "refusing to overwrite") {
		t.Errorf("%s", message)
	}
	if again, _ := os.ReadFile(second); string(again) != string(first) {
		t.Error("the first backup was changed")
	}

	// `--site` is not used by `site` commands; an unknown site is a usage error.
	r := w.run("site", "backup", "nope", "--out", w.file("n.dump"))
	if r.code != 2 {
		t.Errorf("code %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(w.file("n.dump")); err == nil {
		t.Error("a file for an unknown site")
	}
}

// Rust: backup.rs::a_missing_client_tool_is_a_clear_error_and_leaves_no_file
func TestAMissingClientToolIsAClearErrorAndLeavesNoFile(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	nowhere := w.file("no-bin")
	if err := os.MkdirAll(nowhere, 0o755); err != nil {
		t.Fatal(err)
	}
	file := w.file("x.dump")
	message := w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + nowhere}, "site", "backup", "src", "--out", file))
	if !strings.HasPrefix(message, "error: pg_dump not found; install the PostgreSQL client tools of the server's major version") ||
		!strings.Contains(message, "docs/guide/operations.md") {
		t.Errorf("%s", message)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a file is left behind")
	}

	// pg_restore: a real backup first, then the tool is hidden.
	w.ok("site", "backup", "src", "--out", file)
	w.ok("site", "create", "dst", "--empty")
	message = w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + nowhere}, "site", "restore", "dst", "--from", file))
	if !strings.HasPrefix(message, "error: pg_restore not found;") {
		t.Errorf("%s", message)
	}
	// Nothing was restored.
	if got := w.dst.scalar("SELECT to_regclass('fb_migration')::text"); got != "<NULL>" {
		t.Errorf("%s", got)
	}
}

// fakeTools is a directory with fake pg_dump and pg_restore scripts that print version for --version,
// restoreList for `pg_restore --list` (when not empty), and run body (a shell script) for any other
// arguments.
func fakeTools(t *testing.T, dir, version, restoreList, body string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-bin-"+uniqueName())
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		list := ""
		if tool == "pg_restore" && restoreList != "" {
			list = fmt.Sprintf("if [ \"$1\" = \"--list\" ]; then printf '%%s\\n' '%s'; exit 0; fi\n", restoreList)
		}
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '%s (PostgreSQL) %s'; exit 0; fi\n%s%s\n", tool, version, list, body)
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// serverMajor is the major version of the server the tests run against.
func serverMajor(w *backupWorld) int {
	n, err := strconv.Atoi(w.src.scalar("SELECT (current_setting('server_version_num')::int / 10000)::text"))
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// Rust: backup.rs::a_client_tool_of_another_major_version_is_refused_with_both_versions
func TestAClientToolOfAnotherMajorVersionIsRefusedWithBothVersions(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	server := serverMajor(w)
	// Older and newer tools are both refused: the tool must have the server's major version.
	for _, other := range []int{server - 1, server + 1} {
		bin := fakeTools(t, w.dir, fmt.Sprintf("%d.4", other), "", "exit 0")
		file := w.file("x.dump")
		message := w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + bin}, "site", "backup", "src", "--out", file))
		if !strings.Contains(message, fmt.Sprintf("pg_dump is version %d but the server is version %d;", other, server)) {
			t.Errorf("%s", message)
		}
		for _, name := range []string{"x.dump", "x.dump.partial"} {
			if _, err := os.Stat(w.file(name)); err == nil {
				t.Errorf("%s exists", name)
			}
		}
	}
	// The same for pg_restore.
	file := w.file("src.dump")
	w.ok("site", "backup", "src", "--out", file)
	w.ok("site", "create", "dst", "--empty")
	bin := fakeTools(t, w.dir, fmt.Sprintf("%d.4", server+1), "", "exit 0")
	message := w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + bin}, "site", "restore", "dst", "--from", file))
	if !strings.Contains(message, fmt.Sprintf("pg_restore is version %d but the server is version %d;", server+1, server)) {
		t.Errorf("%s", message)
	}
}

// Rust: backup.rs::the_tool_gets_the_connection_in_its_environment_only_and_a_failed_dump_leaves_no_file
func TestTheToolGetsTheConnectionInItsEnvironmentOnlyAndAFailedDumpLeavesNoFile(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	server := serverMajor(w)
	log := w.file("tool.log")
	// Records its arguments and its PG* environment, writes some of a dump to its standard output and
	// fails.
	body := fmt.Sprintf("echo \"ARGS: $*\" > '%s'\nenv | grep '^PG' | sort >> '%s'\n"+
		"echo partial\necho 'something went wrong' >&2\n"+
		"echo 'pg_dump: error: already named' >&2\nexit 3", log, log)
	bin := fakeTools(t, w.dir, fmt.Sprintf("%d.0", server), "", body)
	file := w.file("x.dump")
	// Deviation from Rust: a PGSERVICE in the environment is refused by the connection of the binary itself
	// (pgx cannot be told to ignore it, internal/db/url.go), so it is not among the inherited variables here;
	// the unit test of the child's environment (internal/app) covers it.
	r := w.runWith([]string{"FORGEBASE_PG_BIN=" + bin, "PGPASSFILE=/inherited/passfile", "PGOPTIONS=-c inherited=1"},
		"site", "backup", "src", "--out", file)
	message := w.fails(r)
	// The tool's standard error is passed on with its name, once; the failure is reported.
	if !strings.Contains(message, "pg_dump: something went wrong\n") {
		t.Errorf("%s", message)
	}
	if !strings.Contains(message, "pg_dump: error: already named\n") || strings.Contains(message, "pg_dump: pg_dump:") {
		t.Errorf("%s", message)
	}
	if !strings.Contains(message, "pg_dump failed; no backup file was written") {
		t.Errorf("%s", message)
	}
	for _, name := range []string{"x.dump", "x.dump.partial"} {
		if _, err := os.Stat(w.file(name)); err == nil {
			t.Errorf("%s exists: the partial file is removed", name)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args, env, _ := strings.Cut(string(data), "\n")
	// Not on the command line: no URL, user, password, host, port or file name.
	for _, secret := range []string{"forgebase", "postgres://", "localhost", "5432", "x.dump"} {
		if strings.Contains(args, secret) {
			t.Errorf("%s contains %s", args, secret)
		}
	}
	if !strings.Contains(args, "--format=custom") || !strings.Contains(args, "--no-owner") || strings.Contains(args, "--file") {
		t.Errorf("%s", args)
	}
	// In the environment: the connection, and none of the inherited PG* variables.
	for _, want := range []string{"PGPASSWORD=forgebase\n", "PGUSER=forgebase\n", "PGDATABASE=" + w.src.name + "\n", "PGHOST=localhost\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("%q is not in %s", want, env)
		}
	}
	for _, inherited := range []string{"PGPASSFILE", "PGOPTIONS"} {
		if strings.Contains(env, inherited) {
			t.Errorf("%s is inherited: %s", inherited, env)
		}
	}
	// A PGSERVICE of the operator is refused before anything runs (the deviation above).
	refused := w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + bin, "PGSERVICE=inherited-service"}, "site", "backup", "src", "--out", w.file("y.dump")))
	if !strings.Contains(refused, "unset PGSERVICE") {
		t.Errorf("%s", refused)
	}
	// Neither our output nor our error has the password or the URL.
	if strings.Contains(r.stdout, "forgebase:forgebase") || strings.Contains(message, "forgebase:forgebase") {
		t.Error("the URL is in the output")
	}
}

// Rust: backup.rs::a_killed_backup_leaves_at_most_the_partial_file_and_it_is_not_overwritten
func TestAKilledBackupLeavesAtMostThePartialFileAndItIsNotOverwritten(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	server := serverMajor(w)
	// A slow dump: some output, then it waits (exec: no shell is left behind).
	bin := fakeTools(t, w.dir, fmt.Sprintf("%d.0", server), "", "echo partial\nexec sleep 5")
	file, partial := w.file("x.dump"), w.file("x.dump.partial")
	cmd := exec.Command(binary, w.args([]string{"site", "backup", "src", "--out", file})...)
	cmd.Env = w.env("FORGEBASE_PG_BIN=" + bin)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for waited := 0; ; waited++ {
		if info, err := os.Stat(partial); err == nil && info.Size() > 0 {
			break
		}
		if waited >= 200 {
			_ = cmd.Process.Kill()
			t.Fatal("the backup did not start writing")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// While it runs, the final name does not exist.
	if _, err := os.Stat(file); err == nil {
		t.Error("the final name exists while the dump runs")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := os.Stat(file); err == nil {
		t.Error("a killed backup never leaves FILE")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Error("the partial file may be left")
	}
	// The next backup refuses to go on top of it, and says why.
	message := w.fails(w.run("site", "backup", "src", "--out", file))
	if !strings.Contains(message, "an earlier backup was interrupted") || !strings.Contains(message, "x.dump.partial") {
		t.Errorf("%s", message)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("FILE exists")
	}
	if err := os.Remove(partial); err != nil {
		t.Fatal(err)
	}
	// With the partial file gone, a real backup works.
	w.ok("site", "backup", "src", "--out", file)
	if _, err := os.Stat(file); err != nil {
		t.Error("no backup")
	}
	if _, err := os.Stat(partial); err == nil {
		t.Error("the partial file is left after a finished backup")
	}
}

// Rust: backup.rs::a_backup_made_by_a_newer_pg_dump_is_refused_by_pg_restore
func TestABackupMadeByANewerPgDumpIsRefusedByPgRestore(t *testing.T) {
	w := newBackupWorld(t)
	w.fillSrc()
	server := serverMajor(w)
	// A fake pg_restore of the server's own major version, whose listing says that the backup was made by
	// pg_dump 99: only the dump-version rule can refuse it.
	file := w.file("src.dump")
	w.ok("site", "backup", "src", "--out", file)
	w.ok("site", "create", "dst", "--empty")
	bin := fakeTools(t, w.dir, fmt.Sprintf("%d.2", server), ";     Dumped by pg_dump version: 99.0", "echo 'the restore must not run' >&2\nexit 9")
	message := w.fails(w.runWith([]string{"FORGEBASE_PG_BIN=" + bin}, "site", "restore", "dst", "--from", file))
	want := fmt.Sprintf("the backup was made by pg_dump version 99, which is newer than the server (version %d; pg_restore is version %d)", server, server)
	if !strings.Contains(message, want) || strings.Contains(message, "the restore must not run") {
		t.Errorf("%s", message)
	}
	if got := w.dst.scalar("SELECT to_regclass('fb_migration')::text"); got != "<NULL>" {
		t.Errorf("%s", got)
	}
}
