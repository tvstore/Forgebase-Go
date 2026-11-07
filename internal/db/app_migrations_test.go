package db_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The app data migrations against a real PostgreSQL (see dbtest). Ported from
// crates/forgebase-db/tests/app_migrations.rs; the behaviour of the app migrations as an app sees it (order,
// checksums, refusals, fb_app) is tested through the public API in apps_test.go at the root of the module
// (Rust: crates/forgebase/tests/apps.rs).

// migrateKey is the key of the migration lock (db.MigrateLockKey).
const migrateKey = db.MigrateLockKey

var slowApp = db.AppSpec{Name: "slow_app", Version: "1", Migrations: []db.AppMigration{{
	Version: 1, Name: "slow",
	SQL: "SELECT pg_sleep(0.5); CREATE TABLE zz_slow (n integer); INSERT INTO zz_slow VALUES (1)",
}}}

func options(timeout time.Duration) db.SyncOptions { return db.SyncOptions{LockTimeout: timeout} }

// Rust: app_migrations.rs::two_concurrent_migrates_do_not_interleave
func TestTwoConcurrentMigratesDoNotInterleave(t *testing.T) {
	d := dbtest.New(t)
	registry := core.NewRegistry()
	reports := make([]*db.MigrateReport, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range reports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = db.MigrateApps(bg, d.Pool, registry, []db.AppSpec{slowApp}, options(10*time.Second))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
	// One of them applied the migration, the other waited and found it applied.
	if n := len(reports[0].AppMigrations) + len(reports[1].AppMigrations); n != 1 {
		t.Errorf("%d migrations applied in total", n)
	}
	want(t, d.Rows("SELECT count(*)::text FROM zz_slow"), [][]string{{"1"}}, "zz_slow")
}

// Rust: app_migrations.rs::a_held_migration_lock_makes_migrate_time_out_and_nothing_is_left_behind
func TestAHeldMigrationLockMakesMigrateTimeOutAndNothingIsLeftBehind(t *testing.T) {
	d := dbtest.New(t)
	registry := core.NewRegistry()
	// Another session holds the lock, like a migrate that is still running.
	holder, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(bg, "SELECT pg_advisory_lock($1)", migrateKey); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	_, err = db.MigrateApps(ctx, d.Pool, registry, []db.AppSpec{slowApp}, options(200*time.Millisecond))
	if err == nil {
		t.Fatal("must time out")
	}
	if ctx.Err() != nil {
		t.Fatal("MigrateApps did not give up waiting for the lock")
	}
	if e := asDbError(t, err); e.Kind != db.ErrLockTimeout {
		t.Errorf("%v", err)
	}
	want(t, d.Rows("SELECT coalesce(to_regclass('fb_migration')::text, 'none')"), [][]string{{"none"}}, "fb_migration")

	// Released: the migrate works, and releases its own lock again (the holder can take it).
	if _, err := holder.Exec(bg, "SELECT pg_advisory_unlock($1)", migrateKey); err != nil {
		t.Fatal(err)
	}
	done, err := db.MigrateApps(bg, d.Pool, registry, []db.AppSpec{slowApp}, db.DefaultSyncOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(done.AppMigrations) != 1 {
		t.Errorf("%+v", done.AppMigrations)
	}
	var free bool
	if err := holder.QueryRow(bg, "SELECT pg_try_advisory_lock($1)", migrateKey).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if !free {
		t.Error("MigrateApps left the advisory lock held")
	}
}

// heldLocks counts the advisory locks held in this test's database (the view is cluster-wide).
const heldLocks = "SELECT count(*)::text FROM pg_locks WHERE locktype = 'advisory' " +
	"AND database = (SELECT oid FROM pg_database WHERE datname = current_database())"

func waitForLocks(d *dbtest.DB, count string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if rows := d.Rows(heldLocks); rows[0][0] == count {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Rust: app_migrations.rs::a_cancelled_migrate_does_not_leave_its_lock_on_a_pooled_connection
func TestACancelledMigrateDoesNotLeaveItsLockOnAPooledConnection(t *testing.T) {
	d := dbtest.New(t)
	sleepy := db.AppSpec{Name: "sleepy_app", Migrations: []db.AppMigration{{Version: 1, Name: "sleepy", SQL: "SELECT pg_sleep(1.5)"}}}
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := db.MigrateApps(ctx, d.Pool, core.NewRegistry(), []db.AppSpec{sleepy}, db.DefaultSyncOptions())
		finished <- err
	}()
	// Wait until the migrate holds its lock, then cancel it in the middle of the migration.
	if !waitForLocks(d, "1", 5*time.Second) {
		t.Fatal("the migrate never took its lock")
	}
	cancel()
	if err := <-finished; err == nil {
		t.Error("a cancelled migrate must fail")
	}
	// The connection was closed, not returned to the pool alive: once the server notices the lock is gone.
	if !waitForLocks(d, "0", 10*time.Second) {
		t.Error("a cancelled migrate left the advisory lock held")
	}
}

// Rust: app_migrations.rs::an_app_migration_gives_up_waiting_for_a_lock_after_the_lock_timeout
func TestAnAppMigrationGivesUpWaitingForALockAfterTheLockTimeout(t *testing.T) {
	d := dbtest.New(t)
	d.Exec("CREATE TABLE zz_locked (n integer)")
	app := db.AppSpec{Name: "blocked_app", Migrations: []db.AppMigration{{
		Version: 1, Name: "blocked",
		SQL: "INSERT INTO zz_locked VALUES (1); ALTER TABLE zz_locked ADD COLUMN extra text",
	}}}
	// Another transaction holds a conflicting lock on the table and does not end.
	holder, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(bg, "LOCK TABLE zz_locked IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// The timeout of the test itself: a regression (no lock timeout) fails fast instead of hanging.
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	_, err = db.MigrateApps(ctx, d.Pool, core.NewRegistry(), []db.AppSpec{app}, options(300*time.Millisecond))
	if err == nil {
		t.Fatal("must time out")
	}
	e := asDbError(t, err)
	if e.Kind != db.ErrAppMigrationFailed || e.Version != 1 || !strings.Contains(e.Message, "timed out waiting for a database lock") {
		t.Errorf("%v", err)
	}
	if time.Since(started) >= 5*time.Second {
		t.Errorf("took %v", time.Since(started))
	}
	if err := holder.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	// Nothing was recorded.
	want(t, d.Rows("SELECT count(*)::text FROM fb_app_migration"), [][]string{{"0"}}, "fb_app_migration")
}

// Rust: app_migrations.rs::a_dry_run_with_pending_framework_migrations_lists_every_app_migration
func TestADryRunWithPendingFrameworkMigrationsListsEveryAppMigration(t *testing.T) {
	// Only the migrations before 0004 are applied: the app tables do not exist, so no app migration can
	// have been applied and all of them are pending; the sync plan is not known.
	d := dbtest.Migrated(t)
	d.Exec("DROP TABLE fb_app, fb_app_migration; DELETE FROM fb_migration WHERE version = 4")
	exists, err := db.AppMigrationTablesExistForTest(bg, d.Pool)
	if err != nil || exists {
		t.Fatalf("the tables exist: %v %v", exists, err)
	}
	report, err := db.MigrateApps(bg, d.Pool, core.NewRegistry(), []db.AppSpec{slowApp}, db.SyncOptions{DryRun: true, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Migrations) != 1 || report.Sync != nil || len(report.AppMigrations) != 1 {
		t.Errorf("%+v", report)
	}
	// A dry run keeps nothing.
	want(t, d.Rows("SELECT (to_regclass('fb_app_migration') IS NULL)::text"), [][]string{{"true"}}, "no tables")
}

// Rust: app_migrations.rs::the_app_migration_tables_are_found_once_they_exist
func TestTheAppMigrationTablesAreFoundOnceTheyExist(t *testing.T) {
	// The decision behind the dry run above: with the tables present (a later framework migration pending)
	// the app migrations are verified, not blindly listed.
	d := dbtest.Migrated(t)
	exists, err := db.AppMigrationTablesExistForTest(bg, d.Pool)
	if err != nil || !exists {
		t.Errorf("%v %v", exists, err)
	}
}

var (
	one       = db.AppMigration{Version: 1, Name: "one", SQL: "CREATE TABLE zz_one (n integer)"}
	two       = db.AppMigration{Version: 2, Name: "two", SQL: "CREATE TABLE zz_two (n integer)"}
	oneEdited = db.AppMigration{Version: 1, Name: "one", SQL: "CREATE TABLE zz_one (n bigint)"} // other SQL: another checksum
)

func verifyApp(migrations ...db.AppMigration) []db.AppSpec {
	return []db.AppSpec{{Name: "verify_app", Version: "1", Migrations: migrations}}
}

// Rust: app_migrations.rs::a_dry_run_with_pending_framework_migrations_still_verifies_the_app_migrations
func TestADryRunWithPendingFrameworkMigrationsStillVerifiesTheAppMigrations(t *testing.T) {
	// The situation of a later framework migration (0005, say) that is pending while the app tables exist.
	// None exists yet, so it is simulated: migration 0004 is made pending again. A dry run executes the
	// pending migration's SQL before rolling back, and 0004 would fail on tables that exist; so the real
	// tables are moved to the schema scratch, which comes after public in the search path: 0004 creates
	// its tables in public (and rolls them back), and the app migration queries still find the moved ones.
	d := dbtest.Migrated(t)
	registry := core.NewRegistry()
	applied := db.SyncOptions{LockTimeout: 5 * time.Second}
	if _, err := db.MigrateApps(bg, d.Pool, registry, verifyApp(one), applied); err != nil {
		t.Fatalf("apply the first app migration: %v", err)
	}
	d.Exec("CREATE SCHEMA scratch; " +
		"ALTER TABLE fb_app SET SCHEMA scratch; " +
		"ALTER TABLE fb_app_migration SET SCHEMA scratch; " +
		"DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET search_path = public, scratch', current_database()); END $$; " +
		"DELETE FROM fb_migration WHERE version = 4")
	// New connections have the search path; the pool of d may still hold old ones.
	pool, err := db.Connect(bg, d.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	dry := db.SyncOptions{DryRun: true, LockTimeout: 5 * time.Second}

	// An applied migration whose SQL changed is found, though framework migrations are pending.
	_, err = db.MigrateApps(bg, pool, registry, verifyApp(oneEdited), dry)
	if e := asDbError(t, err); e.Kind != db.ErrAppChecksumMismatch || e.Version != 1 {
		t.Errorf("%v", err)
	}

	// Only the migrations that are really pending are listed, not every declared one.
	report, err := db.MigrateApps(bg, pool, registry, verifyApp(one, two), dry)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Migrations) != 1 || report.Sync != nil {
		t.Errorf("the framework migration 0004 is pending and the sync is not planned: %+v", report)
	}
	if len(report.AppMigrations) != 1 || report.AppMigrations[0].Version != 2 || report.AppMigrations[0].Name != "two" {
		t.Errorf("%+v", report.AppMigrations)
	}
}

// Not in Rust: the recorded apps (Rust recorded_apps, tested through the site tests there).
func TestRecordedApps(t *testing.T) {
	d := dbtest.New(t)
	names, err := db.RecordedApps(bg, d.Pool)
	if err != nil || len(names) != 0 {
		t.Fatalf("%v %v", names, err)
	}
	apps := []db.AppSpec{{Name: "b_app", Version: "1"}, {Name: "A_app"}, {Name: "a_app"}}
	if _, err := db.MigrateApps(bg, d.Pool, core.NewRegistry(), apps, db.DefaultSyncOptions()); err != nil {
		t.Fatal(err)
	}
	names, err = db.RecordedApps(bg, d.Pool)
	if err != nil {
		t.Fatal(err)
	}
	// In byte order.
	want(t, names, []string{"A_app", "a_app", "b_app"}, "recorded apps")
	want(t, d.Rows("SELECT name, coalesce(version, 'NULL') FROM fb_app ORDER BY name COLLATE \"C\""),
		[][]string{{"A_app", "NULL"}, {"a_app", "NULL"}, {"b_app", "1"}}, "fb_app")
}
