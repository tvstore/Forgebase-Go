package db

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// DocType schema sync: introspect, plan, and apply additive changes in one transaction.

// SyncLockKey is the advisory lock key serializing schema syncs (transaction-level lock).
const SyncLockKey int64 = 0x4642_5359_4e43_0001

// DefaultLockTimeout is the lock_timeout of a sync whose options do not set one.
const DefaultLockTimeout = 5 * time.Second

// SyncOptions are the options of Sync.
type SyncOptions struct {
	// DryRun computes and returns the plan without keeping any change.
	DryRun bool
	// LockTimeout is the longest wait for any lock (including the sync advisory lock). Hitting it
	// fails the sync with ErrLockTimeout. Zero means DefaultLockTimeout (5 s; the Rust default,
	// where an explicit zero would be clamped to 1 ms, which nobody wants). Otherwise it is clamped
	// to 1 ms ..= 2^31-1 ms (about 24.8 days): PostgreSQL rejects larger values.
	LockTimeout time.Duration
}

// DefaultSyncOptions are the options of a sync that is not a dry run and waits at most
// DefaultLockTimeout for a lock (the Rust SyncOptions::default()).
func DefaultSyncOptions() SyncOptions {
	return SyncOptions{LockTimeout: DefaultLockTimeout}
}

// SyncReport is what a sync did (or, in a dry run, would do).
type SyncReport struct {
	// DryRun is true if this was a dry run: nothing was changed.
	DryRun bool
	// Steps are the steps applied, in order. Empty when the database already matches.
	Steps []Step
	// Warnings are the differences noticed but intentionally left alone.
	Warnings []Warning
}

// Sync brings the database in line with registry using additive changes only.
//
// It runs in one transaction: it sets a transaction-local lock_timeout, takes a transaction-level
// advisory lock, then introspects the connection's current schema (so concurrent syncs see each
// other's result), plans, and applies. Conflicts (ErrSyncConflicts) refuse the whole sync and
// nothing is applied. No destructive DDL is generated. Index creation is plain CREATE INDEX, which
// blocks writes on that table while it builds. Adding a foreign key takes a lock on both tables and
// validates the existing rows.
//
// A Link or Table field whose target DocType is not in registry, and the other reference rules of
// child tables (a child belongs to exactly one parent), are refused first (ErrInvalidMetadata, from
// Registry.CheckReferences), before anything touches the database. A child DocType's table gets
// parent_id and idx columns and a parent_id foreign key with ON DELETE CASCADE.
//
// The framework migrations must have been applied (ErrFrameworkNotMigrated).
func Sync(ctx context.Context, starter TxStarter, registry *core.Registry, options SyncOptions) (*SyncReport, error) {
	if problems := registry.CheckReferences(); len(problems) > 0 {
		return nil, &Error{Kind: ErrInvalidMetadata, Problems: problems}
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	timeout := options.LockTimeout
	if timeout == 0 {
		timeout = DefaultLockTimeout
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", lockTimeoutSetting(timeout)); err != nil {
		return nil, wrap(err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", SyncLockKey); err != nil {
		return nil, wrap(err)
	}

	// Qualify fb_doctype with the current schema, like the introspection below.
	var schema string
	if err := tx.QueryRow(ctx, "SELECT current_schema()::text").Scan(&schema); err != nil {
		return nil, wrap(err)
	}
	doctypeTable := QuoteIdent(schema) + ".fb_doctype"
	var registered bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", doctypeTable).Scan(&registered); err != nil {
		return nil, wrap(err)
	}
	if !registered {
		return nil, &Error{Kind: ErrFrameworkNotMigrated}
	}

	cat, err := loadCatalog(ctx, tx, registry, doctypeTable, schema)
	if err != nil {
		return nil, err
	}
	plan := planSchema(registry, cat)
	if len(plan.conflicts) > 0 {
		if err := tx.Rollback(ctx); err != nil {
			return nil, wrap(err)
		}
		return nil, &Error{Kind: ErrSyncConflicts, Conflicts: plan.conflicts}
	}
	if options.DryRun {
		err = tx.Rollback(ctx)
	} else {
		for _, step := range plan.steps {
			if err := applyStep(ctx, tx, step, doctypeTable); err != nil {
				return nil, err
			}
		}
		err = tx.Commit(ctx)
	}
	if err != nil {
		return nil, wrap(err)
	}
	return &SyncReport{DryRun: options.DryRun, Steps: plan.steps, Warnings: plan.warnings}, nil
}

func applyStep(ctx context.Context, tx pgx.Tx, step Step, doctypeTable string) error {
	if step.Kind == StepRegisterDocType {
		_, err := tx.Exec(ctx,
			"INSERT INTO "+doctypeTable+" (name, table_name) VALUES ($1, $2) "+
				"ON CONFLICT (name) DO UPDATE SET table_name = excluded.table_name, synced_at = now()",
			step.DocType, core.TableName(step.DocType))
		return wrap(err)
	}
	// No arguments: the simple query protocol.
	_, err := tx.Exec(ctx, step.SQL)
	return wrap(err)
}

// loadCatalog reads the live schema (current schema only) for the tables of the registry. Rows are
// scanned, so unexpected types in the database give an error, never a panic.
func loadCatalog(ctx context.Context, tx pgx.Tx, registry *core.Registry, doctypeTable, schema string) (*catalog, error) {
	tables := []string{}
	for _, d := range registry.Iter() {
		tables = append(tables, core.TableName(d.Name))
	}
	cat := newCatalog()

	rows, err := tx.Query(ctx,
		"SELECT table_name::text, column_name::text, data_type::text, "+
			"character_maximum_length::int4, numeric_precision::int4, "+
			"numeric_scale::int4, is_nullable::text, datetime_precision::int4 "+
			"FROM information_schema.columns "+
			"WHERE table_schema = current_schema() AND table_name = ANY($1::text[])", tables)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var table, name, dataType, nullable string
		var length, precision, scale, datetimePrecision *int32
		if err := rows.Scan(&table, &name, &dataType, &length, &precision, &scale, &nullable, &datetimePrecision); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		switch {
		case dataType == "character varying" && length != nil:
			dataType = "character varying(" + strconv.Itoa(int(*length)) + ")"
		case dataType == "numeric" && precision != nil && scale != nil:
			dataType = "numeric(" + strconv.Itoa(int(*precision)) + "," + strconv.Itoa(int(*scale)) + ")"
		// 6 is the default; anything else (for example timestamptz(0)) is drift.
		case dataType == "timestamp with time zone" && datetimePrecision != nil && *datetimePrecision != 6:
			dataType = "timestamp(" + strconv.Itoa(int(*datetimePrecision)) + ") with time zone"
		}
		if cat.columns[table] == nil {
			cat.columns[table] = map[string]column{}
		}
		cat.columns[table][name] = column{dataType: dataType, notNull: nullable == "NO"}
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	rows, err = tx.Query(ctx,
		"SELECT c.relname::text, a.attname::text, i.indisprimary, i.indisunique "+
			"FROM pg_index i "+
			"JOIN pg_class c ON c.oid = i.indrelid "+
			"JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = i.indkey[0] "+
			"WHERE c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema()) "+
			"  AND c.relname = ANY($1::text[]) "+
			"  AND i.indisvalid AND i.indnatts = 1 AND i.indpred IS NULL", tables)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var table, name string
		var primary, unique bool
		if err := rows.Scan(&table, &name, &primary, &unique); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		key := colKey{table, name}
		cat.indexed[key] = true
		if primary {
			cat.primary[key] = true
		} else if unique {
			cat.unique[key] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	// Single-column foreign keys. confdeltype is of the type "char"; it is cast for reading.
	rows, err = tx.Query(ctx,
		"SELECT c.relname::text, a.attname::text, rn.nspname::text, rt.relname::text, "+
			"       ra.attname::text, con.confdeltype::text, con.convalidated "+
			"FROM pg_constraint con "+
			"JOIN pg_class c ON c.oid = con.conrelid "+
			"JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = con.conkey[1] "+
			"JOIN pg_class rt ON rt.oid = con.confrelid "+
			"JOIN pg_namespace rn ON rn.oid = rt.relnamespace "+
			"JOIN pg_attribute ra ON ra.attrelid = rt.oid AND ra.attnum = con.confkey[1] "+
			"WHERE con.contype = 'f' AND array_length(con.conkey, 1) = 1 "+
			"  AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema()) "+
			"  AND c.relname = ANY($1::text[])", tables)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var table, name, refSchema, refTable, refColumn, rule string
		var validated bool
		if err := rows.Scan(&table, &name, &refSchema, &refTable, &refColumn, &rule, &validated); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		referenced := refTable
		if refSchema != schema {
			referenced = refSchema + "." + refTable
		}
		onDelete := byte('?')
		if rule != "" {
			onDelete = rule[0]
		}
		key := colKey{table, name}
		cat.foreignKeys[key] = append(cat.foreignKeys[key], foreignKey{
			table: referenced, column: refColumn, onDelete: onDelete, validated: validated,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	rows, err = tx.Query(ctx,
		"SELECT relname::text, relkind::text FROM pg_class "+
			"WHERE relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema()) AND relname = ANY($1::text[])", tables)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		if kind == "r" || kind == "p" {
			cat.tables[name] = true
		}
		cat.relations[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	// Standalone types (domain, enum, ...) share the namespace of table row types. Row types of
	// tables (typrelid <> 0) are covered by the pg_class query above.
	rows, err = tx.Query(ctx,
		"SELECT typname::text FROM pg_type "+
			"WHERE typnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema()) AND typname = ANY($1::text[]) "+
			"  AND typrelid = 0", tables)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		cat.relations[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}

	rows, err = tx.Query(ctx, "SELECT name::text FROM "+doctypeTable)
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		cat.managed[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return cat, nil
}

// lockTimeoutSetting is lock_timeout as the PostgreSQL setting text ("5000ms"), at least 1 ms and at
// most the largest value PostgreSQL accepts. Bound as a parameter of set_config, never formatted
// into SQL.
func lockTimeoutSetting(timeout time.Duration) string {
	ms := min(max(timeout.Milliseconds(), 1), math.MaxInt32)
	return strconv.FormatInt(ms, 10) + "ms"
}
