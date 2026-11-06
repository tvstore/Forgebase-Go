package db

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Framework migrations: Forgebase's own tables, as embedded ordered SQL. The files in migrations/
// are byte-identical to the ones of the Rust implementation: a database migrated by one
// implementation is served by the other, so the SQL and its checksums never change (add a new
// numbered file instead of editing an applied one).

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrateLockKey is the advisory lock key serializing migrations: taken per transaction by
// MigrateFramework and, in later phases, for the whole of the app migrations (session-level lock).
const MigrateLockKey int64 = 0x4642_4d49_4752_0001

// bootstrapSQL creates the migration table before the migrations run so applied ones can be read.
const bootstrapSQL = "CREATE TABLE IF NOT EXISTS fb_migration (" +
	"version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, " +
	"applied_at timestamptz NOT NULL DEFAULT now())"

// Migration is one embedded framework migration.
type Migration struct {
	// Version is the strictly increasing version number.
	Version int32
	// Name is the short name.
	Name string
	// SQL runs inside the migration transaction, so it must not contain BEGIN or COMMIT. Never edit
	// an applied migration: its checksum is verified.
	SQL string
}

// Migrations are all framework migrations, in order.
var Migrations = loadMigrations([]struct {
	version int32
	name    string
}{
	{1, "init"},
	{2, "series"},
	{3, "auth"},
	{4, "apps"},
	{5, "jobs"},
	{6, "schedules"},
	{7, "finished_jobs_index"},
	{8, "workflow_log"},
	{9, "saved_views"},
	{10, "webhooks"},
	{11, "fixtures"},
	{12, "custom_fields"},
	{13, "user_permissions"},
})

func loadMigrations(table []struct {
	version int32
	name    string
}) []Migration {
	out := make([]Migration, len(table))
	for i, m := range table {
		file := fmt.Sprintf("migrations/%04d_%s.sql", m.version, m.name)
		sql, err := migrationFiles.ReadFile(file)
		if err != nil {
			panic("framework migration file missing from the build: " + file) // a build defect, caught by every test run
		}
		out[i] = Migration{Version: m.version, Name: m.name, SQL: string(sql)}
	}
	return out
}

// MigrateFramework applies the pending framework migrations and returns them.
//
// Everything runs in one transaction guarded by a transaction-level advisory lock, so concurrent
// callers queue and a failure leaves nothing behind. Before applying, every applied migration is
// checked: it must exist in this build (ErrUnknownMigration) and its SQL checksum (SHA-256,
// computed by PostgreSQL) must match (ErrChecksumMismatch). With dryRun nothing is kept and the
// pending migrations are returned. A dry run still executes the migration SQL (and takes its
// locks) before rolling back, so a slow or lock-heavy migration is just as slow in a dry run.
func MigrateFramework(ctx context.Context, starter TxStarter, dryRun bool) ([]Migration, error) {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	// After a successful Commit or Rollback this is a no-op; on every early return it undoes the
	// transaction.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", MigrateLockKey); err != nil {
		return nil, wrap(err)
	}
	if _, err := tx.Exec(ctx, bootstrapSQL); err != nil {
		return nil, wrap(err)
	}

	rows, err := tx.Query(ctx, "SELECT version, name, checksum FROM fb_migration")
	if err != nil {
		return nil, wrap(err)
	}
	type appliedRow struct {
		version      int32
		name, actual string
	}
	var found []appliedRow
	for rows.Next() {
		var r appliedRow
		// Scan: a pre-existing fb_migration with other column types is an error, not a panic.
		if err := rows.Scan(&r.version, &r.name, &r.actual); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	applied := map[int32]bool{}
	for _, r := range found {
		migration := findMigration(r.version)
		if migration == nil {
			return nil, &Error{Kind: ErrUnknownMigration, Version: r.version, Name: r.name}
		}
		expected, err := checksum(ctx, tx, migration.SQL)
		if err != nil {
			return nil, err
		}
		if expected != r.actual {
			return nil, &Error{Kind: ErrChecksumMismatch, Version: r.version, Name: r.name, Expected: expected, Actual: r.actual}
		}
		applied[r.version] = true
	}

	var pending []Migration
	for _, m := range Migrations {
		if !applied[m.Version] {
			pending = append(pending, m)
		}
	}
	for _, m := range pending {
		// No arguments: the simple query protocol, which runs a script of several statements.
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return nil, wrap(err)
		}
		sum, err := checksum(ctx, tx, m.SQL)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO fb_migration (version, name, checksum) VALUES ($1, $2, $3)",
			m.Version, m.Name, sum); err != nil {
			return nil, wrap(err)
		}
	}
	if dryRun {
		err = tx.Rollback(ctx)
	} else {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return nil, wrap(err)
	}
	return pending, nil
}

func findMigration(version int32) *Migration {
	for i := range Migrations {
		if Migrations[i].Version == version {
			return &Migrations[i]
		}
	}
	return nil
}

// checksum is the SHA-256 of sql as lower-case hex, computed by PostgreSQL over the UTF-8 bytes of
// the text, exactly as the Rust implementation does.
func checksum(ctx context.Context, q querier, sql string) (string, error) {
	var sum string
	err := q.QueryRow(ctx, "SELECT encode(sha256(convert_to($1::text, 'UTF8')), 'hex')", sql).Scan(&sum)
	if err != nil {
		return "", wrap(err)
	}
	return sum, nil
}

// querier is what checksum needs of a connection, a pool or a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxStarter is anything that can start a transaction: a *pgxpool.Pool, a *pgxpool.Conn, a *pgx.Conn,
// or a pgx.Tx. It is what the Rust functions take as a client.
//
// With a pgx.Tx, Begin starts a savepoint inside the caller's transaction: the transaction-local
// settings (set_config(..., true), as the sync's lock_timeout) and the transaction-level advisory
// locks (pg_advisory_xact_lock, as the migration and sync locks) then last until the caller's outer
// transaction ends, not until the function returns, and a commit of the function only releases
// the savepoint.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}
