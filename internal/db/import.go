package db

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Import: many documents of one DocType in one transaction, each row inside a savepoint (M9.3, ADR 0021;
// the Rust module import.rs).
//
// Every row goes through the normal pipeline as the caller: Insert (or, for an existing document in
// ImportUpsert, Get and Update), so the hooks, validation, naming, links, workflow rules, the per-row
// rights, the outbox jobs and the webhooks all apply. A row that fails is rolled back to its savepoint and
// its error is collected, so one bad row does not hide the others. When commit is true and no row failed,
// the transaction commits; otherwise (a dry run, or any error) it is rolled back, and with it every
// document, job and webhook delivery the rows wrote.
//
// Rights. The caller needs import and create, and write for an upsert. The rows check their own rights as
// always.
//
// Transaction length. The whole import is one transaction: for the 10 000 rows of the default limit it
// holds its locks (series counters, unique values) until the end. Run big imports when the site is quiet.

// MaxReportedErrors is how many row errors an import reports in full; the rest is only counted.
const MaxReportedErrors = 100

// ImportMode says whether an import only inserts, or updates documents that exist.
type ImportMode int

const (
	// ImportInsert: every row is a new document.
	ImportInsert ImportMode = iota
	// ImportUpsert: every row names a document (name): it is updated if it exists and inserted if not.
	ImportUpsert
)

// ImportRow is one row of an import, already mapped to values.
type ImportRow struct {
	// Row is the 1-based number of the row among the data rows.
	Row int
	// Line is, for a CSV file, the line of the file the row starts on; 0 for JSON.
	Line uint64
	// Name is the name given for the row; "" when it gave none.
	Name string
	// NameGiven says that the row gave a name that is the empty text (JSON `"name": ""`; Rust: Some("")).
	NameGiven bool
	// Values are the values the row gives.
	Values map[string]core.Value
	// Tables are the rows of the Table fields the row gives (JSON only).
	Tables map[string][]core.Row
	// Empty are the fields whose CSV cell was empty. An insert leaves them out (the default or null
	// applies); an upsert of an existing document sets them to null.
	Empty []string
}

// ImportError is a row that failed.
type ImportError struct {
	// Row is the 1-based data row number.
	Row int
	// Line is the line of a CSV file; 0 for JSON.
	Line uint64
	// Name is the document name, if the row gave one.
	Name string
	// NameGiven says the name is the empty text.
	NameGiven bool
	// Err is why.
	Err error
}

// ImportOutcome is what an import did (or, rolled back, would have done).
type ImportOutcome struct {
	// Inserted is the number of rows that inserted a document.
	Inserted uint64
	// Updated is the number of rows that updated a document.
	Updated uint64
	// Errors are the first MaxReportedErrors row errors, in row order.
	Errors []ImportError
	// ErrorCount is the number of rows that failed.
	ErrorCount uint64
	// Committed says the transaction was committed.
	Committed bool
}

// Import imports rows as caller; see the comment above. With commit false nothing is kept (a dry run).
//
// Errors: ErrPermissionDenied when the caller lacks import, create or (upsert) write, before any row; a
// database error that is not about a row's data (a lost connection, for example) ends the import and is
// returned, rolled back. Row errors are in the outcome.
func Import(ctx context.Context, client TxStarter, registry *core.Registry, doctype string, caller core.Caller,
	rows []ImportRow, mode ImportMode, commit bool) (*ImportOutcome, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	rights := []core.Right{core.RightImport, core.RightCreate}
	if mode == ImportUpsert {
		rights = append(rights, core.RightWrite)
	}
	for _, right := range rights {
		if _, err := accessTo(dt, caller, right); err != nil {
			return nil, err
		}
	}
	tx, rollback, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer rollback()
	outcome := &ImportOutcome{}
	for _, item := range rows {
		// The row's own savepoint: rolled back on an error, so the transaction goes on.
		savepoint, rollbackRow, err := begin(ctx, tx)
		if err != nil {
			return nil, err
		}
		done, err := importRow(ctx, savepoint, registry, dt.Name, caller, item, mode)
		switch {
		case err == nil:
			if err := commit0(ctx, savepoint); err != nil {
				return nil, err
			}
			if done == rowInserted {
				outcome.Inserted++
			} else {
				outcome.Updated++
			}
		case isRowError(err):
			rollbackRow()
			outcome.ErrorCount++
			if len(outcome.Errors) < MaxReportedErrors {
				outcome.Errors = append(outcome.Errors, ImportError{Row: item.Row, Line: item.Line, Name: item.Name, NameGiven: item.NameGiven, Err: err})
			}
		default:
			rollbackRow()
			return nil, err
		}
	}
	if commit && outcome.ErrorCount == 0 {
		if err := commit0(ctx, tx); err != nil {
			return nil, err
		}
		outcome.Committed = true
	}
	return outcome, nil
}

// commit0 commits a transaction or savepoint.
func commit0(ctx context.Context, tx interface{ Commit(context.Context) error }) error {
	return wrap(tx.Commit(ctx))
}

type rowDone int

const (
	rowInserted rowDone = iota
	rowUpdated
)

// isRowError is an error about the row's data or the document's state, as opposed to a failure of the
// database or the app that would fail every row.
func isRowError(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Kind {
	case ErrPostgres:
		// Data exceptions (22...) and integrity violations (23...) are about the row.
		var pg *pgconn.PgError
		return errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23"))
	case ErrPool, ErrLockTimeout, ErrDecode, ErrJob, ErrWebhook, ErrInvalidActor, ErrUnknownDocType, ErrChildDocType,
		ErrTLS, ErrCreatePool:
		return false
	}
	return true
}

func importRow(ctx context.Context, c Client, registry *core.Registry, doctype string, caller core.Caller,
	item ImportRow, mode ImportMode) (rowDone, error) {
	if mode == ImportInsert {
		doc := core.Document{Name: item.Name, NameGiven: item.NameGiven, Values: cloneValues(item.Values), Tables: cloneTables(item.Tables)}
		if _, err := Insert(ctx, c, registry, doctype, doc, caller); err != nil {
			return 0, err
		}
		return rowInserted, nil
	}
	if item.Name == "" && !item.NameGiven {
		return 0, &Error{Kind: ErrNaming, Message: "an upsert needs a `name` for every row"}
	}
	stored, err := Get(ctx, c, registry, doctype, item.Name, caller)
	if err != nil {
		return 0, err
	}
	if stored == nil {
		doc := core.Document{Name: item.Name, NameGiven: item.NameGiven, Values: cloneValues(item.Values), Tables: cloneTables(item.Tables)}
		if _, err := Insert(ctx, c, registry, doctype, doc, caller); err != nil {
			return 0, err
		}
		return rowInserted, nil
	}
	values := cloneValues(item.Values)
	if values == nil {
		values = map[string]core.Value{}
	}
	for _, field := range item.Empty {
		values[field] = core.Null()
	}
	// The naming field gives the name and cannot be changed: its value is the name already (the row is the
	// same document), so it is left out; a different value is a rename, refused.
	if dt, err := documentDocType(registry, doctype); err == nil && dt.Naming.Kind == core.NamingField {
		field := dt.Naming.Field
		if given, ok := values[field]; ok {
			delete(values, field)
			if given.Kind != core.KindNull && !(given.Kind == core.KindText && given.Text == stored.Name) {
				return 0, &Error{Kind: ErrNaming, Message: "field `" + field +
					"` gives the document its name and cannot be changed (renames are not supported yet)"}
			}
		}
	}
	changes := core.Changes{Values: values, Tables: cloneTables(item.Tables)}
	if _, err := Update(ctx, c, registry, doctype, item.Name, stored.Version, changes, caller); err != nil {
		return 0, err
	}
	return rowUpdated, nil
}
