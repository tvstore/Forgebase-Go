package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The whole of `forgebase migrate` for the stock binary: the framework migrations, then the schema
// sync, under one session-level lock (the Rust functions migrate and migrate_all of forgebase-db).
//
// The app data migrations, the schedules, the workflow backfill and the fixtures of apps arrive with
// the app model (P4) and the job queue (P5): the report already has their places (and the app's text
// report prints them), so adding them does not change this signature.

// AppliedAppMigration is one app data migration that was applied (or, in a dry run, is pending).
type AppliedAppMigration struct {
	App     string
	Version int32
	Name    string
}

// AppFixtures is what the fixtures of one app did.
type AppFixtures struct {
	App                                string
	Records                            int
	Inserted, Updated, Kept, Unchanged int
}

// MigrateReport is what Migrate did (or, in a dry run, would do).
type MigrateReport struct {
	// Migrations are the framework migrations applied (pending, in a dry run).
	Migrations []Migration
	// Sync is the schema sync; nil while framework migrations are pending in a dry run, because
	// the plan needs the tables they create.
	Sync *SyncReport
	// AppMigrations are the app data migrations, in the order they run (P4).
	AppMigrations []AppliedAppMigration
	// Schedules are the changes made to the schedules, one line each (P5).
	Schedules []string
	// WorkflowWarnings are documents in a workflow state the workflow no longer defines (P6).
	WorkflowWarnings []string
	// Fixtures are the fixtures of the apps that have some (P4).
	Fixtures []AppFixtures
	// FixturesPlanned is false while framework migrations are pending in a dry run: the fixtures
	// are then only counted.
	FixturesPlanned bool
	// FixturesFailure is a refused fixture: the report says what was committed, and the caller
	// reports this error after it.
	FixturesFailure error
}

// Migrate applies the framework migrations, then syncs the DocType schema of registry.
//
// The advisory lock of the framework migrations is held for the whole call (a session-level lock on
// one connection that is taken out of the pool and closed at the end), so two migrate runs never
// interleave. The steps still use separate transactions: the framework migrations stay applied if the
// sync fails. With DryRun nothing is kept: the report lists the pending framework migrations and the
// sync plan; while framework migrations are pending the plan is nil.
func Migrate(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, options SyncOptions) (*MigrateReport, error) {
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return nil, &Error{Kind: ErrPool, Message: err.Error(), Err: err}
	}
	// The connection holds a session-level lock, so it must never go back to the pool alive:
	// Hijack takes it out, and closing it ends the session, which releases the lock.
	conn := acquired.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	if _, err := conn.Exec(ctx, "SELECT set_config('lock_timeout', $1, false)", lockTimeoutSetting(options.LockTimeout)); err != nil {
		return nil, wrap(err)
	}
	_, lockErr := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", MigrateLockKey)
	// Back to the connection's default, so the migrations that follow have no timeout of their own.
	// This runs before the lock result is looked at, but must not hide it: when both fail, the lock
	// error (the cause) is the one reported.
	_, resetErr := conn.Exec(ctx, "RESET lock_timeout")
	if lockErr != nil {
		return nil, wrap(lockErr)
	}
	if resetErr != nil {
		return nil, wrap(resetErr)
	}
	report, err := migrateLocked(ctx, conn, registry, options)
	// Released explicitly as well, so that a caller that keeps going does not wait for the close.
	_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", MigrateLockKey)
	return report, err
}

func migrateLocked(ctx context.Context, starter TxStarter, registry *core.Registry, options SyncOptions) (*MigrateReport, error) {
	migrations, err := MigrateFramework(ctx, starter, options.DryRun)
	if err != nil {
		return nil, err
	}
	if options.DryRun && len(migrations) > 0 {
		// Framework migrations are pending, so the sync plan cannot be computed.
		return &MigrateReport{Migrations: migrations}, nil
	}
	sync, err := Sync(ctx, starter, registry, options)
	if err != nil {
		return nil, err
	}
	return &MigrateReport{Migrations: migrations, Sync: sync, FixturesPlanned: true}, nil
}
