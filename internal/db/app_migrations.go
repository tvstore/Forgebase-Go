package db

import (
	"context"
	"sort"
	"time"
)

// App data migrations: versioned SQL that an app declares, applied once per database (the Rust
// module app_migrations.rs). The tables fb_app and fb_app_migration come from framework migration
// 0004. App code is trusted and compiled in (ADR 0006, ADR 0012): the SQL runs with the same
// database role as everything else.

// AppMigration is one migration an app declares.
type AppMigration struct {
	// Version is positive and strictly increasing within the app.
	Version int32
	// Name is a short name, recorded in fb_app_migration.
	Name string
	// SQL runs inside the migration's transaction, so it must not contain BEGIN or COMMIT. Never edit
	// an applied migration: its checksum is verified.
	SQL string
}

// AppSpec is an app as far as the database layer needs to know it.
type AppSpec struct {
	// Name is the app name.
	Name string
	// Version is the version the app declares, recorded in fb_app; "" is recorded as NULL.
	Version string
	// Migrations are in the order they run (strictly increasing versions).
	Migrations []AppMigration
}

// pendingAppMigration is a declared migration that is not applied yet.
type pendingAppMigration struct {
	app       string
	migration AppMigration
}

// appMigrationTablesExist reports whether the app migration tables exist (framework migration 0004
// has been applied). A dry run asks this while framework migrations are pending. Rust:
// app_migration_tables_exist.
func appMigrationTablesExist(ctx context.Context, q querier) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('fb_app_migration') IS NOT NULL").Scan(&exists); err != nil {
		return false, wrap(err)
	}
	return exists, nil
}

type appliedKey struct {
	app     string
	version int32
}

type appliedRow struct{ name, checksum string }

// pendingAppMigrations are the migrations of apps that are not applied yet, in app order and then
// version order. Every applied migration of these apps is checked first: it must still be declared
// (ErrUnknownAppMigration) and its checksum must match (ErrAppChecksumMismatch), and no declared
// migration that is not applied may have a lower version than an applied one
// (ErrOutOfOrderAppMigration). Apps that are not in apps are not looked at.
func pendingAppMigrations(ctx context.Context, q Queryer, apps []AppSpec) ([]pendingAppMigration, error) {
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = a.Name
	}
	rows, err := q.Query(ctx, "SELECT app, version, name, checksum FROM fb_app_migration WHERE app = ANY($1)", names)
	if err != nil {
		return nil, wrap(err)
	}
	applied := map[appliedKey]appliedRow{}
	for rows.Next() {
		var (
			app, name, sum string
			version        int32
		)
		if err := rows.Scan(&app, &version, &name, &sum); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		applied[appliedKey{app, version}] = appliedRow{name, sum}
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	var pending []pendingAppMigration
	for _, app := range apps {
		// The applied versions of this app, ascending, so the first unknown one is reported (the Rust
		// map is ordered by (app, version)).
		var versions []int32
		for key := range applied {
			if key.app == app.Name {
				versions = append(versions, key.version)
			}
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
		var highest int32
		for _, version := range versions {
			declared := false
			for _, m := range app.Migrations {
				if m.Version == version {
					declared = true
				}
			}
			if !declared {
				return nil, &Error{Kind: ErrUnknownAppMigration, App: app.Name, Version: version, Name: applied[appliedKey{app.Name, version}].name}
			}
			highest = max(highest, version)
		}
		for _, m := range app.Migrations {
			row, done := applied[appliedKey{app.Name, m.Version}]
			if !done {
				if len(versions) > 0 && highest > m.Version {
					return nil, &Error{Kind: ErrOutOfOrderAppMigration, App: app.Name, Version: m.Version, Name: m.Name, AppliedVersion: highest}
				}
				pending = append(pending, pendingAppMigration{app.Name, m})
				continue
			}
			expected, err := checksum(ctx, q, m.SQL)
			if err != nil {
				return nil, err
			}
			if expected != row.checksum {
				return nil, &Error{Kind: ErrAppChecksumMismatch, App: app.Name, Version: m.Version, Name: row.name, Expected: expected, Actual: row.checksum}
			}
		}
	}
	return pending, nil
}

// applyAppMigration applies one migration in its own transaction together with its
// fb_app_migration row: the SQL and the record commit or roll back together. A failing SQL is
// ErrAppMigrationFailed. The transaction has a lock_timeout of lockTimeout (set_config with
// is_local, so it ends with the transaction): a migration that waits longer for a lock fails and is
// rolled back.
func applyAppMigration(ctx context.Context, starter TxStarter, app string, m AppMigration, lockTimeout time.Duration) error {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	// A no-op after the commit; on every early return it rolls the transaction back.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", lockTimeoutSetting(lockTimeout)); err != nil {
		return wrap(err)
	}
	// No arguments: the simple query protocol, which runs a script of several statements.
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return &Error{Kind: ErrAppMigrationFailed, App: app, Version: m.Version, Name: m.Name, Message: wrap(err).Error(), Err: err}
	}
	sum, err := checksum(ctx, tx, m.SQL)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO fb_app_migration (app, version, name, checksum) VALUES ($1, $2, $3, $4)",
		app, m.Version, m.Name, sum); err != nil {
		return wrap(err)
	}
	return wrap(tx.Commit(ctx))
}

// recordApp records that app was present at this migrate, with the version it declares. Apps that
// are not in the binary are left as they are.
func recordApp(ctx context.Context, q Queryer, app AppSpec) error {
	var version any
	if app.Version != "" {
		version = app.Version
	}
	_, err := q.Exec(ctx,
		"INSERT INTO fb_app (name, version) VALUES ($1, $2) "+
			"ON CONFLICT (name) DO UPDATE SET version = excluded.version, migrated_at = now()",
		app.Name, version)
	return wrap(err)
}

// RecordedApps are the names recorded in fb_app: the apps whose migrations ran on this database, in
// byte order. Empty while the table does not exist (the database is not migrated yet).
func RecordedApps(ctx context.Context, q Queryer) ([]string, error) {
	exists, err := appMigrationTablesExist(ctx, q)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT name FROM fb_app ORDER BY name COLLATE "C"`)
	if err != nil {
		return nil, wrap(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		names = append(names, name)
	}
	return names, wrap(rows.Err())
}
