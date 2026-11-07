package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// `site create`, `site backup` and `site restore` over TLS (M9.0, ADR 0018): the built binary and the real
// pg_dump and pg_restore against the PostgreSQL with TLS of .github/ci/pg-tls.sh. The site URLs say
// sslmode=require and sslrootcert=<CA file>, so the tools get PGSSLMODE=verify-full and PGSSLROOTCERT.
// Ported from crates/forgebase/tests/backup_tls.rs. The variables are FORGEBASE_TEST_TLS_URL, _CA and
// _WRONG_CA (see CONTRIBUTING.md); the test is skipped with a notice when they are not set, and fails when
// CI=true (dbtest.TLSEnv). The PostgreSQL 16 client tools are needed like in backup_test.go.

// Rust: backup_tls.rs::site_create_backup_and_restore_work_over_tls
func TestSiteCreateBackupAndRestoreWorkOverTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	// The role tls_only is accepted over TLS only (see .github/ci/pg-tls.sh), so the dump and the restore
	// below prove that pg_dump and pg_restore used TLS.
	only := strings.Replace(tls.URL, "forgebase:forgebase", "tls_only:tls-only", 1)
	id := os.Getpid()
	names := [2]string{fmt.Sprintf("fb_test_tls_src_%d", id), fmt.Sprintf("fb_test_tls_dst_%d", id)}
	query := "sslmode=require&sslrootcert=" + dbtest.Encode(tls.CA)
	siteURL := func(name string) string {
		return strings.Replace(only, "/postgres", "/"+name, 1) + "?" + query
	}
	// The databases are dropped over TLS at the end, also when the test fails.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool, err := db.Connect(ctx, tls.With(query))
		if err != nil {
			return
		}
		defer pool.Close()
		for _, name := range names {
			_, _ = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+db.QuoteIdent(name)+" WITH (FORCE)")
		}
	})
	dir := t.TempDir()
	config := "[[sites]]\nname = \"src\"\nhosts = [\"src.localhost\"]\ndatabase_url_env = \"FB_TEST_TLS_SRC_URL\"\n\n" +
		"[[sites]]\nname = \"dst\"\nhosts = [\"dst.localhost\"]\ndatabase_url_env = \"FB_TEST_TLS_DST_URL\"\n"
	if err := os.WriteFile(filepath.Join(dir, "forgebase.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "FB_TEST_TLS_SRC_URL=" + siteURL(names[0]), "FB_TEST_TLS_DST_URL=" + siteURL(names[1])}
	if bin := os.Getenv("FORGEBASE_PG_BIN"); bin != "" {
		env = append(env, "FORGEBASE_PG_BIN="+bin)
	}
	ok := func(args ...string) string {
		t.Helper()
		r := runBinary(t, env, "", append([]string{"--config", filepath.Join(dir, "forgebase.toml")}, args...)...)
		if r.code != 0 {
			t.Fatalf("%v: %s", args, r.stderr)
		}
		return r.stdout
	}

	// `site create` connects to the maintenance database over TLS, then migrates over TLS.
	ok("site", "create", "src", "--doctypes", doctypes)
	ok("doc", "insert", "customer", "--site", "src", "--doctypes", doctypes, "--data", `{"email":"a@example.com","full_name":"Ann"}`)
	before := ok("doc", "list", "customer", "--site", "src", "--doctypes", doctypes)
	if !strings.Contains(before, "a@example.com") {
		t.Fatalf("%s", before)
	}

	// pg_dump and pg_restore verify the server with PGSSLMODE=verify-full and PGSSLROOTCERT. pg_dump with
	// the environment of a wrong-CA URL fails on the certificate, not on the login.
	pgDump := "pg_dump"
	if bin := os.Getenv("FORGEBASE_PG_BIN"); bin != "" {
		pgDump = filepath.Join(bin, "pg_dump")
	}
	wrongURL := strings.Replace(only, "/postgres", "/"+names[0], 1) + "?sslmode=require&sslrootcert=" + dbtest.Encode(tls.WrongCA)
	vars, err := db.PgEnvironment(wrongURL)
	if err != nil {
		t.Fatal(err)
	}
	dump := exec.Command(pgDump, "--format=custom", "--file=/dev/null")
	dump.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, v := range vars {
		dump.Env = append(dump.Env, v.Name+"="+v.Value)
	}
	out, err := dump.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "certificate") {
		t.Fatalf("pg_dump with the wrong CA: %v: %s", err, out)
	}

	file := filepath.Join(dir, "src.dump")
	if out := ok("site", "backup", "src", "--out", file); !strings.HasPrefix(out, "backed up site `src`") || strings.Contains(out, "forgebase:forgebase") {
		t.Errorf("%s", out)
	}
	ok("site", "create", "dst", "--empty")
	ok("site", "restore", "dst", "--from", file)
	if after := ok("doc", "list", "customer", "--site", "dst", "--doctypes", doctypes); after != before {
		t.Errorf("the documents differ:\n%s\n---\n%s", before, after)
	}
}
