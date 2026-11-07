package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The unit tests of crates/forgebase/src/backup.rs. The tests that run the real tools are in
// cmd/forgebase/backup_test.go.

// Rust: backup.rs::versions_are_read_from_the_tool_output
func TestVersionsAreReadFromTheToolOutput(t *testing.T) {
	for _, c := range []struct {
		text string
		want uint32
		ok   bool
	}{
		{"pg_dump (PostgreSQL) 16.4 (Ubuntu 16.4-1.pgdg24.04+1)\n", 16, true},
		{"pg_restore (PostgreSQL) 17devel", 17, true},
		{"pg_dump (PostgreSQL) 9.6.24", 9, true},
		{"pg_dump", 0, false},
		{"", 0, false},
	} {
		if got, ok := parseMajor(c.text); got != c.want || ok != c.ok {
			t.Errorf("%q: %d %v", c.text, got, ok)
		}
	}
	listing := ";\n; Archive created at 2026-10-30\n; Dumped from database version: 16.4\n;     Dumped by pg_dump version: 17.0 (x)\n;\n"
	if got, ok := dumpMajor(listing); !ok || got != 17 {
		t.Errorf("%d %v", got, ok)
	}
	if _, ok := dumpMajor("; nothing here\n"); ok {
		t.Error("a listing without a version")
	}
}

// Rust: backup.rs::the_tool_must_have_the_servers_major_version
func TestTheToolMustHaveTheServersMajorVersion(t *testing.T) {
	if versionProblem(pgDump, 16, 16) != "" {
		t.Error("equal versions")
	}
	for _, other := range []uint32{15, 17} {
		problem := versionProblem(pgDump, other, 16)
		if !strings.HasPrefix(problem, "pg_dump is version "+itoa32(other)+" but the server is version 16;") ||
			!strings.Contains(problem, "install the PostgreSQL client tools of version 16") {
			t.Errorf("%s", problem)
		}
	}
	if problem := versionProblem(pgRestore, 17, 16); !strings.HasPrefix(problem, "pg_restore is version 17 but the server is version 16") {
		t.Errorf("%s", problem)
	}
}

func itoa32(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

// Rust: backup.rs::a_dump_must_not_be_newer_than_the_server
func TestADumpMustNotBeNewerThanTheServer(t *testing.T) {
	if dumpProblem(16, 16, 16) != "" || dumpProblem(17, 16, 17) != "" {
		t.Error("an equal dump, or an upgrade, was refused")
	}
	problem := dumpProblem(16, 17, 16)
	if !strings.Contains(problem, "pg_dump version 17") || !strings.Contains(problem, "server (version 16; pg_restore is version 16)") {
		t.Errorf("%s", problem)
	}
}

// Rust: backup.rs::no_argument_carries_the_url_or_the_password
func TestNoArgumentCarriesTheURLOrThePassword(t *testing.T) {
	env, err := db.PgEnvironment("postgres://ann:s3cretpw@db.example.com:6543/shop")
	if err != nil {
		t.Fatal(err)
	}
	// The password is in the environment of the child, and only there.
	var database string
	found := false
	for _, v := range env {
		found = found || v == (db.EnvVar{Name: "PGPASSWORD", Value: "s3cretpw"})
		if v.Name == "PGDATABASE" {
			database = v.Value
		}
	}
	if !found || database != "shop" {
		t.Fatalf("%v", env)
	}
	for _, args := range [][]string{dumpArgs(), restoreArgs(database, "/backups/shop.dump")} {
		for _, arg := range args {
			for _, secret := range []string{"s3cretpw", "ann", "db.example.com", "6543", "postgres://"} {
				if strings.Contains(arg, secret) {
					t.Errorf("%s contains %s", arg, secret)
				}
			}
		}
	}
}

// Rust: backup.rs::restore_arguments_are_fixed_and_a_dash_file_is_not_an_option
func TestRestoreArgumentsAreFixedAndADashFileIsNotAnOption(t *testing.T) {
	got := strings.Join(restoreArgs("shop", "-odd.dump"), " ")
	want := "--no-owner --no-privileges --exit-on-error --single-transaction --dbname=shop ./-odd.dump"
	if got != want {
		t.Errorf("%s", got)
	}
	if got := strings.Join(dumpArgs(), " "); got != "--format=custom --no-owner --no-privileges" {
		t.Errorf("%s", got)
	}
}

// Rust: backup.rs::an_existing_file_is_never_overwritten
func TestAnExistingFileIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.dump")
	partial := partialPath(file)
	if partial != filepath.Join(dir, "x.dump.partial") {
		t.Fatal(partial)
	}
	if err := os.WriteFile(file, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func() error { return checkNewFile(file) }
	res := func() error { _, err := reserve(file, partial); return err }
	pub := func() error { _, err := publish(partial, file); return err }
	for i, f := range []func() error{check, res, pub} {
		err := f()
		if err == nil || !(err.Error() == "refusing to overwrite "+file || strings.HasPrefix(err.Error(), "cannot create")) {
			t.Errorf("step %d: %v", i, err)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != "precious" {
		t.Errorf("%q", data)
	}
	if _, err := os.Lstat(partial); err == nil {
		t.Error("a refused backup creates nothing")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := checkNewFile(file); err != nil {
		t.Fatal(err)
	}
	handle, err := reserve(file, partial)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(partial); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%v %v", info, err)
	}
	// An earlier interrupted backup is reported as such, and is not overwritten.
	for i, f := range []func() error{check, res} {
		err := f()
		if err == nil || !strings.Contains(err.Error(), "an earlier backup was interrupted") || !strings.Contains(err.Error(), "x.dump.partial") {
			t.Errorf("step %d: %v", i, err)
		}
	}
	// Publishing gives the final name and never overwrites.
	if _, err := handle.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	size, err := publish(partial, file)
	if err != nil || size != 4 {
		t.Fatalf("%d %v", size, err)
	}
	if _, err := publish(partial, file); err == nil {
		t.Error("published twice")
	}
	_ = handle.Close()
}

// Rust: backup.rs::a_missing_tool_is_a_clear_error
func TestAMissingToolIsAClearError(t *testing.T) {
	r := &toolRunner{binDir: t.TempDir(), stderr: &strings.Builder{}}
	for _, tool := range []pgTool{pgDump, pgRestore} {
		_, err := r.toolMajor(tool)
		if err == nil || !strings.HasPrefix(err.Error(), string(tool)+" not found; install the PostgreSQL client tools of the server's major version") ||
			!strings.Contains(err.Error(), "docs/guide/operations.md") {
			t.Errorf("%v", err)
		}
	}
	// With no directory the tool is looked up on PATH: an empty PATH finds nothing.
	t.Setenv("PATH", t.TempDir())
	_, err := (&toolRunner{stderr: &strings.Builder{}}).toolMajor(pgDump)
	if err == nil || !strings.HasPrefix(err.Error(), "pg_dump not found;") {
		t.Errorf("%v", err)
	}
}

// Go addition: the child never inherits a PG* variable of this process; the connection is exactly what it
// was given.
func TestTheChildGetsNoInheritedPGVariables(t *testing.T) {
	t.Setenv("PGSERVICE", "inherited-service")
	t.Setenv("PGPASSFILE", "/inherited/passfile")
	t.Setenv("KEPT_VARIABLE", "kept")
	r := &toolRunner{stderr: &strings.Builder{}}
	cmd := r.command(pgDump, []db.EnvVar{{Name: "PGUSER", Value: "ann"}})
	have := map[string]string{}
	for _, entry := range cmd.Env {
		name, value, _ := strings.Cut(entry, "=")
		have[name] = value
	}
	if _, ok := have["PGSERVICE"]; ok || have["PGUSER"] != "ann" || have["KEPT_VARIABLE"] != "kept" {
		t.Errorf("%v", have)
	}
	if _, ok := have["PGPASSFILE"]; ok {
		t.Error("PGPASSFILE is inherited")
	}
}
