package db_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// TLS to PostgreSQL (ADR 0018) against a real PostgreSQL 16 with TLS and a throwaway CA. Ported from
// crates/forgebase-db/tests/tls.rs. Start the server with `.github/ci/pg-tls.sh up DIR` (see
// CONTRIBUTING.md); a test is skipped with a notice when the variables are not set, and fails when
// CI=true (dbtest.TLSEnv). The host name test needs no server (tls_fake_test.go).

func usesSSL(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ssl bool
	if err := pool.QueryRow(bg, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl); err != nil {
		t.Fatal(err)
	}
	return ssl
}

// tlsFailure is the error of a connect that must fail: its text, after a check that it is about TLS.
func tlsFailure(t *testing.T, url string) string {
	t.Helper()
	pool, err := db.Connect(bg, url)
	if err == nil {
		pool.Close()
		t.Fatal("it must not connect")
	}
	msg := err.Error()
	if !strings.Contains(msg, "TLS") || strings.Contains(msg, "password") || strings.Contains(msg, "28P01") {
		t.Fatalf("not a TLS error: %s", msg)
	}
	return msg
}

// Rust: tls.rs::require_and_the_verify_modes_connect_over_tls_with_the_right_ca
func TestRequireAndTheVerifyModesConnectOverTLSWithTheRightCA(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		pool, err := db.Connect(bg, tls.Verified(mode))
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if !usesSSL(t, pool) {
			t.Errorf("%s: no TLS", mode)
		}
		pool.Close()
	}
}

// Rust: tls.rs::the_pool_works_over_tls_with_several_connections
func TestThePoolWorksOverTLSWithSeveralConnections(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	pool, err := db.Connect(bg, tls.Verified("require"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var held []*pgxpool.Conn
	for range 4 {
		conn, err := pool.Acquire(bg)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	for _, conn := range held {
		var ssl bool
		if err := conn.QueryRow(bg, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl); err != nil || !ssl {
			t.Errorf("a pooled connection without TLS: %v", err)
		}
	}
	for _, conn := range held {
		conn.Release()
	}
	if !usesSSL(t, pool) {
		t.Error("no TLS after release")
	}
}

// Rust: tls.rs::a_wrong_ca_fails_with_a_verification_error
func TestAWrongCAFailsWithAVerificationError(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	// Go says "unknown authority" where rustls says UnknownIssuer.
	if msg := tlsFailure(t, tls.WithWrongCA()); !strings.Contains(msg, "unknown authority") {
		t.Errorf("%s", msg)
	}
}

// Rust: tls.rs::an_ip_host_is_verified_against_the_ip_names_of_the_certificate
func TestAnIPHostIsVerifiedAgainstTheIPNamesOfTheCertificate(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	pool, err := db.Connect(bg, strings.Replace(tls.Verified("require"), "localhost", "127.0.0.1", 1))
	if err != nil {
		t.Fatalf("127.0.0.1 is in the certificate: %v", err)
	}
	defer pool.Close()
	if !usesSSL(t, pool) {
		t.Error("no TLS")
	}
}

// Rust: tls.rs::disable_and_no_sslmode_connect_without_tls
func TestDisableAndNoSslmodeConnectWithoutTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	for _, url := range []string{tls.With("sslmode=disable"), tls.URL} {
		pool, err := db.Connect(bg, url)
		if err != nil {
			t.Fatal(err)
		}
		if usesSSL(t, pool) {
			t.Errorf("%s uses TLS", url)
		}
		pool.Close()
	}
}

// Rust: tls.rs::prefer_unknown_modes_and_client_certificates_are_refused
func TestPreferUnknownModesAndClientCertificatesAreRefused(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	for _, c := range []struct{ query, expected string }{
		{"sslmode=prefer", "`sslmode=prefer` is not supported"},
		{"sslmode=allow", "`sslmode=prefer` is not supported"},
		{"sslmode=bogus", "not supported"},
		{"sslmode=require&sslcert=/x.pem", "`sslcert`"},
		{"sslmode=require&sslkey=/x.pem", "`sslkey`"},
	} {
		_, err := db.Connect(bg, tls.With(c.query))
		if err == nil || !strings.Contains(err.Error(), c.expected) || strings.Contains(err.Error(), "forgebase:forgebase") {
			t.Errorf("%s: %v", c.query, err)
		}
	}
	if _, err := db.CreateDatabase(bg, tls.With("sslmode=prefer"), ""); err == nil {
		t.Error("CreateDatabase accepted sslmode=prefer")
	}
	if _, err := db.InspectDatabase(bg, tls.With("sslmode=prefer")); err == nil {
		t.Error("InspectDatabase accepted sslmode=prefer")
	}
}

// Rust: tls.rs::a_missing_root_file_is_a_clear_error_without_the_url
func TestAMissingRootFileIsAClearErrorWithoutTheURL(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	_, err := db.Connect(bg, tls.With("sslmode=require&sslrootcert=/nonexistent/ca.pem"))
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/ca.pem") || strings.Contains(err.Error(), "forgebase:forgebase") {
		t.Errorf("%v", err)
	}
}

// Rust: tls.rs::the_system_roots_do_not_trust_the_throwaway_ca. The system roots do not contain the
// throwaway CA, so sslrootcert=system (and the default) must fail with a verification error.
func TestTheSystemRootsDoNotTrustTheThrowawayCA(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	for _, query := range []string{"sslmode=require", "sslmode=require&sslrootcert=system"} {
		_, err := db.Connect(bg, tls.With(query))
		// An image without CA certificates fails earlier, with its own clear message.
		if err == nil || !(strings.Contains(err.Error(), "unknown authority") || strings.Contains(err.Error(), "trust store")) {
			t.Errorf("%s: %v", query, err)
		}
	}
}

var scratchCounter atomic.Uint32

// scratchName is a database name no test run uses twice.
func scratchName() string {
	return fmt.Sprintf("fb_test_tls_%x_%d", os.Getpid(), scratchCounter.Add(1))
}

// dropOverTLS drops a database (with FORCE) over TLS when the test ends, also when it fails.
func dropOverTLS(t *testing.T, maintenanceURL, name string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool, err := db.Connect(ctx, maintenanceURL)
		if err != nil {
			return
		}
		defer pool.Close()
		_, _ = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+db.QuoteIdent(name)+" WITH (FORCE)")
	})
}

// Rust: tls.rs::create_database_inspect_and_migrate_work_over_tls
func TestCreateDatabaseInspectAndMigrateWorkOverTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	name := scratchName()
	query := "sslmode=require&sslrootcert=" + dbtest.Encode(tls.CA)
	dropOverTLS(t, tls.With(query), name)
	url := tls.ForDatabase(name) + "?" + query

	// The maintenance connection of CreateDatabase uses TLS: with the wrong CA it fails.
	wrong := tls.ForDatabase(name) + "?sslmode=require&sslrootcert=" + dbtest.Encode(tls.WrongCA)
	_, err := db.CreateDatabase(bg, wrong, "")
	if err == nil || !strings.Contains(err.Error(), "TLS") || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("wrong CA: %v", err)
	}

	if created, err := db.CreateDatabase(bg, url, ""); err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if created, err := db.CreateDatabase(bg, url, ""); err != nil || created {
		t.Fatalf("exists: %v %v", created, err)
	}
	info, err := db.InspectDatabase(bg, url)
	if err != nil || info.ServerMajor() != 16 || !info.IsEmpty {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	pool, err := db.Connect(bg, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if !usesSSL(t, pool) {
		t.Error("the pool uses no TLS")
	}
	applied, err := db.MigrateFramework(bg, pool, false)
	if err != nil || len(applied) == 0 {
		t.Fatalf("migrate over TLS: %v %v", applied, err)
	}
	var count int
	if err := pool.QueryRow(bg, "SELECT count(*) FROM fb_migration").Scan(&count); err != nil || count == 0 {
		t.Errorf("fb_migration: %d %v", count, err)
	}
}

// Rust: tls.rs::scram_channel_binding_works_over_tls. Neon's connection strings carry
// channel_binding=require; SCRAM channel binding needs the certificate hash of the TLS connection.
func TestSCRAMChannelBindingWorksOverTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	pool, err := db.Connect(bg, tls.Verified("require")+"&channel_binding=require")
	if err != nil {
		t.Fatalf("channel_binding=require: %v", err)
	}
	defer pool.Close()
	if !usesSSL(t, pool) {
		t.Error("no TLS")
	}
}

// Rust: tls.rs::a_question_mark_in_the_password_must_be_encoded_and_then_keeps_tls. The role qmark
// (password p?w) is accepted over TLS only (pg-tls.sh). A raw ? in the password hid sslmode from the
// parser once, and connected without TLS: now the raw form is refused before any connection, and the
// encoded one (p%3Fw) connects over TLS.
func TestAQuestionMarkInThePasswordMustBeEncodedAndThenKeepsTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	user := func(url, password string) string {
		return strings.Replace(url, "forgebase:forgebase", "qmark:"+password, 1)
	}
	for _, url := range []string{
		user(tls.Verified("require"), "p%3Fw"),
		// The key written with a percent-encoded letter.
		user(tls.With("%73slmode=require&sslrootcert="+dbtest.Encode(tls.CA)), "p%3Fw"),
	} {
		pool, err := db.Connect(bg, url)
		if err != nil {
			t.Fatalf("a TLS connection: %v", err)
		}
		if !usesSSL(t, pool) {
			t.Error("no TLS")
		}
		pool.Close()
	}
	// The raw ? is refused with a clear message, not connected without TLS.
	for _, query := range []string{"sslmode=require", "sslmode=disable"} {
		_, err := db.Connect(bg, user(tls.With(query), "p?w"))
		if err == nil || !strings.Contains(err.Error(), "before its first `@`") {
			t.Errorf("%s: %v", query, err)
		}
	}
	// Without TLS the server rejects the role: the role really is TLS only.
	if pool, err := db.Connect(bg, user(tls.With("sslmode=disable"), "p%3Fw")); err == nil {
		pool.Close()
		t.Error("the TLS-only role connected without TLS")
	}
	// An encoded TLS option is refused by name, not ignored.
	_, err := db.Connect(bg, user(tls.With("sslmode=require&%73slcert=/x.pem"), "p%3Fw"))
	if err == nil || !strings.Contains(err.Error(), "`sslcert`") {
		t.Errorf("%v", err)
	}
}

// Rust: tls.rs (the cancel request goes over TLS too: tokio-postgres cancel_query with the same connector).
// pgx 5.12 sends a cancel request over TLS when its connection uses TLS, with the TLS configuration of the
// connection (pgconn.CancelRequest, "tls error on cancel connection"), so the cancel needs the verifying roots
// that the pool has. A long query is cancelled at once and the connection stays usable.
func TestACancelRequestWorksOverTLS(t *testing.T) {
	tls := dbtest.TLSEnv(t)
	pool, err := db.Connect(bg, tls.Verified("require"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	done := make(chan error, 1)
	go func() {
		_, err := conn.Exec(bg, "SELECT pg_sleep(30)")
		done <- err
	}()
	// The cancel request is sent until the query has started and was cancelled.
	deadline := time.After(20 * time.Second)
	for {
		time.Sleep(100 * time.Millisecond)
		if err := conn.Conn().PgConn().CancelRequest(bg); err != nil {
			t.Fatalf("the cancel request: %v", err)
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "canceling statement due to user request") {
				t.Fatalf("the query: %v", err)
			}
			var one int
			if err := conn.QueryRow(bg, "SELECT 1").Scan(&one); err != nil || one != 1 {
				t.Errorf("the connection is not usable after the cancel: %v", err)
			}
			return
		case <-deadline:
			t.Fatal("the query was not cancelled")
		default:
		}
	}
}
