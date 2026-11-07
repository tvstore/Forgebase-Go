package db

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The whole of `forgebase migrate`: the framework migrations, the schema sync and the data
// migrations of the apps, under one session-level lock (the Rust functions migrate, migrate_apps and
// migrate_all of forgebase-db).
//
// The workflow backfill (P6) runs after the sync, then the schedules (P5, synced by MigrateAll), and last the
// fixtures of the apps (P7.2, ADR 0022). A stored custom field of the site keeps its column through the sync (P7.3, ADR 0023).

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
	// AppMigrations are the app data migrations, in the order they run.
	AppMigrations []AppliedAppMigration
	// Schedules are the changes made to the schedules (add, change, remove; see SyncSchedules), one
	// line each; with a dry run, the planned ones. Empty when the schedules were not given (MigrateApps)
	// or, in a dry run, while framework migrations are pending.
	Schedules []string
	// WorkflowWarnings are documents in a workflow state the workflow no longer defines (see backfillWorkflowStates).
	WorkflowWarnings []string
	// Fixtures are the fixtures of the apps that have some, in app order; in a dry run, what they would do.
	Fixtures []AppFixtures
	// FixturesPlanned is false while framework migrations are pending in a dry run: the fixtures
	// are then only counted.
	FixturesPlanned bool
	// CustomFieldWarnings are the stored custom fields that were skipped because they no longer validate (one
	// message each; they are also written to SyncOptions.Warn as warning: lines before the sync, as Rust does).
	CustomFieldWarnings []string
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
	return MigrateApps(ctx, pool, registry, nil, options)
}

// MigrateApps applies the framework migrations, syncs the DocType schema of registry and then
// applies the data migrations of apps, in the order of apps and, per app, by version (the Rust
// migrate_apps).
//
// Each app migration is its own transaction together with its fb_app_migration row, so a failing
// one is rolled back (ErrAppMigrationFailed) and stops the run; the ones before it stay applied.
// Each runs with lock_timeout set to options.LockTimeout (set_config with is_local). Before the
// schema sync every applied migration of apps is verified against the SQL the apps declare now
// (ErrAppChecksumMismatch, ErrUnknownAppMigration), and a declared migration that is not applied
// must not have a lower version than an applied one of the same app (ErrOutOfOrderAppMigration), so
// a changed or out-of-order migration is refused before anything but the framework migrations is
// applied. After an app's migrations, fb_app is upserted with the app's name and version (apps that
// are not in apps are left as they are).
//
// With DryRun the report lists the pending framework and app migrations and the sync plan. While
// framework migrations are pending the sync plan is nil; the app migrations are still verified and
// listed when fb_app_migration exists, and before framework migration 0004 creates it every
// declared app migration is listed as pending (none can have been applied).
//
// The rest is as for Migrate.
func MigrateApps(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, apps []AppSpec, options SyncOptions) (*MigrateReport, error) {
	return MigrateAll(ctx, pool, registry, apps, nil, options)
}

// MigrateAll is MigrateApps and then, in the same run and under the same lock, the sync of the schedules
// the binary declares (SyncSchedules) when schedules is not nil: schedules the binary does not declare are
// deleted, so the binary that migrates is the source of truth. A non-nil empty slice therefore deletes
// them all; nil leaves the schedule table alone (Rust: Option<&[Declared]>).
func MigrateAll(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, apps []AppSpec, schedules []DeclaredSchedule, options SyncOptions) (*MigrateReport, error) {
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
	report, err := migrateLocked(ctx, conn, registry, apps, schedules, options)
	// Released explicitly as well, so that a caller that keeps going does not wait for the close.
	_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", MigrateLockKey)
	return report, err
}

func migrateLocked(ctx context.Context, conn *pgx.Conn, registry *core.Registry, apps []AppSpec, schedules []DeclaredSchedule, options SyncOptions) (*MigrateReport, error) {
	migrations, err := MigrateFramework(ctx, conn, options.DryRun)
	if err != nil {
		return nil, err
	}
	if options.DryRun && len(migrations) > 0 {
		// Framework migrations are pending, so the sync plan cannot be computed. The app migrations are
		// still verified when their tables already exist (a later framework migration is pending);
		// when they do not, no app migration can have been applied.
		var listed []AppliedAppMigration
		exists, err := appMigrationTablesExist(ctx, conn)
		if err != nil {
			return nil, err
		}
		if exists {
			pending, err := pendingAppMigrations(ctx, conn, apps)
			if err != nil {
				return nil, err
			}
			for _, p := range pending {
				listed = append(listed, applied(p.app, p.migration))
			}
		} else {
			for _, app := range apps {
				for _, m := range app.Migrations {
					listed = append(listed, applied(app.Name, m))
				}
			}
		}
		var fixtures []AppFixtures
		for _, app := range apps {
			if len(app.Fixtures) > 0 {
				fixtures = append(fixtures, AppFixtures{App: app.Name, Records: len(app.Fixtures)})
			}
		}
		return &MigrateReport{Migrations: migrations, AppMigrations: listed, Fixtures: fixtures}, nil
	}
	pending, err := pendingAppMigrations(ctx, conn, apps)
	if err != nil {
		return nil, err
	}
	// Every stored custom field, enabled or disabled, keeps its column: the sync works from the registry of the
	// apps plus those fields (M10.1). Fields that no longer validate are skipped.
	stored, err := schemaRegistry(ctx, conn, registry)
	if err != nil {
		return nil, err
	}
	warn := options.Warn
	if warn == nil {
		warn = os.Stderr
	}
	for _, warning := range stored.Warnings {
		_, _ = fmt.Fprintf(warn, "warning: %s\n", warning)
	}
	sync, err := Sync(ctx, conn, stored.Registry, options)
	if err != nil {
		return nil, err
	}
	var done []AppliedAppMigration
	if options.DryRun {
		for _, p := range pending {
			done = append(done, applied(p.app, p.migration))
		}
	} else {
		for _, app := range apps {
			for _, p := range pending {
				if p.app != app.Name {
					continue
				}
				if err := applyAppMigration(ctx, conn, app.Name, p.migration, options.LockTimeout); err != nil {
					return nil, err
				}
				done = append(done, applied(app.Name, p.migration))
			}
			if err := recordApp(ctx, conn, app); err != nil {
				return nil, err
			}
		}
	}
	var lines []string
	if schedules != nil {
		if lines, err = SyncSchedules(ctx, conn, schedules, options.DryRun); err != nil {
			return nil, err
		}
	}
	// After the sync, so the workflow_state columns exist; a dry run keeps nothing, so there is nothing
	// to backfill.
	var warnings []string
	if !options.DryRun {
		if warnings, err = backfillWorkflowStates(ctx, conn, registry); err != nil {
			return nil, err
		}
	}
	// Last, after the app migrations (which may create what a record links to) and the backfill: one transaction
	// per app, in app order. A failure leaves everything above applied.
	var fixtures []AppFixtures
	var failure error
	for _, app := range apps {
		if len(app.Fixtures) == 0 {
			continue
		}
		var one *AppFixtures
		if options.DryRun {
			if one, err = planFixtures(ctx, conn, app); err != nil {
				return nil, err
			}
			fixtures = append(fixtures, *one)
			continue
		}
		one, err = applyFixtures(ctx, conn, registry, app, options.LockTimeout)
		var dbErr *Error
		if errors.As(err, &dbErr) && dbErr.Kind == ErrFixtureFailed {
			failure = err
			break
		}
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, *one)
	}
	return &MigrateReport{Migrations: migrations, Sync: sync, AppMigrations: done, Schedules: lines,
		WorkflowWarnings: warnings, Fixtures: fixtures, FixturesPlanned: true, FixturesFailure: failure,
		CustomFieldWarnings: stored.Warnings}, nil
}

func applied(app string, m AppMigration) AppliedAppMigration {
	return AppliedAppMigration{App: app, Version: m.Version, Name: m.Name}
}
