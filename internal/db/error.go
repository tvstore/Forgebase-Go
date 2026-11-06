package db

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
)

// ErrorKind is the variant of an Error. It mirrors the DbError enum of the Rust crate
// forgebase-db; only the variants that the code in this package needs are here. The rest arrive
// with the phases that need them (document operations in P2, and so on): add a kind, its fields
// and its Error text.
type ErrorKind int

const (
	// ErrPostgres is a PostgreSQL or connection error. The message includes the server's message
	// and SQLSTATE; Err is the driver error (a *pgconn.PgError when the server answered), so
	// errors.As reaches it.
	ErrPostgres ErrorKind = iota + 1
	// ErrLockTimeout: a DDL statement waited longer than the sync lock_timeout (SQLSTATE 55P03).
	ErrLockTimeout
	// ErrTLS: the TLS settings of the database URL cannot be used. Never contains the URL or the
	// password.
	ErrTLS
	// ErrCreatePool: the connection pool could not be created (for example an invalid database
	// URL). Message is the reason; it never contains the URL or the password.
	ErrCreatePool
	// ErrPool: no connection could be obtained from the pool. Message is the reason.
	ErrPool
	// ErrChecksumMismatch: an applied framework migration no longer matches the embedded SQL
	// (Version, Name, Expected, Actual).
	ErrChecksumMismatch
	// ErrUnknownMigration: the database has a migration this build does not know, so the database
	// is newer than the code (Version, Name).
	ErrUnknownMigration
	// ErrFrameworkNotMigrated: schema sync needs the framework tables, which are not there yet.
	ErrFrameworkNotMigrated
	// ErrSyncConflicts: the metadata cannot be applied without a destructive or risky change;
	// nothing was applied (Conflicts).
	ErrSyncConflicts
	// ErrInvalidMetadata: the DocTypes as a whole are inconsistent (Problems, from
	// core.Registry.CheckReferences). Nothing was changed.
	ErrInvalidMetadata
)

// Error is everything that can go wrong in this package. Only the members that belong to Kind are
// set. It is always returned as *Error; use errors.As to get it.
type Error struct {
	Kind ErrorKind
	// Message is the reason, for ErrPostgres, ErrTLS, ErrCreatePool and ErrPool.
	Message string
	// Version and Name identify the migration of ErrChecksumMismatch and ErrUnknownMigration.
	Version int32
	Name    string
	// Expected and Actual are the checksums of ErrChecksumMismatch (the embedded SQL and the
	// database).
	Expected, Actual string
	// Conflicts are the conflicts of ErrSyncConflicts.
	Conflicts []Conflict
	// Problems are the definition errors of ErrInvalidMetadata.
	Problems []core.DefinitionError
	// Err is the underlying driver error, if any.
	Err error
}

// Error is the Rust Display text.
func (e *Error) Error() string {
	switch e.Kind {
	case ErrPostgres, ErrTLS:
		return e.Message
	case ErrLockTimeout:
		return "timed out waiting for a database lock (another transaction holds a conflicting lock); the statement that timed out was rolled back (earlier committed steps, such as framework migrations in `migrate`, stay applied), retry later"
	case ErrCreatePool:
		return "cannot create connection pool: " + e.Message
	case ErrPool:
		return "cannot get a database connection: " + e.Message
	case ErrChecksumMismatch:
		return fmt.Sprintf("framework migration %d (%s) was changed after it was applied: expected checksum %s, database has %s",
			e.Version, e.Name, e.Expected, e.Actual)
	case ErrUnknownMigration:
		return fmt.Sprintf("database has framework migration %d (%s) that this version of Forgebase does not know; upgrade Forgebase",
			e.Version, e.Name)
	case ErrFrameworkNotMigrated:
		return "framework tables are missing; run the framework migrations before schema sync"
	case ErrSyncConflicts:
		return fmt.Sprintf("schema sync refused, %d conflict(s): %s", len(e.Conflicts), joinStringers(e.Conflicts))
	case ErrInvalidMetadata:
		parts := make([]string, len(e.Problems))
		for i, p := range e.Problems {
			parts[i] = p.Error()
		}
		return "invalid DocType metadata: " + strings.Join(parts, "; ")
	}
	return "database error"
}

// Unwrap gives errors.Is and errors.As access to the driver error.
func (e *Error) Unwrap() error { return e.Err }

// IsMissingDatabase reports whether err is a server refusal because the database does not exist
// (SQLSTATE 3D000).
func IsMissingDatabase(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == sqlStateInvalidCatalogName
}

// SQLSTATE codes that this package reacts to.
const (
	sqlStateLockNotAvailable   = "55P03"
	sqlStateInvalidCatalogName = "3D000"
)

// wrap turns an error of the driver into an *Error: a lock timeout (55P03) becomes ErrLockTimeout,
// anything else ErrPostgres with the server's message and SQLSTATE (Rust From<tokio_postgres::
// Error>). An error that already is an *Error, and nil, pass through.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var own *Error
	if errors.As(err, &own) {
		return err
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == sqlStateLockNotAvailable {
		return &Error{Kind: ErrLockTimeout, Err: err}
	}
	return &Error{Kind: ErrPostgres, Message: describe(err), Err: err}
}

// describe is the message of a driver error. A server error reads "SEVERITY: message (SQLSTATE
// code)" with "; detail: ..." when the server gave a detail; any other error is its own text,
// which includes its causes.
func describe(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		text := fmt.Sprintf("%s: %s (SQLSTATE %s)", pg.Severity, pg.Message, pg.Code)
		if pg.Detail != "" {
			text += "; detail: " + pg.Detail
		}
		return text
	}
	return err.Error()
}

func joinStringers[T fmt.Stringer](items []T) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = item.String()
	}
	return strings.Join(parts, "; ")
}
