package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Fixtures: records an app ships, applied by migrate through the document pipeline (M9.4, ADR 0022; the Rust
// module fixtures.rs).
//
// The table (fb_fixture) comes from framework migration 0011. A fixture record is identified by its DocType
// and name (the DocType uses field naming); fb_fixture remembers the checksum of the version of the record that
// was last applied, so that migrate can tell "the app shipped a new version" from "nothing changed" and leave
// the site's own edits alone until then.
//
//	fb_fixture row   document   action
//	none             missing    insert, record the checksum
//	none             exists     keep it as it is, record the checksum (adopted)
//	same checksum    any        nothing: no read of the document, no write
//	other checksum   exists     update the fields the record has, record the checksum
//	other checksum   missing    insert again, record the checksum
//
// Nothing is ever deleted. The checksum itself is computed by the caller (internal/app), which also parses the
// JSON: this package only gets parsed documents, like Import.

// FixtureRecord is one fixture record, already parsed and checked by the caller.
type FixtureRecord struct {
	// DocType is the DocType (field naming, no workflow, not a child).
	DocType string
	// File is the path of the fixture file, for messages.
	File string
	// Index is the position of the record in the file, counting from 1, for messages.
	Index int
	// Name is the document name: the value of the naming field.
	Name string
	// Checksum is the hex SHA-256 of the canonical JSON of the record.
	Checksum string
	// Document is the record as a document: every field it gives, Table fields as rows.
	Document core.Document
}

// fixtureTableExists reports whether the fixture table exists (framework migration 0011 has been applied).
func fixtureTableExists(ctx context.Context, q Queryer) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('fb_fixture') IS NOT NULL").Scan(&exists); err != nil {
		return false, wrap(err)
	}
	return exists, nil
}

// fixtureActor is the actor recorded as owner and modified_by of what the fixtures of app write.
func fixtureActor(app string) string { return "fixture:" + app }

func describeRecord(r FixtureRecord) string { return fmt.Sprintf("record %d (`%s`)", r.Index, r.Name) }

const nothingKept = " (nothing from this app's fixtures was kept)"

// applyFixtures applies the fixtures of one app in one transaction: any error rolls all of them back
// (ErrFixtureFailed). The transaction has a lock_timeout like an app migration.
func applyFixtures(ctx context.Context, starter TxStarter, registry *core.Registry, app AppSpec, lockTimeout time.Duration) (*AppFixtures, error) {
	report := &AppFixtures{App: app.Name, Records: len(app.Fixtures)}
	caller := core.SystemCaller(fixtureActor(app.Name))
	setupFailed := func(err error) error {
		return &Error{Kind: ErrFixtureFailed, App: app.Name, File: "(all fixture files)", Record: "transaction",
			Message: err.Error() + nothingKept}
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, setupFailed(wrap(err))
	}
	// A no-op after the commit; on every early return it rolls the transaction back.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", lockTimeoutSetting(lockTimeout)); err != nil {
		return nil, setupFailed(wrap(err))
	}
	for _, record := range app.Fixtures {
		failed := func(message string) error {
			return &Error{Kind: ErrFixtureFailed, App: app.Name, File: record.File, Record: describeRecord(record), Message: message}
		}
		fail := func(err error) error { return failed(wrap(err).Error() + nothingKept) }
		doctype := registry.Get(record.DocType)
		if doctype == nil {
			return nil, failed(fmt.Sprintf("unknown DocType `%s`%s", record.DocType, nothingKept))
		}
		var previous *string
		var sum string
		switch err := tx.QueryRow(ctx, "SELECT checksum FROM fb_fixture WHERE doctype = $1 AND name = $2",
			record.DocType, record.Name).Scan(&sum); {
		case err == nil:
			previous = &sum
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fail(err)
		}
		if previous != nil && *previous == record.Checksum {
			report.Unchanged++
			continue
		}
		stored, err := Get(ctx, tx, registry, record.DocType, record.Name, caller)
		if err != nil {
			return nil, fail(err)
		}
		switch {
		case previous == nil && stored != nil:
			report.Kept++
		case previous != nil && stored != nil:
			changes := core.Changes{Values: cloneValues(record.Document.Values), Tables: cloneTables(record.Document.Tables)}
			// The naming field is the identity of the record and cannot be changed.
			if doctype.Naming.Kind == core.NamingField {
				delete(changes.Values, doctype.Naming.Field)
			}
			if _, err := Update(ctx, tx, registry, record.DocType, record.Name, stored.Version, changes, caller); err != nil {
				return nil, fail(err)
			}
			report.Updated++
		default:
			inserted, err := Insert(ctx, tx, registry, record.DocType, record.Document, caller)
			if err != nil {
				return nil, fail(err)
			}
			// A hook may change the naming field; the record would then be tracked under a name its document
			// does not have, and inserted again at every changed version.
			if inserted.Name != record.Name {
				return nil, failed(fmt.Sprintf("a hook changed the naming field of fixture record `%s` to `%s`; fixtures need a stable name%s",
					record.Name, inserted.Name, nothingKept))
			}
			report.Inserted++
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO fb_fixture (doctype, name, app, checksum) VALUES ($1, $2, $3, $4) "+
				"ON CONFLICT (doctype, name) DO UPDATE SET app = excluded.app, checksum = excluded.checksum, applied_at = now()",
			record.DocType, record.Name, app.Name, record.Checksum); err != nil {
			return nil, fail(err)
		}
	}
	// A database error here is still the fixtures' failure: migrate then keeps and prints the report of what was
	// committed before.
	if err := tx.Commit(ctx); err != nil {
		return nil, &Error{Kind: ErrFixtureFailed, App: app.Name, File: "(all fixture files)", Record: "commit",
			Message: wrap(err).Error() + " (the outcome of the commit is unknown: the fixtures of this app may have been kept; running migrate again is safe)"}
	}
	return report, nil
}

// planFixtures is what applyFixtures would do, read-only (for a dry run). The framework migration of the table
// may be pending and the DocType tables may not exist yet (the schema sync of a dry run is rolled back): a
// missing table or row counts as "no document", so the plan is exact for a database that is up to date and a
// best statement for one that is not. Nothing is validated.
func planFixtures(ctx context.Context, q Queryer, app AppSpec) (*AppFixtures, error) {
	report := &AppFixtures{App: app.Name, Records: len(app.Fixtures)}
	tracked, err := fixtureTableExists(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, record := range app.Fixtures {
		var previous *string
		if tracked {
			var sum string
			switch err := q.QueryRow(ctx, "SELECT checksum FROM fb_fixture WHERE doctype = $1 AND name = $2",
				record.DocType, record.Name).Scan(&sum); {
			case err == nil:
				previous = &sum
			case !errors.Is(err, pgx.ErrNoRows):
				return nil, wrap(err)
			}
		}
		if previous != nil && *previous == record.Checksum {
			report.Unchanged++
			continue
		}
		table := QuoteIdent(core.TableName(record.DocType))
		var hasTable bool
		if err := q.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&hasTable); err != nil {
			return nil, wrap(err)
		}
		exists := false
		if hasTable {
			var one int32
			switch err := q.QueryRow(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE \"name\" = $1::text", table), record.Name).Scan(&one); {
			case err == nil:
				exists = true
			case !errors.Is(err, pgx.ErrNoRows):
				return nil, wrap(err)
			}
		}
		switch {
		case previous == nil && exists:
			report.Kept++
		case previous != nil && exists:
			report.Updated++
		default:
			report.Inserted++
		}
	}
	return report, nil
}
