package db_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Schema sync against a real PostgreSQL (see dbtest). Ported from
// crates/forgebase-db/tests/schema_sync.rs.
//
// Deferred: schema_sync::migrate_runs_migrations_then_sync. It tests migrate(), the composition of
// the framework migrations, the sync, app migrations, schedules, the workflow backfill and fixtures;
// it moves to P2, with the migrate command of the CLI skeleton.

func registry(t *testing.T, texts ...string) *core.Registry { return dbtest.Registry(t, texts...) }

func mustSync(t *testing.T, d *dbtest.DB, r *core.Registry) *db.SyncReport {
	t.Helper()
	report, err := d.Sync(r)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return report
}

func syncError(t *testing.T, d *dbtest.DB, r *core.Registry) *db.Error {
	t.Helper()
	_, err := d.Sync(r)
	if err == nil {
		t.Fatal("expected the sync to fail")
	}
	return asDbError(t, err)
}

func hasColumn(columns []dbtest.Column, name, typ string) bool {
	return slices.Contains(columns, dbtest.Column{name, typ})
}

func columnNamed(columns []dbtest.Column, name string) bool {
	return slices.ContainsFunc(columns, func(c dbtest.Column) bool { return c[0] == name })
}

func texts[T fmt.Stringer](items []T) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.String()
	}
	return out
}

func kinds(steps []db.Step) []db.StepKind {
	out := make([]db.StepKind, len(steps))
	for i, s := range steps {
		out[i] = s.Kind
	}
	return out
}

func emptyReport(t *testing.T, r *db.SyncReport, label string) {
	t.Helper()
	if len(r.Steps) != 0 || len(r.Warnings) != 0 {
		t.Errorf("%s: steps %v warnings %v", label, texts(r.Steps), texts(r.Warnings))
	}
}

// Rust: schema_sync.rs::sync_creates_table_with_exact_column_types
func TestSyncCreatesTableWithExactColumnTypes(t *testing.T) {
	d := dbtest.Migrated(t)
	report := mustSync(t, d, registry(t, dbtest.Customer))
	want(t, kinds(report.Steps), []db.StepKind{db.StepCreateTable, db.StepAddKeys, db.StepRegisterDocType}, "steps")
	want(t, d.Columns("doc_customer"), []dbtest.Column{
		{"id", "uuid/NO"},
		{"name", "text/NO"},
		{"owner", "text/NO"},
		{"created_at", "timestamp with time zone/NO"},
		{"modified_at", "timestamp with time zone/NO"},
		{"modified_by", "text/NO"},
		{"version", "bigint/NO"},
		{"email", "character varying(100)/YES"},
		{"notes", "text/YES"},
		{"points", "bigint/YES"},
		{"credit_limit", "numeric(12,2)/YES"},
		{"active", "boolean/YES"},
		{"joined_on", "date/YES"},
		{"last_seen", "timestamp with time zone/YES"},
		{"tier", "text/YES"},
	}, "columns")
	want(t, d.Rows("SELECT indexname::text FROM pg_indexes WHERE tablename = 'doc_customer' ORDER BY 1"),
		[][]string{{"doc_customer_email_key"}, {"doc_customer_name_key"}, {"doc_customer_pkey"}}, "indexes")
	want(t, d.Rows("SELECT name::text, table_name::text FROM fb_doctype"), [][]string{{"customer", "doc_customer"}}, "fb_doctype")
}

// Rust: schema_sync.rs::resync_is_a_no_op
func TestResyncIsANoOp(t *testing.T) {
	d := dbtest.Migrated(t)
	r := registry(t, dbtest.Customer)
	mustSync(t, d, r)
	emptyReport(t, mustSync(t, d, r), "again")
}

// Rust: schema_sync.rs::dry_run_creates_nothing_and_reports_sql
func TestDryRunCreatesNothingAndReportsSql(t *testing.T) {
	d := dbtest.Migrated(t)
	report, err := db.Sync(bg, d.Pool, registry(t, dbtest.Customer), dryRun())
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || len(report.Steps) != 3 {
		t.Errorf("%+v", report)
	}
	if !strings.HasPrefix(report.Steps[0].String(), `CREATE TABLE "doc_customer"`) {
		t.Errorf("%s", report.Steps[0])
	}
	if len(d.Columns("doc_customer")) != 0 {
		t.Error("a dry run created the table")
	}
	want(t, d.Rows("SELECT count(*)::text FROM fb_doctype"), [][]string{{"0"}}, "fb_doctype")
}

// Rust: schema_sync.rs::added_field_becomes_nullable_column_and_unique_becomes_index
func TestAddedFieldBecomesNullableColumnAndUniqueBecomesIndex(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	d.Exec("INSERT INTO doc_customer (id, name, owner, created_at, modified_at, modified_by) " +
		"VALUES (gen_random_uuid(), 'a', 'u', now(), now(), 'u')")

	extended := dbtest.Customer + "\n[[fields]]\nname = \"phone\"\ntype = \"data\"\nunique = true\n\n[[fields]]\nname = \"city\"\ntype = \"data\"\n"
	report := mustSync(t, d, registry(t, extended))
	if len(report.Steps) != 3 {
		t.Fatalf("%v", texts(report.Steps))
	}
	// Phases: all added columns first, then the late unique indexes.
	if s := report.Steps[0]; s.Kind != db.StepAddColumn || s.Column != "phone" {
		t.Errorf("%v", s)
	}
	if s := report.Steps[1]; s.Kind != db.StepAddColumn || s.Column != "city" {
		t.Errorf("%v", s)
	}
	if s := report.Steps[2]; s.Kind != db.StepCreateUniqueIndex || s.Column != "phone" {
		t.Errorf("%v", s)
	}
	columns := d.Columns("doc_customer")
	if !hasColumn(columns, "phone", "character varying(140)/YES") || !hasColumn(columns, "city", "character varying(140)/YES") {
		t.Errorf("%v", columns)
	}
	index := d.Rows("SELECT indexdef::text FROM pg_indexes WHERE tablename = 'doc_customer' AND indexdef LIKE '%(phone)'")
	if len(index) != 1 || !strings.Contains(index[0][0], "CREATE UNIQUE INDEX") {
		t.Errorf("%v", index)
	}

	// An existing column that becomes unique later also gets its index.
	later := strings.Replace(extended, "name = \"city\"\ntype = \"data\"", "name = \"city\"\ntype = \"data\"\nunique = true", 1)
	report = mustSync(t, d, registry(t, later))
	if len(report.Steps) != 1 || report.Steps[0].Kind != db.StepCreateUniqueIndex || report.Steps[0].Column != "city" {
		t.Errorf("%v", texts(report.Steps))
	}
}

// Rust: schema_sync.rs::data_max_length_change_is_a_conflict_and_changes_nothing
func TestDataMaxLengthChangeIsAConflictAndChangesNothing(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	// One conflicting change and one otherwise-additive change in the same sync.
	changed := strings.Replace(dbtest.Customer, "max_length = 100", "max_length = 200", 1) +
		"\n[[fields]]\nname = \"extra\"\ntype = \"text\"\n"
	e := syncError(t, d, registry(t, changed))
	if e.Kind != db.ErrSyncConflicts {
		t.Fatalf("%v", e)
	}
	want(t, e.Conflicts, []db.Conflict{{
		Kind: db.ConflictColumnMismatch, Table: "doc_customer", Column: "email",
		Expected: "character varying(200)", Actual: "character varying(100)",
	}}, "conflicts")
	if !strings.Contains(e.Error(), "doc_customer.email") {
		t.Errorf("%v", e)
	}
	if !strings.HasPrefix(e.Error(), "schema sync refused, 1 conflict(s): column doc_customer.email is character varying(100) in the database but the metadata requires character varying(200)") {
		t.Errorf("%v", e)
	}
	columns := d.Columns("doc_customer")
	if !hasColumn(columns, "email", "character varying(100)/YES") {
		t.Errorf("%v", columns)
	}
	if columnNamed(columns, "extra") {
		t.Error("additive step leaked past a conflict")
	}
}

// Rust: schema_sync.rs::removed_field_is_a_warning_and_column_is_kept
func TestRemovedFieldIsAWarningAndColumnIsKept(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	withoutNotes := strings.Replace(dbtest.Customer, "[[fields]]\nname = \"notes\"\ntype = \"text\"\n", "", 1)
	withoutUnique := strings.ReplaceAll(withoutNotes, "unique = true", "")
	report := mustSync(t, d, registry(t, withoutUnique))
	if len(report.Steps) != 0 {
		t.Errorf("%v", texts(report.Steps))
	}
	if !slices.Contains(report.Warnings, db.Warning{Kind: db.WarningColumnNotInMetadata, Table: "doc_customer", Column: "notes"}) {
		t.Errorf("%v", report.Warnings)
	}
	if !slices.Contains(report.Warnings, db.Warning{Kind: db.WarningUniqueRemoved, Table: "doc_customer", Column: "email"}) {
		t.Errorf("%v", report.Warnings)
	}
	if !columnNamed(d.Columns("doc_customer"), "notes") {
		t.Error("the column was dropped")
	}
}

// Rust: schema_sync.rs::doctype_missing_from_registry_is_a_warning
func TestDoctypeMissingFromRegistryIsAWarning(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	report := mustSync(t, d, registry(t))
	want(t, report.Warnings, []db.Warning{{Kind: db.WarningDocTypeNotInRegistry, DocType: "customer"}}, "warnings")
	if len(d.Columns("doc_customer")) == 0 {
		t.Error("the table was dropped")
	}
}

// Rust: schema_sync.rs::unmanaged_existing_table_is_a_conflict
func TestUnmanagedExistingTableIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("CREATE TABLE doc_customer (id int)")
	e := syncError(t, d, registry(t, dbtest.Customer))
	want(t, e.Conflicts, []db.Conflict{{Kind: db.ConflictUnmanagedTable, Table: "doc_customer"}}, "conflicts")
	if len(d.Columns("doc_customer")) != 1 {
		t.Error("the table was changed")
	}
}

// Rust: schema_sync.rs::non_table_relation_with_the_table_name_is_a_conflict
func TestNonTableRelationWithTheTableNameIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("CREATE SEQUENCE doc_customer")
	e := syncError(t, d, registry(t, dbtest.Customer))
	want(t, e.Conflicts, []db.Conflict{{Kind: db.ConflictRelationNameTaken, Name: "doc_customer"}}, "conflicts")
	if !strings.Contains(e.Error(), "doc_customer") {
		t.Errorf("%v", e)
	}
}

const (
	tomlAB = "name = \"a_b\"\nlabel = \"L\"\n[[fields]]\nname = \"c\"\ntype = \"data\"\nunique = true\n"
	tomlA  = "name = \"a\"\nlabel = \"L\"\n[[fields]]\nname = \"b_c\"\ntype = \"data\"\nunique = true\n"
)

// Rust: schema_sync.rs::ambiguous_generated_names_do_not_collide
func TestAmbiguousGeneratedNamesDoNotCollide(t *testing.T) {
	// a_b + c and a + b_c would both be called doc_a_b_c_key under a naming scheme of our own;
	// PostgreSQL picks distinct names. Together, then one after the other.
	together := dbtest.Migrated(t)
	mustSync(t, together, registry(t, tomlAB, tomlA))
	after := dbtest.Migrated(t)
	mustSync(t, after, registry(t, tomlAB))
	mustSync(t, after, registry(t, tomlAB, tomlA))
	for _, d := range []*dbtest.DB{together, after} {
		unique := d.Rows("SELECT count(*)::text FROM pg_indexes WHERE tablename IN ('doc_a', 'doc_a_b') AND indexdef LIKE 'CREATE UNIQUE%'")
		// pkey + name + field for each of the two tables.
		want(t, unique, [][]string{{"6"}}, "unique indexes")
		if len(mustSync(t, d, registry(t, tomlAB, tomlA)).Steps) != 0 {
			t.Error("a re-sync did something")
		}
	}
	// The unique index added to an existing table later is named by PostgreSQL too.
	late := dbtest.Migrated(t)
	withoutUnique := strings.ReplaceAll(tomlA, "unique = true", "")
	mustSync(t, late, registry(t, tomlAB, withoutUnique))
	report := mustSync(t, late, registry(t, tomlAB, tomlA))
	want(t, kinds(report.Steps), []db.StepKind{db.StepCreateUniqueIndex}, "steps")
}

func bare(name string) string { return fmt.Sprintf("name = \"%s\"\nlabel = \"L\"\n", name) }

const itemTOML = "name = \"item\"\nlabel = \"L\"\n[[fields]]\nname = \"code\"\ntype = \"data\"\nunique = true\n"

// Rust: schema_sync.rs::doctype_named_like_a_generated_index_syncs_or_reports_a_conflict
func TestDoctypeNamedLikeAGeneratedIndexSyncsOrReportsAConflict(t *testing.T) {
	for _, clash := range []string{"item_code_key", "item_pkey", "item_name_key"} {
		// Together in one sync: tables are created so that PostgreSQL can pick another index name.
		d := dbtest.Migrated(t)
		reg := registry(t, itemTOML, bare(clash))
		mustSync(t, d, reg)
		if len(mustSync(t, d, reg).Steps) != 0 {
			t.Errorf("%s: a re-sync did something", clash)
		}

		// The DocType added after item already owns the index name: a clear conflict.
		d = dbtest.Migrated(t)
		mustSync(t, d, registry(t, itemTOML))
		e := syncError(t, d, registry(t, itemTOML, bare(clash)))
		want(t, e.Conflicts, []db.Conflict{{Kind: db.ConflictRelationNameTaken, Name: "doc_" + clash}}, clash)
		// Nothing else was applied, and the existing schema still re-syncs cleanly.
		if len(mustSync(t, d, registry(t, itemTOML)).Steps) != 0 {
			t.Errorf("%s: the existing schema does not re-sync cleanly", clash)
		}
	}
}

// Rust: schema_sync.rs::standard_column_drift_is_a_conflict
func TestStandardColumnDriftIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	d.Exec("ALTER TABLE doc_customer ALTER COLUMN owner DROP NOT NULL")
	e := syncError(t, d, registry(t, dbtest.Customer))
	if e.Kind != db.ErrSyncConflicts || len(e.Conflicts) != 1 {
		t.Errorf("%v", e)
	}
}

// Rust: schema_sync.rs::standard_key_drift_is_a_conflict
func TestStandardKeyDriftIsAConflictInTheDatabase(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	d.Exec("ALTER TABLE doc_customer DROP CONSTRAINT doc_customer_name_key")
	e := syncError(t, d, registry(t, dbtest.Customer))
	if len(e.Conflicts) != 1 || e.Conflicts[0].Kind != db.ConflictColumnMismatch ||
		e.Conflicts[0].Column != "name" || e.Conflicts[0].Expected != "unique" {
		t.Errorf("%v", e)
	}
	d.Exec("ALTER TABLE doc_customer ADD UNIQUE (name); ALTER TABLE doc_customer DROP CONSTRAINT doc_customer_pkey")
	e = syncError(t, d, registry(t, dbtest.Customer))
	if len(e.Conflicts) != 1 || e.Conflicts[0].Kind != db.ConflictColumnMismatch ||
		e.Conflicts[0].Column != "id" || e.Conflicts[0].Expected != "primary key" {
		t.Errorf("%v", e)
	}
}

// Rust: schema_sync.rs::not_null_field_column_warns_and_timestamp_precision_conflicts
func TestNotNullFieldColumnWarnsAndTimestampPrecisionConflicts(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	d.Exec("ALTER TABLE doc_customer ALTER COLUMN notes SET NOT NULL")
	report := mustSync(t, d, registry(t, dbtest.Customer))
	want(t, report.Warnings, []db.Warning{{Kind: db.WarningColumnNotNullable, Table: "doc_customer", Column: "notes"}}, "warnings")
	d.Exec("ALTER TABLE doc_customer ALTER COLUMN last_seen TYPE timestamptz(0)")
	e := syncError(t, d, registry(t, dbtest.Customer))
	if len(e.Conflicts) != 1 || e.Conflicts[0].Kind != db.ConflictColumnMismatch ||
		e.Conflicts[0].Column != "last_seen" || e.Conflicts[0].Actual != "timestamp(0) with time zone" {
		t.Errorf("%v", e)
	}
}

// Rust: schema_sync.rs::adding_unique_over_duplicates_fails_and_applies_nothing
func TestAddingUniqueOverDuplicatesFailsAndAppliesNothing(t *testing.T) {
	d := dbtest.Migrated(t)
	plain := strings.ReplaceAll(dbtest.Customer, "unique = true", "")
	mustSync(t, d, registry(t, plain))
	for _, name := range []string{"a", "b"} {
		d.Exec(fmt.Sprintf("INSERT INTO doc_customer (id, name, owner, created_at, modified_at, modified_by, email) "+
			"VALUES (gen_random_uuid(), '%s', 'u', now(), now(), 'u', 'same@x')", name))
	}
	changed := dbtest.Customer + "\n[[fields]]\nname = \"extra\"\ntype = \"text\"\n"
	e := syncError(t, d, registry(t, changed))
	if e.Kind != db.ErrPostgres || !strings.Contains(e.Error(), "23505") {
		t.Errorf("%v", e)
	}
	if columnNamed(d.Columns("doc_customer"), "extra") {
		t.Error("a step of the failed sync was kept")
	}
}

// Rust: schema_sync.rs::huge_lock_timeout_is_clamped
func TestHugeLockTimeoutIsClamped(t *testing.T) {
	d := dbtest.Migrated(t)
	_, err := db.Sync(bg, d.Pool, registry(t, dbtest.Customer),
		db.SyncOptions{DryRun: true, LockTimeout: 100_000_000 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// So is a timeout below 1 ms (0 would disable the timeout), and a negative one.
	for _, timeout := range []time.Duration{0, time.Nanosecond, -time.Second} {
		if _, err := db.Sync(bg, d.Pool, registry(t, dbtest.Customer), db.SyncOptions{DryRun: true, LockTimeout: timeout}); err != nil {
			t.Errorf("%v: %v", timeout, err)
		}
	}
}

// Not in Rust: the setting that PostgreSQL receives. A zero LockTimeout is 1 ms (Rust:
// Duration::ZERO, as the CLI option --lock-timeout-ms 0 gives), not the default and not "no
// timeout"; the clamp is [1 ms, 2^31-1 ms].
func TestLockTimeoutSettingClamps(t *testing.T) {
	for timeout, want := range map[time.Duration]string{
		0:                             "1ms",
		time.Nanosecond:               "1ms",
		999 * time.Microsecond:        "1ms",
		-time.Hour:                    "1ms",
		time.Millisecond:              "1ms",
		5 * time.Second:               "5000ms",
		100_000_000 * time.Second:     "2147483647ms",
		2147483647 * time.Millisecond: "2147483647ms",
	} {
		if got := db.LockTimeoutSettingForTest(timeout); got != want {
			t.Errorf("%v: %q, want %q", timeout, got, want)
		}
	}
	if db.DefaultSyncOptions().LockTimeout != 5*time.Second || db.DefaultSyncOptions().DryRun {
		t.Errorf("%+v", db.DefaultSyncOptions())
	}
}

// Not in Rust: the lock timeout that Sync sets in PostgreSQL is the one given, 1 ms for zero. A
// held table lock makes a sync with LockTimeout 0 fail at once with ErrLockTimeout.
func TestZeroLockTimeoutIsOneMillisecond(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	blocker, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := tx.Exec(bg, "LOCK TABLE doc_customer IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	extended := dbtest.Customer + "\n[[fields]]\nname = \"extra\"\ntype = \"text\"\n"
	start := time.Now()
	_, err = db.Sync(bg, d.Pool, registry(t, extended), db.SyncOptions{})
	if e := asDbError(t, err); e.Kind != db.ErrLockTimeout {
		t.Errorf("%v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("a 1 ms lock timeout took %v", elapsed)
	}
}

// Rust: schema_sync.rs::lock_timeout_reports_a_retryable_error
func TestLockTimeoutReportsARetryableError(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))

	// Another connection holds a conflicting table lock while we try to add a column.
	blocker, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := blocker.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, "LOCK TABLE doc_customer IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

	extended := dbtest.Customer + "\n[[fields]]\nname = \"extra\"\ntype = \"text\"\n"
	_, err = db.Sync(bg, d.Pool, registry(t, extended), db.SyncOptions{LockTimeout: 200 * time.Millisecond})
	if e := asDbError(t, err); e.Kind != db.ErrLockTimeout {
		t.Errorf("%v", err)
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("%v", err)
	}

	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	blocker.Release()
	// After the lock is gone the same sync succeeds: the failed one left nothing behind.
	report := mustSync(t, d, registry(t, extended))
	if len(report.Steps) != 1 {
		t.Errorf("%v", texts(report.Steps))
	}
}

// Rust: schema_sync.rs::concurrent_syncs_both_succeed
func TestConcurrentSyncsBothSucceed(t *testing.T) {
	d := dbtest.Migrated(t)
	r := registry(t, dbtest.Customer)
	a, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	var wg sync.WaitGroup
	reports := make([]*db.SyncReport, 2)
	errs := make([]error, 2)
	for i, conn := range []db.TxStarter{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = db.Sync(bg, conn, r, db.DefaultSyncOptions())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Exactly one of them did the work, the other saw it already done.
	if n := len(reports[0].Steps) + len(reports[1].Steps); n != 3 {
		t.Errorf("%d steps in total: %v %v", n, texts(reports[0].Steps), texts(reports[1].Steps))
	}
}

// Rust: schema_sync.rs::sync_before_framework_migrations_fails_clearly
func TestSyncBeforeFrameworkMigrationsFailsClearly(t *testing.T) {
	d := dbtest.New(t)
	e := syncError(t, d, registry(t, dbtest.Customer))
	if e.Kind != db.ErrFrameworkNotMigrated || !strings.Contains(e.Error(), "migrations") {
		t.Errorf("%v", e)
	}
}

// Rust: schema_sync.rs::long_doctype_and_field_names_get_postgres_chosen_bounded_names
func TestLongDoctypeAndFieldNamesGetPostgresChosenBoundedNames(t *testing.T) {
	d := dbtest.Migrated(t)
	doctype := "d" + strings.Repeat("x", 58)
	field := "f" + strings.Repeat("y", 62)
	toml := fmt.Sprintf("name = \"%s\"\nlabel = \"L\"\n[[fields]]\nname = \"%s\"\ntype = \"data\"\nunique = true\n", doctype, field)
	mustSync(t, d, registry(t, toml))
	names := d.Rows("SELECT indexname::text FROM pg_indexes WHERE tablename LIKE 'doc_dxxx%' ORDER BY 1")
	if len(names) != 3 {
		t.Errorf("%v", names)
	}
	for _, n := range names {
		if len(n[0]) > 63 {
			t.Errorf("%s is longer than 63 bytes", n[0])
		}
	}
	if len(mustSync(t, d, registry(t, toml)).Steps) != 0 {
		t.Error("a re-sync did something")
	}
}

// longNamed is a DocType with no fields, or one unique field.
func longNamed(name, uniqueField string) string {
	toml := bare(name)
	if uniqueField != "" {
		toml += fmt.Sprintf("[[fields]]\nname = \"%s\"\ntype = \"data\"\nunique = true\n", uniqueField)
	}
	return toml
}

// Rust: schema_sync.rs::truncated_generated_names_never_collide_with_tables_in_one_sync
func TestTruncatedGeneratedNamesNeverCollideWithTablesInOneSync(t *testing.T) {
	x := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		label string
		tomls []string
	}{
		// R01: the name unique constraint of the first is named like the second table.
		{"name_key", []string{longNamed(x(51), ""), longNamed(x(50)+"_name_key", "")}},
		// R02: the primary key index.
		{"pkey", []string{longNamed(x(55), ""), longNamed(x(54)+"_pkey", "")}},
		// R03: a unique field.
		{"field_key", []string{longNamed(strings.Repeat("y", 50), "codex"), longNamed(strings.Repeat("y", 49)+"_codex_key", "")}},
		// Cycle: each table's generated name is the other table's name.
		{"cycle", []string{longNamed(strings.Repeat("w", 50)+"_name_key", ""), longNamed(strings.Repeat("w", 50)+"_nam_pkey", "")}},
	}
	for _, c := range cases {
		d := dbtest.Migrated(t)
		reg := registry(t, c.tomls...)
		report, err := d.Sync(reg)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		if len(report.Steps) != 3*len(c.tomls) {
			t.Errorf("%s: %d steps", c.label, len(report.Steps))
		}
		if len(mustSync(t, d, reg).Steps) != 0 {
			t.Errorf("%s: a re-sync did something", c.label)
		}
	}
}

// Rust: schema_sync.rs::late_unique_index_does_not_collide_with_a_new_table
func TestLateUniqueIndexDoesNotCollideWithANewTable(t *testing.T) {
	// R05: the late index on an existing table is named like a table created in the same sync.
	v := strings.Repeat("v", 50)
	existing := longNamed(v, "")
	withUnique := longNamed(v, "codex")
	clash := longNamed(strings.Repeat("v", 49)+"_codex_idx", "")
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, existing))
	reg := registry(t, withUnique, clash)
	mustSync(t, d, reg)
	if len(mustSync(t, d, reg).Steps) != 0 {
		t.Error("a re-sync did something")
	}
}

// Rust: schema_sync.rs::view_with_the_table_name_is_a_name_conflict_not_unmanaged
func TestViewWithTheTableNameIsANameConflictNotUnmanaged(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("CREATE VIEW doc_customer AS SELECT 1 AS id")
	e := syncError(t, d, registry(t, dbtest.Customer))
	want(t, e.Conflicts, []db.Conflict{{Kind: db.ConflictRelationNameTaken, Name: "doc_customer"}}, "conflicts")
}

// Rust: schema_sync.rs::domain_with_the_table_name_is_a_name_conflict
func TestDomainWithTheTableNameIsANameConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("CREATE DOMAIN doc_customer AS text")
	e := syncError(t, d, registry(t, dbtest.Customer))
	want(t, e.Conflicts, []db.Conflict{{Kind: db.ConflictRelationNameTaken, Name: "doc_customer"}}, "conflicts")
	// A synced table's own row type is not a conflict.
	d = dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.Customer))
	if len(mustSync(t, d, registry(t, dbtest.Customer)).Steps) != 0 {
		t.Error("a re-sync did something")
	}
}

// Rust: schema_sync.rs::schema_names_needing_quotes_are_handled_by_name
//
// Schema names that need quoting (mixed case, space, dot) must be read by name, never parsed as
// identifiers, and a lower-case twin schema must not be read or written.
func TestSchemaNamesNeedingQuotesAreHandledByName(t *testing.T) {
	for _, c := range []struct{ schema, twin string }{
		{"MySchema", "myschema"},
		{"my schema", ""},
		{"a.b", ""},
	} {
		d := dbtest.New(t)
		conn, err := d.Pool.Acquire(bg)
		if err != nil {
			t.Fatal(err)
		}
		q := db.QuoteIdent(c.schema)
		if _, err := conn.Exec(bg, "CREATE SCHEMA "+q); err != nil {
			t.Fatal(err)
		}
		if c.twin != "" {
			// A decoy that must neither conflict nor be modified.
			if _, err := conn.Exec(bg, fmt.Sprintf("CREATE SCHEMA %s; "+
				"CREATE TABLE %s.doc_customer (id uuid PRIMARY KEY, name text UNIQUE)", c.twin, c.twin)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.Exec(bg, "SET search_path TO "+q); err != nil {
			t.Fatal(err)
		}
		if _, err := db.MigrateFramework(bg, conn, false); err != nil {
			t.Fatal(err)
		}

		reg := registry(t, dbtest.Customer)
		first, err := db.Sync(bg, conn, reg, db.DefaultSyncOptions())
		if err != nil {
			t.Fatal(err)
		}
		if first.Steps[0].Kind != db.StepCreateTable {
			t.Errorf("%s: %v", c.schema, texts(first.Steps))
		}
		again, err := db.Sync(bg, conn, reg, db.DefaultSyncOptions())
		if err != nil {
			t.Fatal(err)
		}
		emptyReport(t, again, c.schema)

		count := func(sql string, arg string) string {
			var n string
			if err := conn.QueryRow(bg, sql, arg).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := count("SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE n.nspname = $1 AND c.relname IN ('doc_customer', 'fb_doctype', 'fb_migration')", c.schema); n != "3" {
			t.Errorf("%s: %s tables in the schema", c.schema, n)
		}
		if c.twin != "" {
			if n := count("SELECT count(*)::text FROM information_schema.columns WHERE table_schema = $1", c.twin); n != "2" {
				t.Errorf("decoy columns: %s", n)
			}
			// doc_customer, its pkey and its name key; nothing else.
			if n := count("SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
				"WHERE n.nspname = $1", c.twin); n != "3" {
				t.Errorf("decoy relations: %s", n)
			}
		}
		conn.Release()
	}
}

// foreignKeys are the foreign keys of a table as pg_get_constraintdef text, sorted.
func foreignKeys(d *dbtest.DB, table string) []string {
	out := []string{}
	for _, r := range d.Rows(fmt.Sprintf("SELECT pg_get_constraintdef(oid) FROM pg_constraint "+
		"WHERE conrelid = '%s'::regclass AND contype = 'f' ORDER BY 1", table)) {
		out = append(out, r[0])
	}
	return out
}

// indexedColumns are the single-column indexes of a table, as "column (unique|plain)".
func indexedColumns(d *dbtest.DB, table string) []string {
	out := []string{}
	for _, r := range d.Rows(fmt.Sprintf("SELECT a.attname::text || CASE WHEN i.indisunique THEN ' (unique)' ELSE ' (plain)' END "+
		"FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0] "+
		"WHERE i.indrelid = '%s'::regclass AND i.indnatts = 1 ORDER BY 1", table)) {
		out = append(out, r[0])
	}
	return out
}

// Rust: schema_sync.rs::link_columns_get_a_foreign_key_and_an_index
func TestLinkColumnsGetAForeignKeyAndAnIndex(t *testing.T) {
	d := dbtest.Migrated(t)
	report := mustSync(t, d, registry(t, dbtest.SalesOrder, dbtest.Person))
	// Foreign keys come after the keys of every new table; indexes after the foreign keys.
	want(t, kinds(report.Steps), []db.StepKind{
		db.StepCreateTable, db.StepCreateTable, db.StepAddKeys, db.StepAddKeys, db.StepAddForeignKeys,
		db.StepCreateIndex, db.StepCreateIndex, db.StepCreateIndex, db.StepRegisterDocType, db.StepRegisterDocType,
	}, "steps")
	var links []dbtest.Column
	for _, c := range d.Columns("doc_sales_order") {
		if slices.Contains([]string{"customer", "follows", "approver"}, c[0]) {
			links = append(links, c)
		}
	}
	want(t, links, []dbtest.Column{{"customer", "uuid/YES"}, {"follows", "uuid/YES"}, {"approver", "uuid/YES"}}, "link columns")
	want(t, foreignKeys(d, "doc_sales_order"), []string{
		"FOREIGN KEY (approver) REFERENCES doc_person(id) ON DELETE RESTRICT",
		"FOREIGN KEY (customer) REFERENCES doc_person(id) ON DELETE RESTRICT",
		"FOREIGN KEY (follows) REFERENCES doc_sales_order(id) ON DELETE RESTRICT",
	}, "foreign keys")
	want(t, indexedColumns(d, "doc_sales_order"), []string{
		"approver (plain)", "customer (plain)", "follows (plain)", "id (unique)", "name (unique)",
	}, "indexes")
	if len(foreignKeys(d, "doc_person")) != 0 {
		t.Error("person has foreign keys")
	}
}

// Rust: schema_sync.rs::doctypes_linking_each_other_and_themselves_sync_in_one_run
func TestDoctypesLinkingEachOtherAndThemselvesSyncInOneRun(t *testing.T) {
	d := dbtest.Migrated(t)
	a := "name = \"alpha\"\nlabel = \"A\"\n[[fields]]\nname = \"beta\"\ntype = \"link\"\ntarget = \"beta\"\n" +
		"[[fields]]\nname = \"me\"\ntype = \"link\"\ntarget = \"alpha\"\n"
	b := "name = \"beta\"\nlabel = \"B\"\n[[fields]]\nname = \"alpha\"\ntype = \"link\"\ntarget = \"alpha\"\nunique = true\n"
	r := registry(t, a, b)
	mustSync(t, d, r)
	want(t, foreignKeys(d, "doc_alpha"), []string{
		"FOREIGN KEY (beta) REFERENCES doc_beta(id) ON DELETE RESTRICT",
		"FOREIGN KEY (me) REFERENCES doc_alpha(id) ON DELETE RESTRICT",
	}, "alpha")
	want(t, foreignKeys(d, "doc_beta"), []string{"FOREIGN KEY (alpha) REFERENCES doc_alpha(id) ON DELETE RESTRICT"}, "beta")
	// A unique Link field is indexed by its unique constraint only.
	want(t, indexedColumns(d, "doc_beta"), []string{"alpha (unique)", "id (unique)", "name (unique)"}, "indexes")
	emptyReport(t, mustSync(t, d, r), "again")
}

// Rust: schema_sync.rs::resync_with_links_is_a_no_op
func TestResyncWithLinksIsANoOp(t *testing.T) {
	d := dbtest.Migrated(t)
	r := registry(t, dbtest.SalesOrder, dbtest.Person)
	mustSync(t, d, r)
	emptyReport(t, mustSync(t, d, r), "again")
}

// Rust: schema_sync.rs::link_column_added_later_gets_a_foreign_key_and_an_index
func TestLinkColumnAddedLaterGetsAForeignKeyAndAnIndex(t *testing.T) {
	d := dbtest.Migrated(t)
	withoutLink := "name = \"sales_order\"\nlabel = \"S\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
	mustSync(t, d, registry(t, dbtest.Person, withoutLink))
	d.Exec("INSERT INTO doc_sales_order (id, name, owner, created_at, modified_at, modified_by, version, title) " +
		"VALUES (gen_random_uuid(), 'old', 'x', now(), now(), 'x', 1, 't')")

	withLink := "name = \"sales_order\"\nlabel = \"S\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n" +
		"[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"person\"\n"
	report := mustSync(t, d, registry(t, dbtest.Person, withLink))
	want(t, texts(report.Steps), []string{
		`ALTER TABLE "doc_sales_order" ADD COLUMN "customer" uuid;`,
		`ALTER TABLE "doc_sales_order" ADD FOREIGN KEY ("customer") REFERENCES "doc_person" ("id") ON DELETE RESTRICT;`,
		`CREATE INDEX ON "doc_sales_order" ("customer");`,
	}, "steps")
	want(t, foreignKeys(d, "doc_sales_order"), []string{"FOREIGN KEY (customer) REFERENCES doc_person(id) ON DELETE RESTRICT"}, "fks")
	if !slices.Contains(indexedColumns(d, "doc_sales_order"), "customer (plain)") {
		t.Error("no index on the new Link column")
	}
	// The existing row stays (its new link is NULL), and a re-sync changes nothing.
	want(t, d.Rows("SELECT count(*)::text FROM doc_sales_order WHERE customer IS NULL"), [][]string{{"1"}}, "rows")
	if again := mustSync(t, d, registry(t, dbtest.Person, withLink)); len(again.Steps) != 0 {
		t.Errorf("%v", texts(again.Steps))
	}
}

func syncedWithLinks(t *testing.T) *dbtest.DB {
	t.Helper()
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, dbtest.SalesOrder, dbtest.Person))
	return d
}

func conflictTexts(t *testing.T, err *db.Error) []string {
	t.Helper()
	if err.Kind != db.ErrSyncConflicts {
		t.Fatalf("expected conflicts, got %v", err)
	}
	return texts(err.Conflicts)
}

// Rust: schema_sync.rs::a_dropped_foreign_key_is_a_conflict
func TestADroppedForeignKeyIsAConflict(t *testing.T) {
	d := syncedWithLinks(t)
	d.Exec("ALTER TABLE doc_sales_order DROP CONSTRAINT doc_sales_order_customer_fkey")
	ts := conflictTexts(t, syncError(t, d, registry(t, dbtest.SalesOrder, dbtest.Person)))
	if len(ts) != 1 || !strings.Contains(ts[0], "doc_sales_order.customer is missing") ||
		!strings.Contains(ts[0], `REFERENCES "doc_person" ("id") ON DELETE RESTRICT`) {
		t.Errorf("%v", ts)
	}
	// Nothing was repaired behind the user's back.
	if n := len(foreignKeys(d, "doc_sales_order")); n != 2 {
		t.Errorf("%d foreign keys", n)
	}
}

// Rust: schema_sync.rs::a_not_validated_foreign_key_is_a_conflict
func TestANotValidatedForeignKeyIsAConflict(t *testing.T) {
	d := syncedWithLinks(t)
	d.Exec("ALTER TABLE doc_sales_order DROP CONSTRAINT doc_sales_order_customer_fkey; " +
		"ALTER TABLE doc_sales_order ADD FOREIGN KEY (customer) REFERENCES doc_person (id) " +
		"ON DELETE RESTRICT NOT VALID")
	ts := conflictTexts(t, syncError(t, d, registry(t, dbtest.SalesOrder, dbtest.Person)))
	if len(ts) != 1 || !strings.Contains(ts[0], "doc_sales_order.customer") || !strings.Contains(ts[0], "not validated") {
		t.Errorf("%v", ts)
	}
	// Nothing was applied or repaired.
	if n := len(foreignKeys(d, "doc_sales_order")); n != 3 {
		t.Errorf("%d foreign keys", n)
	}
}

// Rust: schema_sync.rs::a_cascade_foreign_key_is_a_conflict
func TestACascadeForeignKeyIsAConflict(t *testing.T) {
	d := syncedWithLinks(t)
	d.Exec("ALTER TABLE doc_sales_order DROP CONSTRAINT doc_sales_order_customer_fkey; " +
		"ALTER TABLE doc_sales_order ADD FOREIGN KEY (customer) REFERENCES doc_person (id) ON DELETE CASCADE")
	ts := conflictTexts(t, syncError(t, d, registry(t, dbtest.SalesOrder, dbtest.Person)))
	if len(ts) != 1 || !strings.Contains(ts[0], "ON DELETE CASCADE") {
		t.Errorf("%v", ts)
	}
	// The default rule (NO ACTION) is not RESTRICT either.
	d.Exec("ALTER TABLE doc_sales_order DROP CONSTRAINT doc_sales_order_customer_fkey; " +
		"ALTER TABLE doc_sales_order ADD FOREIGN KEY (customer) REFERENCES doc_person (id)")
	ts = conflictTexts(t, syncError(t, d, registry(t, dbtest.SalesOrder, dbtest.Person)))
	if !strings.Contains(ts[0], "ON DELETE NO ACTION") {
		t.Errorf("%v", ts)
	}
}

// Rust: schema_sync.rs::a_foreign_key_to_the_wrong_table_is_a_conflict
func TestAForeignKeyToTheWrongTableIsAConflict(t *testing.T) {
	d := syncedWithLinks(t)
	d.Exec("ALTER TABLE doc_sales_order DROP CONSTRAINT doc_sales_order_customer_fkey; " +
		"ALTER TABLE doc_sales_order ADD FOREIGN KEY (customer) REFERENCES doc_sales_order (id) ON DELETE RESTRICT")
	ts := conflictTexts(t, syncError(t, d, registry(t, dbtest.SalesOrder, dbtest.Person)))
	if len(ts) != 1 || !strings.Contains(ts[0], `REFERENCES "doc_sales_order" ("id")`) {
		t.Errorf("%v", ts)
	}
}

// Rust: schema_sync.rs::a_foreign_key_on_a_column_that_is_not_a_link_is_a_conflict
func TestAForeignKeyOnAColumnThatIsNotALinkIsAConflict(t *testing.T) {
	d := syncedWithLinks(t)
	// Give a plain text column a foreign key by hand (to a unique text column of another table).
	withExtra := strings.Replace(dbtest.SalesOrder, "[[fields]]\nname = \"title\"",
		"[[fields]]\nname = \"extra\"\ntype = \"text\"\n[[fields]]\nname = \"title\"", 1)
	// Without a foreign key the extra field is just added.
	mustSync(t, d, registry(t, withExtra, dbtest.Person))
	d.Exec("ALTER TABLE doc_person ADD COLUMN ref text UNIQUE; " +
		"ALTER TABLE doc_sales_order ADD FOREIGN KEY (extra) REFERENCES doc_person (ref)")
	ts := conflictTexts(t, syncError(t, d, registry(t, withExtra, dbtest.Person)))
	if len(ts) != 1 || !strings.Contains(ts[0], "doc_sales_order.extra") || !strings.Contains(ts[0], "requires no foreign key") {
		t.Errorf("%v", ts)
	}
}

// Rust: schema_sync.rs::a_missing_link_index_is_added
func TestAMissingLinkIndexIsAdded(t *testing.T) {
	d := syncedWithLinks(t)
	index := d.Rows("SELECT i.indexrelid::regclass::text FROM pg_index i " +
		"JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0] " +
		"WHERE i.indrelid = 'doc_sales_order'::regclass AND a.attname = 'customer'")[0][0]
	d.Exec("DROP INDEX " + index)
	report := mustSync(t, d, registry(t, dbtest.SalesOrder, dbtest.Person))
	if len(report.Steps) != 1 || report.Steps[0].Kind != db.StepCreateIndex || report.Steps[0].Column != "customer" ||
		report.Steps[0].SQL != `CREATE INDEX ON "doc_sales_order" ("customer")` {
		t.Errorf("%v", texts(report.Steps))
	}
	if !slices.Contains(indexedColumns(d, "doc_sales_order"), "customer (plain)") {
		t.Error("the index was not created")
	}
}

// Rust: schema_sync.rs::an_unknown_link_target_is_refused_before_the_database_is_touched
func TestAnUnknownLinkTargetIsRefusedBeforeTheDatabaseIsTouched(t *testing.T) {
	d := dbtest.Migrated(t)
	// person is not in the registry.
	e := syncError(t, d, registry(t, dbtest.SalesOrder))
	if e.Kind != db.ErrInvalidMetadata || len(e.Problems) != 2 {
		t.Fatalf("expected InvalidMetadata with 2 problems, got %v", e)
	}
	if !strings.Contains(e.Error(), "doctype `sales_order`: field `customer` links to unknown doctype `person`") {
		t.Errorf("%v", e)
	}
	want(t, d.Rows("SELECT count(*)::text FROM fb_doctype"), [][]string{{"0"}}, "fb_doctype")
}

// Rust: schema_sync.rs::dry_run_shows_the_foreign_key_sql_and_keeps_nothing
func TestDryRunShowsTheForeignKeySqlAndKeepsNothing(t *testing.T) {
	d := dbtest.Migrated(t)
	report, err := db.Sync(bg, d.Pool, registry(t, dbtest.SalesOrder, dbtest.Person), dryRun())
	if err != nil {
		t.Fatal(err)
	}
	text := texts(report.Steps)
	if !slices.Contains(text, `ALTER TABLE "doc_sales_order" ADD FOREIGN KEY ("customer") REFERENCES "doc_person" ("id") ON DELETE RESTRICT, `+
		`ADD FOREIGN KEY ("follows") REFERENCES "doc_sales_order" ("id") ON DELETE RESTRICT, `+
		`ADD FOREIGN KEY ("approver") REFERENCES "doc_person" ("id") ON DELETE RESTRICT;`) {
		t.Errorf("%#v", text)
	}
	if !slices.Contains(text, `CREATE INDEX ON "doc_sales_order" ("customer");`) {
		t.Errorf("%#v", text)
	}
	want(t, d.Rows("SELECT coalesce(to_regclass('doc_sales_order')::text, 'none')"), [][]string{{"none"}}, "table")
}

// dryRun is DefaultSyncOptions with DryRun set (the Rust SyncOptions { dry_run: true, ..default() }).
func dryRun() db.SyncOptions {
	options := db.DefaultSyncOptions()
	options.DryRun = true
	return options
}
