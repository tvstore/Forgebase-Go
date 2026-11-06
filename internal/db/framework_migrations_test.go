package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Framework migrations against a real PostgreSQL (see dbtest). Ported from
// crates/forgebase-db/tests/framework_migrations.rs.

func want(t *testing.T, got, expected any, label string) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", expected) {
		t.Errorf("%s:\n got %#v\nwant %#v", label, got, expected)
	}
}

func asDbError(t *testing.T, err error) *db.Error {
	t.Helper()
	var e *db.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected a *db.Error, got %T: %v", err, err)
	}
	return e
}

var bg = context.Background()

// Rust: framework_migrations.rs::migrates_empty_database_and_is_idempotent
func TestMigratesEmptyDatabaseAndIsIdempotent(t *testing.T) {
	d := dbtest.New(t)
	applied, err := db.MigrateFramework(bg, d.Pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(db.Migrations) {
		t.Errorf("applied %d of %d", len(applied), len(db.Migrations))
	}
	want(t, d.Rows("SELECT version::text, name, length(checksum)::text FROM fb_migration ORDER BY fb_migration.version"), [][]string{
		{"1", "init", "64"},
		{"2", "series", "64"},
		{"3", "auth", "64"},
		{"4", "apps", "64"},
		{"5", "jobs", "64"},
		{"6", "schedules", "64"},
		{"7", "finished_jobs_index", "64"},
		{"8", "workflow_log", "64"},
		{"9", "saved_views", "64"},
		{"10", "webhooks", "64"},
		{"11", "fixtures", "64"},
		{"12", "custom_fields", "64"},
		{"13", "user_permissions", "64"},
	}, "fb_migration")
	want(t, d.Rows("SELECT count(*)::text FROM fb_doctype"), [][]string{{"0"}}, "fb_doctype")

	again, err := db.MigrateFramework(bg, d.Pool, false)
	if err != nil || len(again) != 0 {
		t.Errorf("second run: %v, %v", again, err)
	}
	want(t, d.Rows("SELECT count(*)::text FROM fb_migration"), [][]string{{"13"}}, "count")
}

// Not in Rust: PostgreSQL computes the checksum of each migration, and it must equal the SHA-256
// that the Rust implementation recorded (expectedChecksums in migrate_test.go): a database migrated
// by Rust verifies cleanly in Go, and the other way round.
func TestRecordedChecksumsAreTheRustChecksums(t *testing.T) {
	d := dbtest.Migrated(t)
	rows := d.Rows("SELECT version::text, checksum FROM fb_migration ORDER BY fb_migration.version")
	if len(rows) != 13 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		var version int
		fmt.Sscan(r[0], &version)
		if r[1] != db.ExpectedChecksumForTest(int32(version)) {
			t.Errorf("migration %s: recorded %s, Rust has %s", r[0], r[1], db.ExpectedChecksumForTest(int32(version)))
		}
	}
}

// Rust: framework_migrations.rs::the_series_counter_table_is_created_by_migration_2
func TestTheSeriesCounterTableIsCreatedByMigration2(t *testing.T) {
	d := dbtest.Migrated(t)
	// prefix is the primary key, current is not null.
	want(t, d.Columns("fb_series"), []dbtest.Column{{"prefix", "text/NO"}, {"current", "bigint/NO"}}, "columns")
	want(t, d.Rows("SELECT a.attname::text FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid "+
		"AND a.attnum = i.indkey[0] WHERE i.indrelid = 'fb_series'::regclass AND i.indisprimary"),
		[][]string{{"prefix"}}, "primary key")
}

// applyFirst applies the first count migrations by hand, with the checksums the runner computes.
func applyFirst(d *dbtest.DB, count int) {
	d.Exec("CREATE TABLE fb_migration (version integer PRIMARY KEY, name text NOT NULL, " +
		"checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())")
	for _, m := range db.Migrations[:count] {
		d.Exec(m.SQL)
		d.Exec(fmt.Sprintf("INSERT INTO fb_migration (version, name, checksum) VALUES (%d, '%s', "+
			"encode(sha256(convert_to($sql$%s$sql$, 'UTF8')), 'hex'))", m.Version, m.Name, m.SQL))
	}
}

func versions(ms []db.Migration) []int32 {
	out := []int32{}
	for _, m := range ms {
		out = append(out, m.Version)
	}
	return out
}

// Rust: framework_migrations.rs::a_database_at_migration_1_is_upgraded_to_the_latest
func TestADatabaseAtMigration1IsUpgradedToTheLatest(t *testing.T) {
	d := dbtest.New(t)
	applyFirst(d, 1)
	applied, err := db.MigrateFramework(bg, d.Pool, false)
	if err != nil {
		t.Fatal(err)
	}
	want(t, versions(applied), []int32{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, "applied")
	want(t, d.Rows("SELECT version::text FROM fb_migration ORDER BY fb_migration.version"),
		[][]string{{"1"}, {"2"}, {"3"}, {"4"}, {"5"}, {"6"}, {"7"}, {"8"}, {"9"}, {"10"}, {"11"}, {"12"}, {"13"}}, "versions")
	want(t, d.Rows("SELECT count(*)::text FROM fb_series"), [][]string{{"0"}}, "fb_series")
}

// Rust: framework_migrations.rs::a_database_at_migration_2_is_upgraded_to_the_latest
func TestADatabaseAtMigration2IsUpgradedToTheLatest(t *testing.T) {
	d := dbtest.New(t)
	applyFirst(d, 2)
	applied, err := db.MigrateFramework(bg, d.Pool, false)
	if err != nil {
		t.Fatal(err)
	}
	want(t, versions(applied), []int32{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, "applied")
	if applied[0].Name != "auth" {
		t.Errorf("first applied is %s", applied[0].Name)
	}
	for _, table := range []string{"fb_user", "fb_user_role", "fb_session", "fb_api_token", "fb_login_failure",
		"fb_app", "fb_app_migration", "fb_schedule"} {
		want(t, d.Rows("SELECT count(*)::text FROM "+table), [][]string{{"0"}}, table)
	}
}

// Rust: framework_migrations.rs::dry_run_applies_nothing
func TestDryRunAppliesNothing(t *testing.T) {
	d := dbtest.New(t)
	pending, err := db.MigrateFramework(bg, d.Pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(db.Migrations) {
		t.Errorf("%d pending", len(pending))
	}
	tables := d.Rows("SELECT table_name::text FROM information_schema.tables WHERE table_name LIKE 'fb_%'")
	if len(tables) != 0 {
		t.Errorf("dry run left tables behind: %v", tables)
	}
}

// Rust: framework_migrations.rs::tampered_checksum_is_rejected
func TestTamperedChecksumIsRejected(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("UPDATE fb_migration SET checksum = repeat('0', 64)")
	_, err := db.MigrateFramework(bg, d.Pool, false)
	e := asDbError(t, err)
	if e.Kind != db.ErrChecksumMismatch || e.Version != 1 {
		t.Errorf("%v", err)
	}
	if !strings.Contains(err.Error(), "framework migration 1 (init) was changed after it was applied: expected checksum ") {
		t.Errorf("%v", err)
	}
}

// Rust: framework_migrations.rs::unknown_applied_version_is_rejected
func TestUnknownAppliedVersionIsRejected(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("INSERT INTO fb_migration (version, name, checksum) VALUES (999, 'from_the_future', 'x')")
	_, err := db.MigrateFramework(bg, d.Pool, false)
	e := asDbError(t, err)
	if e.Kind != db.ErrUnknownMigration || e.Version != 999 {
		t.Errorf("%v", err)
	}
	if !strings.Contains(err.Error(), "from_the_future") {
		t.Errorf("%v", err)
	}
}

// Rust: framework_migrations.rs::invalid_url_and_unreachable_server_give_clear_errors
func TestInvalidURLAndUnreachableServerGiveClearErrors(t *testing.T) {
	_, err := db.Connect(bg, "not a url")
	if e := asDbError(t, err); e.Kind != db.ErrCreatePool {
		t.Errorf("%v", err)
	}
	_, err = db.Connect(bg, "postgres://nobody:nothing@127.0.0.1:1/none")
	// A failed connection attempt is a Postgres error carrying the cause, not a bare "error".
	if e := asDbError(t, err); e.Kind != db.ErrPostgres {
		t.Errorf("%v", err)
	}
	if !strings.Contains(err.Error(), "failed to connect") {
		t.Errorf("%v", err)
	}
	if strings.Contains(err.Error(), "nothing") && strings.Contains(err.Error(), "nobody:nothing") {
		t.Errorf("the password is in the error: %v", err)
	}
}

// Rust: framework_migrations.rs::postgres_errors_show_server_message_and_sqlstate
func TestPostgresErrorsShowServerMessageAndSqlstate(t *testing.T) {
	d := dbtest.Migrated(t)
	// Violates the primary key of fb_migration.
	err := db.WrapForTest(d.TryExec("INSERT INTO fb_migration VALUES (1, 'dup', 'x')"))
	text := err.Error()
	if !strings.Contains(text, "duplicate key") || !strings.Contains(text, "23505") {
		t.Errorf("%s", text)
	}
}

// Rust: framework_migrations.rs::connect_errors_show_server_message_and_sqlstate
func TestConnectErrorsShowServerMessageAndSqlstate(t *testing.T) {
	_, err := db.Connect(bg, dbtest.URLWithPassword(t, "definitely-wrong"))
	if err == nil {
		t.Fatal("connected with a wrong password")
	}
	text := err.Error()
	if !strings.Contains(text, "password authentication failed") || !strings.Contains(text, "28P01") {
		t.Errorf("%s", text)
	}
	if strings.Contains(text, "definitely-wrong") {
		t.Errorf("%s", text)
	}

	_, err = db.Connect(bg, dbtest.URLForDatabase(t, "fb_no_such_database"))
	if err == nil {
		t.Fatal("connected to a missing database")
	}
	text = err.Error()
	if !strings.Contains(text, "does not exist") || !strings.Contains(text, "3D000") {
		t.Errorf("%s", text)
	}
	if !db.IsMissingDatabase(err) {
		t.Errorf("IsMissingDatabase: %v", err)
	}
}

// Rust: framework_migrations.rs::unexpected_fb_migration_shape_is_an_error_not_a_panic
func TestUnexpectedFbMigrationShapeIsAnErrorNotAPanic(t *testing.T) {
	d := dbtest.New(t)
	d.Exec("CREATE TABLE fb_migration (version bigint PRIMARY KEY, name text NOT NULL, " +
		"checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now()); " +
		"INSERT INTO fb_migration (version, name, checksum) VALUES (1, 'init', 'x')")
	_, err := db.MigrateFramework(bg, d.Pool, false)
	// version bigint scans into an int32 as long as the value fits; the row is then an unknown
	// checksum. Either way it is an error, never a panic.
	if err == nil {
		t.Fatal("expected an error")
	}
	if e := asDbError(t, err); e.Kind != db.ErrPostgres && e.Kind != db.ErrChecksumMismatch {
		t.Errorf("%v", err)
	}
}

// Rust: framework_migrations.rs::migration_7_replaces_the_done_jobs_index_with_a_finished_jobs_index
func TestMigration7ReplacesTheDoneJobsIndexWithAFinishedJobsIndex(t *testing.T) {
	d := dbtest.Migrated(t)
	want(t, d.Rows("SELECT indexname::text FROM pg_indexes "+
		"WHERE tablename = 'fb_job' AND indexname LIKE 'fb_job_%_idx' "+
		"AND indexname IN ('fb_job_done_idx', 'fb_job_finished_idx')"),
		[][]string{{"fb_job_finished_idx"}}, "indexes")
}

// Rust: framework_migrations.rs::migration_8_creates_the_workflow_log
func TestMigration8CreatesTheWorkflowLog(t *testing.T) {
	d := dbtest.Migrated(t)
	want(t, d.Columns("fb_workflow_log"), []dbtest.Column{
		{"id", "bigint/NO"}, {"doctype", "text/NO"}, {"name", "text/NO"}, {"action", "text/NO"},
		{"from_state", "text/NO"}, {"to_state", "text/NO"}, {"actor", "text/NO"},
		{"at", "timestamp with time zone/NO"},
	}, "columns")
	want(t, d.Rows("SELECT indexdef::text FROM pg_indexes WHERE tablename = 'fb_workflow_log' "+
		"AND indexname = 'fb_workflow_log_document'"),
		[][]string{{"CREATE INDEX fb_workflow_log_document ON public.fb_workflow_log USING btree (doctype, name, id)"}}, "index")
}

// Rust: framework_migrations.rs::migration_10_creates_the_webhooks
func TestMigration10CreatesTheWebhooks(t *testing.T) {
	d := dbtest.Migrated(t)
	want(t, d.Columns("fb_webhook"), []dbtest.Column{
		{"name", "text/NO"}, {"doctype", "text/NO"}, {"events", "ARRAY/NO"}, {"url", "text/NO"},
		{"secret", "text/NO"}, {"enabled", "boolean/NO"}, {"allow_private", "boolean/NO"},
		{"insecure_http", "boolean/NO"}, {"created_at", "timestamp with time zone/NO"},
		{"modified_at", "timestamp with time zone/NO"},
	}, "columns")
	// The defaults: enabled, and both development flags off.
	d.Exec("INSERT INTO fb_webhook (name, doctype, events, url, secret) " +
		"VALUES ('w', 'note', '{insert}', 'https://example.com/x', 's')")
	want(t, d.Rows("SELECT enabled::text, allow_private::text, insecure_http::text FROM fb_webhook"),
		[][]string{{"true", "false", "false"}}, "defaults")
	// A partial index on the DocType of the enabled webhooks, for the lookup of every write.
	want(t, d.Rows("SELECT indexdef::text FROM pg_indexes WHERE tablename = 'fb_webhook' "+
		"AND indexname = 'fb_webhook_doctype'"),
		[][]string{{"CREATE INDEX fb_webhook_doctype ON public.fb_webhook USING btree (doctype) WHERE enabled"}}, "index")
}

// Rust: framework_migrations.rs::migration_9_creates_the_saved_views
func TestMigration9CreatesTheSavedViews(t *testing.T) {
	d := dbtest.Migrated(t)
	want(t, d.Columns("fb_saved_view"), []dbtest.Column{
		{"id", "uuid/NO"}, {"doctype", "text/NO"}, {"label", "text/NO"}, {"owner_id", "uuid/YES"},
		{"definition", "jsonb/NO"}, {"created_at", "timestamp with time zone/NO"},
		{"modified_at", "timestamp with time zone/NO"}, {"version", "bigint/NO"},
	}, "columns")
	want(t, d.Rows("SELECT indexname::text FROM pg_indexes WHERE tablename = 'fb_saved_view' "+
		"AND indexdef LIKE 'CREATE UNIQUE INDEX%' AND indexname <> 'fb_saved_view_pkey' "+
		"ORDER BY indexname COLLATE \"C\""),
		[][]string{{"fb_saved_view_private_label"}, {"fb_saved_view_shared_label"}}, "unique indexes")
}

// Rust: framework_migrations.rs::migration_11_creates_the_fixture_table
func TestMigration11CreatesTheFixtureTable(t *testing.T) {
	d := dbtest.Migrated(t)
	want(t, d.Columns("fb_fixture"), []dbtest.Column{
		{"doctype", "text/NO"}, {"name", "text/NO"}, {"app", "text/NO"}, {"checksum", "text/NO"},
		{"applied_at", "timestamp with time zone/NO"},
	}, "columns")
	// One row per (doctype, name).
	d.Exec("INSERT INTO fb_fixture (doctype, name, app, checksum) VALUES ('d', 'n', 'a', 'c')")
	if err := d.TryExec("INSERT INTO fb_fixture (doctype, name, app, checksum) VALUES ('d', 'n', 'b', 'c')"); err == nil {
		t.Error("a second row for the same (doctype, name) was accepted")
	}
}
