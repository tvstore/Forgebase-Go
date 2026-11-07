package db

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
)

// ErrorKind is the variant of an Error. It mirrors the DbError enum of the Rust crate
// forgebase-db; only the variants that the code in this package needs are here (P2.1 added those of the
// document operations). The rest arrive with the phases that need them (listing in P3, transitions
// in P6, and so on): add a kind, its fields and its Error text.
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
	// ErrUnknownDocType: the DocType name is not in the registry (Name).
	ErrUnknownDocType
	// ErrChildDocType: an operation was called with a child DocType (DocType).
	ErrChildDocType
	// ErrChangedWhileReading: the rows of a document changed several times while it was read
	// (DocType, Name).
	ErrChangedWhileReading
	// ErrValidation: the document or the changes break the DocType's rules (Validation).
	ErrValidation
	// ErrNotFound: no document of this DocType has this name, or the caller cannot read it
	// (DocType, Name).
	ErrNotFound
	// ErrConflict: the document changed since the caller read it (DocType, Name, ExpectedVersion,
	// CurrentVersion).
	ErrConflict
	// ErrLinkedDocumentExists: the document cannot be deleted because other documents link to it
	// (DocType, Name, ReferencedBy).
	ErrLinkedDocumentExists
	// ErrPermissionDenied: the caller lacks a right (DocType, Right).
	ErrPermissionDenied
	// ErrUniqueViolation: a unique constraint was violated, SQLSTATE 23505 (Message).
	ErrUniqueViolation
	// ErrInvalidActor: the actor is empty or contains NUL.
	ErrInvalidActor
	// ErrNaming: the document name conflicts with the DocType's naming rule (Message).
	ErrNaming
	// ErrDecode: a stored value could not be read (DocType, Field, Message).
	ErrDecode
	// ErrNoChanges: an update was called without any change.
	ErrNoChanges
	// ErrInvalidDocStatus: the operation is not allowed in the document's docstatus (DocType, Name,
	// DocStatus, Operation).
	ErrInvalidDocStatus
	// ErrNotSubmittable: submit, cancel or amend on a DocType that is not submittable (DocType,
	// Operation).
	ErrNotSubmittable
	// ErrFieldLocked: a change to a submitted document touches a locked field (DocType, Name,
	// Field).
	ErrFieldLocked
	// ErrAlreadyAmended: the cancelled document was already amended (DocType, Name, AmendedBy).
	ErrAlreadyAmended
	// ErrWorkflowRequired: submit or cancel on a DocType with a workflow (DocType, Operation).
	ErrWorkflowRequired
	// ErrEditDenied: the document's workflow state has edit_roles the caller does not have
	// (DocType, State).
	ErrEditDenied
	// ErrJob: a job cannot be enqueued (Message).
	ErrJob
	// ErrHook: a lifecycle hook rejected the operation (DocType, Hook, Message).
	ErrHook
	// ErrInvalidQuery: a list query cannot be run: an unknown field, an operator that does not fit
	// the field, a value of the wrong kind, or a limit or offset out of range (Message). Nothing from
	// the query was sent to the database.
	ErrInvalidQuery
	// ErrAppChecksumMismatch: an applied app migration no longer matches the SQL the app declares
	// now (App, Version, Name, Expected, Actual).
	ErrAppChecksumMismatch
	// ErrUnknownAppMigration: the database has an app migration that the app does not declare any
	// more (App, Version, Name).
	ErrUnknownAppMigration
	// ErrOutOfOrderAppMigration: an app migration is not applied but one with a higher version of
	// the same app is (App, Version, Name, AppliedVersion).
	ErrOutOfOrderAppMigration
	// ErrAppMigrationFailed: an app migration failed and was rolled back (App, Version, Name,
	// Message).
	ErrAppMigrationFailed
	// ErrCreateDatabase: CreateDatabase cannot use the URL it was given (Message). Never contains the
	// URL.
	ErrCreateDatabase
	// ErrBackup: PgEnvironment or InspectDatabase cannot use the URL or the server's answer
	// (Message). Never contains the URL or a value of it.
	ErrBackup
	// ErrNoWorkflow: a transition, its list or its history on a DocType without a workflow (DocType).
	ErrNoWorkflow
	// ErrUnknownTransition: the document's state has no transition with this action (DocType, Name,
	// State, Action).
	ErrUnknownTransition
	// ErrTransitionDenied: the caller may not take the transition (DocType, Action, Right).
	ErrTransitionDenied
	// ErrInvalidView: a saved view cannot be written (Message).
	ErrInvalidView
	// ErrViewExists: a view with this label exists for the DocType (DocType, Label).
	ErrViewExists
	// ErrViewDenied: the caller may not do this with a view they can see (Message).
	ErrViewDenied
	// ErrReportNotFound: the report does not exist or the caller may not run it (Report).
	ErrReportNotFound
	// ErrReportTimeout: a report ran over its timeout or waited too long for a lock (Report).
	ErrReportTimeout
	// ErrReportReadOnly: a report's SQL tried to write (Report).
	ErrReportReadOnly
	// ErrReport: the statement of a report does not have the declared parameters or columns
	// (Message; never the SQL).
	ErrReport
	// ErrReportCheck: migrate found reports whose SQL does not fit what they declare (Problems as
	// text in Details).
	ErrReportCheck
)

// Error is everything that can go wrong in this package. Only the members that belong to Kind are
// set. It is always returned as *Error; use errors.As to get it.
type Error struct {
	Kind ErrorKind
	// Message is the reason, for ErrPostgres, ErrTLS, ErrCreatePool, ErrPool, ErrCreateDatabase and ErrBackup.
	Message string
	// Version and Name identify the migration of ErrChecksumMismatch and ErrUnknownMigration, and
	// of the app migration errors.
	Version int32
	Name    string
	// App is the app of the app migration errors.
	App string
	// AppliedVersion is the highest applied version of ErrOutOfOrderAppMigration.
	AppliedVersion int32
	// Expected and Actual are the checksums of ErrChecksumMismatch (the embedded SQL and the
	// database).
	Expected, Actual string
	// Conflicts are the conflicts of ErrSyncConflicts.
	Conflicts []Conflict
	// Problems are the definition errors of ErrInvalidMetadata.
	Problems []core.DefinitionError
	// DocType is the DocType of the document errors (Rust: doctype).
	DocType string
	// Field is the field of ErrDecode and ErrFieldLocked.
	Field string
	// ExpectedVersion and CurrentVersion are the versions of ErrConflict.
	ExpectedVersion, CurrentVersion int64
	// ReferencedBy is the DocType of the documents that link to the one ErrLinkedDocumentExists
	// is about; nil when it is not known or the caller may not read it (ADR 0011).
	ReferencedBy *string
	// Right is the right ErrPermissionDenied is about.
	Right core.Right
	// Validation are the problems of ErrValidation.
	Validation []core.ValidationError
	// DocStatus is the docstatus of ErrInvalidDocStatus.
	DocStatus core.DocStatus
	// Operation is the refused operation of ErrInvalidDocStatus, ErrNotSubmittable and
	// ErrWorkflowRequired: update, delete, submit, cancel or amend.
	Operation string
	// AmendedBy is the amendment of ErrAlreadyAmended.
	AmendedBy string
	// State is the workflow state of ErrEditDenied.
	State string
	// Action is the transition of ErrUnknownTransition and ErrTransitionDenied.
	Action string
	// Label is the label of ErrViewExists.
	Label string
	// Report is the full name of the report of the report errors.
	Report string
	// Details are the problems of ErrReportCheck, one message each.
	Details []string
	// Hook is the failed hook of ErrHook: before_insert, validate, before_delete, before_submit,
	// before_cancel or before_transition.
	Hook string
	// Err is the underlying driver error, if any.
	Err error
}

// Error is the Rust Display text.
func (e *Error) Error() string {
	switch e.Kind {
	case ErrPostgres, ErrTLS, ErrCreateDatabase, ErrBackup:
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
	case ErrAppChecksumMismatch:
		return fmt.Sprintf("migration %d (%s) of app `%s` was changed after it was applied: expected checksum %s, database has %s",
			e.Version, e.Name, e.App, e.Expected, e.Actual)
	case ErrUnknownAppMigration:
		return fmt.Sprintf("database has migration %d (%s) of app `%s` that the app does not declare; the database is newer than the app, or a migration was removed",
			e.Version, e.Name, e.App)
	case ErrOutOfOrderAppMigration:
		return fmt.Sprintf("migration %d (%s) of app `%s` is not applied, but migration %d of the app is; a new migration must have a version above every applied one (renumber it above %d); nothing was applied",
			e.Version, e.Name, e.App, e.AppliedVersion, e.AppliedVersion)
	case ErrAppMigrationFailed:
		return fmt.Sprintf("migration %d (%s) of app `%s` failed and was rolled back (earlier migrations stay applied): %s",
			e.Version, e.Name, e.App, e.Message)
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
	case ErrUnknownDocType:
		return fmt.Sprintf("unknown DocType `%s`", e.Name)
	case ErrChildDocType:
		return fmt.Sprintf("DocType `%s` is a child DocType: its rows belong to a parent document and are read and written through the parent", e.DocType)
	case ErrChangedWhileReading:
		return fmt.Sprintf("%s `%s` was changed while it was being read; retry", e.DocType, e.Name)
	case ErrValidation:
		parts := make([]string, len(e.Validation))
		for i, p := range e.Validation {
			parts[i] = p.Error()
		}
		return "validation failed: " + strings.Join(parts, "; ")
	case ErrNotFound:
		return fmt.Sprintf("%s `%s` not found", e.DocType, e.Name)
	case ErrConflict:
		return fmt.Sprintf("%s `%s` was changed by someone else: expected version %d, current version %d",
			e.DocType, e.Name, e.ExpectedVersion, e.CurrentVersion)
	case ErrLinkedDocumentExists:
		by := "another document"
		if e.ReferencedBy != nil {
			by = *e.ReferencedBy
		}
		return fmt.Sprintf("%s `%s` cannot be deleted: it is linked from %s", e.DocType, e.Name, by)
	case ErrPermissionDenied:
		return fmt.Sprintf("permission denied: no `%s` right on DocType `%s`", e.Right, e.DocType)
	case ErrUniqueViolation, ErrNaming:
		return e.Message
	case ErrInvalidQuery:
		return "invalid list query: " + e.Message
	case ErrInvalidActor:
		return "actor must be a non-empty string without NUL characters"
	case ErrDecode:
		return fmt.Sprintf("cannot read field `%s` (column \"%s\") of DocType `%s`: %s", e.Field, e.Field, e.DocType, e.Message)
	case ErrNoChanges:
		return "update needs at least one changed field"
	case ErrInvalidDocStatus:
		return fmt.Sprintf("cannot %s %s `%s`: it is %s", e.Operation, e.DocType, e.Name, e.DocStatus)
	case ErrNotSubmittable:
		return fmt.Sprintf("cannot %s: DocType `%s` is not submittable (`submittable = true`)", e.Operation, e.DocType)
	case ErrFieldLocked:
		return fmt.Sprintf("%s `%s` is submitted: field `%s` cannot be changed (only fields marked allow_on_submit can; tables are locked)",
			e.DocType, e.Name, e.Field)
	case ErrAlreadyAmended:
		return fmt.Sprintf("%s `%s` was already amended by `%s`", e.DocType, e.Name, e.AmendedBy)
	case ErrWorkflowRequired:
		return fmt.Sprintf("DocType `%s` has a workflow; use a transition (cannot %s)", e.DocType, e.Operation)
	case ErrEditDenied:
		return fmt.Sprintf("permission denied: a `%s` document in state `%s` can be edited only by the roles of that state", e.DocType, e.State)
	case ErrNoWorkflow:
		return fmt.Sprintf("DocType `%s` has no workflow", e.DocType)
	case ErrUnknownTransition:
		return fmt.Sprintf("%s `%s` is in state `%s`: no transition `%s` from it", e.DocType, e.Name, e.State, e.Action)
	case ErrTransitionDenied:
		return fmt.Sprintf("permission denied: transition `%s` of DocType `%s` needs one of its roles and the `%s` right", e.Action, e.DocType, e.Right)
	case ErrInvalidView:
		return "invalid saved view: " + e.Message
	case ErrViewExists:
		return fmt.Sprintf("a view named %s already exists for DocType `%s`", core.DebugQuote(e.Label), e.DocType)
	case ErrViewDenied:
		return "permission denied: " + e.Message
	case ErrReportNotFound:
		return fmt.Sprintf("report `%s` not found", e.Report)
	case ErrReportTimeout:
		return fmt.Sprintf("report `%s` took too long and was stopped", e.Report)
	case ErrReportReadOnly:
		return fmt.Sprintf("report `%s` tried to write, which a report may not do", e.Report)
	case ErrReport:
		return e.Message
	case ErrReportCheck:
		return fmt.Sprintf("report check failed (%d problem(s))", len(e.Details))
	case ErrJob:
		return "job: " + e.Message
	case ErrHook:
		return fmt.Sprintf("%s hook of DocType `%s` rejected the operation: %s", e.Hook, e.DocType, e.Message)
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
	sqlStateUniqueViolation    = "23505"
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
	if tlsErr, ok := tlsConnectError(err); ok {
		return tlsErr
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
