package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Document persistence: Insert, Get, Update and Delete of one document, with its child table rows,
// in the tables of its DocType (the Rust module document.rs; Submit, Cancel and Amend are in
// docstatus.go).
//
// Every mutating function takes a TxStarter (a pool, a connection, or a pgx.Tx the caller owns) and
// the registry, and runs all of its statements inside client.Begin(): a real transaction on a pool
// or a connection, a savepoint when the caller already has a transaction. A document and its rows
// are therefore written atomically whichever client is passed, and a failed call leaves a caller's
// transaction usable. SQL is built only from validated, quoted identifiers and numeric
// placeholders with explicit casts; values are always bound parameters.
//
// Link fields hold the target's id in the database; this package resolves the target name to the
// id on the way in and back to the name on the way out (see Insert). Table fields are rows of a
// child DocType's table, see Insert and Update.
//
// The app's lifecycle hooks (core.DocTypeHooks, registered in the registry) run inside the same
// transaction at fixed points: the order of every operation is listed in the documentation of
// Insert, Update and Delete. A hook error rolls everything back (ErrHook). Hooks cannot reach the
// database. They may enqueue background jobs: after the hooks and before the commit, the jobs they
// asked for are inserted in the same transaction (the outbox, ADR 0013), so a job exists if and only
// if the operation committed. An unknown job name or an oversize payload is ErrJob and fails the
// operation.
//
// DocTypes with Submittable = true also have Submit, Cancel and Amend, and their documents carry a
// DocStatus. A DocType with a workflow (M8.1, ADR 0015) changes state only through a transition
// (P6), which continues in the update, submit or cancel pipeline of this package; Submit and Cancel
// refuse it (ErrWorkflowRequired).
//
// # Permissions (M3.2, ADR 0011)
//
// Every operation takes the core.Caller and enforces the DocType's [[permissions]] with the one
// evaluator, core.Evaluate; this package never compares roles itself. The core.Access it returns is
// applied as follows:
//
//   - Read access is applied in SQL, never by fetching and filtering: for core.AccessOwnerOnly every
//     statement that finds a document has AND "owner" = $n (bound). A document the caller cannot
//     read is therefore not found: Get returns nil, and every other operation fails with
//     ErrNotFound, before the version is compared (a stale version on an unreadable document is
//     ErrNotFound, never ErrConflict).
//   - A right the caller does not have on the DocType at all (core.AccessDenied, which does not
//     depend on any document) is ErrPermissionDenied, before anything is looked up. So is a right
//     limited to own documents (if_owner) on a readable document of somebody else. Both say nothing
//     about the data.
//   - A Link name is resolved with the caller's read access on the target DocType: a target the
//     caller cannot read is reported exactly like a missing one.
//   - Child rows follow the parent document; they are never addressed alone.
//
// Field levels (M10.2, ADR 0024). A field has a PermLevel; a field the caller cannot read is unknown
// to them. Get does not select it, and every operation that returns a document removes it from the
// result (after the hooks and the webhook deliveries, which carry the whole document). The caller's
// input for such a field is an unknown field, and for a field they can read but not write
// core.ErrNotPermitted when it would change the field, both before any hook runs. Hooks and the
// merge of an update see every field, so an update by a user who cannot see a field keeps its
// value. A DocType without PermLevel, and the system context and the Administrator role, are not
// affected.
//
// User permissions (M10.3, ADR 0025). A caller can be restricted to some documents of a DocType.
// The scope (core.RowScopeOf) is applied in the same SQL as if_owner, by the same code (readScope):
// the own name of a restricted DocType, and every top-level Link to a restricted target (empty or
// allowed). A document outside it is not found on every path, the locking reads included (not
// returned, not locked). A Link to a target outside the target's scope is core.ErrLinkNotFound (a
// kept stored value is still resolved with full access). A caller restricted on a DocType cannot
// create or amend one of it (ErrPermissionDenied): the new name could not be checked and they could
// not see the result.
//
// The system caller and the Administrator role bypass the rules.

// Queryer is what the statements of one operation run on: a pgx.Tx, a *pgxpool.Pool, a
// *pgxpool.Conn or a *pgx.Conn.
type Queryer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Client is a Queryer that can also start a transaction (a savepoint, for a pgx.Tx): what Get takes.
type Client interface {
	Queryer
	TxStarter
}

// readAttempts is how often Get re-reads a document whose rows changed while it was being read.
const readAttempts = 5

// StoredDocument is a document as stored: the standard columns, the field values and the table
// rows.
type StoredDocument struct {
	// ID is the surrogate key (UUID v7, generated by the application).
	ID uuid.UUID
	// Name is the human-facing unique name.
	Name string
	// Owner is the caller that created the document: the username, or the system actor.
	Owner string
	// CreatedAt is the creation time (database now(), that is the transaction start time).
	CreatedAt time.Time
	// ModifiedAt is the time of the last change (database now()).
	ModifiedAt time.Time
	// ModifiedBy is the caller that made the last change: the username, or the system actor.
	ModifiedBy string
	// Version starts at 1 and is incremented by every update, submit and cancel.
	Version int64
	// DocStatus is the docstatus of a document of a submittable DocType; nil for any other DocType.
	DocStatus *core.DocStatus
	// AmendedFrom is, for a document made by Amend, the name of the cancelled document it replaces.
	// "" otherwise, and for every DocType that is not submittable.
	AmendedFrom string
	// WorkflowState is the name of the workflow state the document is in (M8.1, ADR 0015): set by
	// insert, amend and transitions, never by a caller. "" for a DocType without a workflow, and for
	// a document of a DocType with a workflow that has no state yet (migrate gives existing
	// documents one; such a document has no transitions).
	WorkflowState string
	// Values has one entry per column field of the DocType (every field except Table fields);
	// Null when absent. A Link field holds the target document's name as a text value.
	Values map[string]core.Value
	// Tables has one entry per Table field of the DocType (an empty list when there are no rows):
	// the rows in order, each with one entry per field of the child DocType. Row ids and positions
	// are not exposed: the position is the place in the list, and the ids change when a table's
	// rows are replaced (see Update).
	Tables map[string][]core.Row
}

// begin starts the transaction (or savepoint) of an operation. The returned function ends it: it
// rolls back unless Commit was called, whatever happens, a panic of a hook included.
func begin(ctx context.Context, client TxStarter) (pgx.Tx, func(), error) {
	tx, err := client.Begin(ctx)
	if err != nil {
		return nil, nil, wrap(err)
	}
	// The rollback must not depend on ctx: a cancelled context is exactly when it is needed.
	return tx, func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }, nil
}

func commit(ctx context.Context, tx pgx.Tx) error { return wrap(tx.Commit(ctx)) }

// Insert inserts a document and returns it as stored.
//
// doctype is the name of a DocType in registry; it must not be a child DocType (ErrChildDocType),
// and a name the registry does not know is ErrUnknownDocType. doc is validated first (core.Validate,
// all problems collected in ErrValidation). The name follows the DocType's naming rule: with random
// naming it is the new UUID v7 and doc.Name must be empty; with field naming it is the value of
// that field and doc.Name must be empty or equal to it (ErrNaming); with a series it is the next
// name of the series and doc.Name must be empty. The caller's name (core.Caller.Name: a non-empty
// string without NUL, ErrInvalidActor) is recorded as owner and modified_by; the caller is not
// authenticated here. The caller needs the create right (ErrPermissionDenied, checked first;
// if_owner does not restrict it, the creator is the owner). created_at and modified_at come from
// the database's now(), and version is 1. A duplicate value in a unique field or a duplicate name is
// ErrUniqueViolation. doc is not changed (the hooks work on a copy).
//
// Atomicity. Everything runs in client.Begin() (see above): the document and all its rows are
// written, or nothing is. That holds on a pool too.
//
// Read-only fields. A field marked read_only is written only by hooks: a non-null value for one in
// doc, or in a row of any table, is refused before any hook runs (core.ErrReadOnly in
// ErrValidation, wrapped in core.ErrInRow for a row). An amendment copies the cancelled document,
// and the copy is not caller input, so it is not refused.
//
// Pipeline. In this order: the DocType must not be a child DocType; the caller's input is checked
// for read-only fields; the registered BeforeInsert hooks run on doc (in registration order, and
// they may modify it); the Validate hooks run; the framework validates the result (core.Validate);
// Links are resolved; the name is chosen; the document and its rows are written. The hooks see the
// document before framework validation, so it may be invalid, but the validation runs again after
// them. A hook error is ErrHook and nothing is written.
//
// Links. A non-null Link value, in the document or in a row, is the name of a document of the
// target DocType. Each one is looked up with SELECT "id" ... FOR KEY SHARE on the same transaction,
// which locks the target against a concurrent delete until the transaction ends (so the lock covers
// the write). A target the caller cannot read (no read access to the target DocType, or an if_owner
// read on a document of somebody else) is treated exactly like a name that does not exist: a
// core.ErrLinkNotFound in ErrValidation (wrapped in core.ErrInRow for a row), together with the
// other problems, so the error does not tell a hidden document from a missing one.
//
// Table rows. doc.Tables holds the rows of each Table field. They are inserted after the document,
// one INSERT per row, with new UUID v7 ids, the document's id as parent_id and idx 1, 2, 3, ... in
// list order.
//
// Series. With a naming series the number comes from the fb_series counter row of the series
// prefix, in the same transaction as the insert (INSERT ... ON CONFLICT DO UPDATE ... RETURNING):
// the row is locked until the transaction ends, so inserts with the same prefix are serialized, and
// a rollback (a failed insert, a caller's transaction rolled back) gives the number back, so
// numbering has no gaps. The prefix is rendered with the UTC date of the transaction start (now()
// of the database, that is the start of the caller's transaction if there is one); it and the
// number are bound parameters. The step happens at the naming point of the pipeline, after the
// hooks and the validation, so a document that is refused earlier does not take a number.
//
// A name collision blocks the series. If the rendered name already exists (for example after the
// pattern changed to another prefix that renders a used name), the insert fails with
// ErrUniqueViolation and the rollback returns the number, so every later insert retries the same
// number and fails too. Set the counter by hand with an upsert (the row may not exist yet, since the
// rollback also removes a row the failed insert created): INSERT INTO fb_series (prefix, current)
// VALUES ('<prefix>', <n>) ON CONFLICT (prefix) DO UPDATE SET current = EXCLUDED.current, choosing
// <n> so that the names rendered for <n> + 1 and the numbers after it are not already used.
// Numbering is gapless per rendered prefix (the fb_series key): two DocTypes whose patterns render
// the same prefix share one counter and each sees gaps; use a distinct prefix per DocType.
//
// Docstatus. A document of a submittable DocType starts as a draft (docstatus 0, no amended_from);
// neither can be set through doc (they are not fields, so a value for them is an unknown field).
//
// Workflow. A document of a DocType with a workflow starts in the initial state (the first one);
// workflow_state cannot be set through doc either.
func Insert(ctx context.Context, client TxStarter, registry *core.Registry, doctype string, doc core.Document, caller core.Caller) (*StoredDocument, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	if _, err := accessTo(dt, caller, core.RightCreate); err != nil {
		return nil, err
	}
	if err := refuseRestrictedCreate(dt, caller, core.RightCreate); err != nil {
		return nil, err
	}
	levels := levelsOf(dt, caller)
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	jobs := core.NewEnqueuedJobs()
	stored, err := insertIn(ctx, tx, registry, dt, cloneDocument(doc), caller, nil, jobs)
	if err != nil {
		return nil, scrub(err, dt, levels)
	}
	if err := writeJobs(ctx, tx, registry, jobs, caller); err != nil {
		return nil, err
	}
	// The webhooks get the whole document: they run as the system, configured by an administrator.
	if err := writeWebhooks(ctx, tx, registry, dt, []string{"insert"}, stored, caller); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return redact(stored, dt, levels), nil
}

func insertIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, doc core.Document,
	caller core.Caller, amend *amendOf, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	actor := caller.Name()
	operation := core.OpInsert
	if amend != nil {
		operation = core.OpAmend
	}
	// An amendment copies the cancelled document: that is not caller input (it keeps the values of
	// the fields the caller cannot read, too).
	if amend == nil {
		if err := checkInput(doctype, levelsOf(doctype, caller), doc.Values, doc.Tables, nil); err != nil {
			return nil, err
		}
		if err := checkReadOnly(registry, doctype, doc.Values, doc.Tables, nil); err != nil {
			return nil, err
		}
	}
	hookCtx := core.NewHookContext(doctype.Name, actor, operation).WithJobs(jobs)
	if err := runHooks(registry, doctype, "before_insert", func(h core.DocTypeHooks) error {
		return h.BeforeInsert(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	if err := runHooks(registry, doctype, "validate", func(h core.DocTypeHooks) error {
		return h.Validate(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	errs := core.Validate(registry, doctype, &doc)
	links, writes, err := resolve(ctx, c, registry, caller, doctype, doc.Values, doc.Tables, nil, &errs)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, &Error{Kind: ErrValidation, Validation: errs}
	}
	id := newID()
	name, err := newName(ctx, c, doctype, &doc, id, amend)
	if err != nil {
		return nil, err
	}

	args := []any{[16]byte(id), name, actor}
	for i, f := range doctype.ColumnFields() {
		arg, err := sqlParam(f.Type, links, f.Name, doc.Values[f.Name], 4+i)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	if amend != nil {
		args = append(args, amend.from)
	}
	// A new document, an amendment too, starts in the initial state of the workflow.
	if doctype.Workflow != nil {
		if initial := doctype.Workflow.Initial(); initial != nil {
			args = append(args, initial.Name)
		} else {
			args = append(args, nil)
		}
	}
	stored, err := scanDocument(doctype, c.QueryRow(ctx, insertSQL(doctype, amend != nil), withFormats(args...)...))
	if err != nil {
		return nil, fromWrite(err, doctype, levelsOf(doctype, caller))
	}
	if err := writeRows(ctx, c, stored.ID, writes); err != nil {
		return nil, err
	}
	if stored.Tables, err = loadTables(ctx, c, registry, doctype, stored.ID); err != nil {
		return nil, err
	}
	return stored, nil
}

// Get returns the document with this name, or nil (and no error) when there is none the caller
// can read. Link values are the target documents' names; Tables holds the rows of every Table
// field in order.
//
// The caller needs the read right (ErrPermissionDenied when the DocType gives it none at all). With
// an if_owner read the statement has AND "owner" = $n, so a document of somebody else is nil, the
// same as a missing one.
//
// A document with Table fields is read with several statements. If an update commits in between,
// the read starts over (a document and its rows are never mixed from two versions, also at READ
// COMMITTED); after a few unlucky attempts it fails with ErrChangedWhileReading.
func Get(ctx context.Context, client Client, registry *core.Registry, doctype, name string, caller core.Caller) (*StoredDocument, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	scope, err := readScopeOf(dt, caller)
	if err != nil {
		return nil, err
	}
	// The fields the caller cannot read are not selected at all (ADR 0024).
	dt = core.ReadableDocTypeFor(dt, caller)
	// Without Table fields the read is one statement, already consistent: no transaction needed.
	if len(dt.TableFields()) == 0 {
		return getIn(ctx, client, registry, dt, name, scope)
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	stored, err := getIn(ctx, tx, registry, dt, name, scope)
	if err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return stored, nil
}

func getIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string, scope readScope) (*StoredDocument, error) {
	scoped := scope.andSQL(2)
	sql := getSQL(doctype, scoped.sql)
	args := append([]any{name}, scoped.args()...)
	for range readAttempts {
		stored, err := scanDocument(doctype, c.QueryRow(ctx, sql, withFormats(args...)...))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if len(doctype.TableFields()) == 0 {
			return stored, nil
		}
		if stored.Tables, err = loadTables(ctx, c, registry, doctype, stored.ID); err != nil {
			return nil, err
		}
		// An update changes the version together with the rows, atomically: if the version is
		// still the one read first, the rows belong to it.
		var current int64
		err = c.QueryRow(ctx, fmt.Sprintf("SELECT \"version\" FROM %s WHERE \"id\" = $1::uuid",
			QuoteIdent(core.TableName(doctype.Name))), [16]byte(stored.ID)).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Deleted meanwhile: read again.
		case err != nil:
			return nil, wrap(err)
		case current == stored.Version:
			return stored, nil
		}
	}
	return nil, &Error{Kind: ErrChangedWhileReading, DocType: doctype.Name, Name: name}
}

// Update changes the given fields of an existing document and returns it as stored.
//
// Only the fields in changes.Values change (a Null clears one); a Table field in changes.Tables
// replaces its rows when they differ from the stored ones (an empty list removes every row;
// identical rows are left alone and keep their ids), and a Table field that is not mentioned keeps
// its rows. Changing the naming field is rejected (ErrNaming): renames are not supported yet. Empty
// changes are ErrNoChanges; changes to tables only are fine. modified_at is set to the database's
// now(), modified_by to the caller's name, and version is incremented; created_at and owner stay.
//
// Permissions. The caller needs the write right on the DocType and must be able to read the
// document (see above): no write or read right at all is ErrPermissionDenied before anything is
// looked up; a document the caller cannot read is ErrNotFound; a readable document of somebody else
// under an if_owner write right is ErrPermissionDenied. All of these come before the version is
// compared and before any hook runs.
//
// Pipeline. The hooks and the validation see the whole document, not only the changes. In this
// order:
//
//  1. the stored document and its rows are read with a row lock (FOR NO KEY UPDATE): an unknown or
//     unreadable name is ErrNotFound, a document the caller may not write is ErrPermissionDenied
//     and a version other than expectedVersion is ErrConflict, in this order and all before any
//     hook runs;
//  2. the caller's changes are checked for read_only fields (core.ErrReadOnly): a top-level value
//     that differs from the stored one is refused (null included when a value is stored; sending
//     the stored value back is allowed), and so is any row in changes.Tables with a non-null value
//     for a read_only field of the child DocType. A client that replaces a table must therefore
//     leave the computed columns out of its rows. Hooks may still set read_only fields;
//  3. the changes are merged into it: the stored values overlaid with changes.Values, and the rows
//     of a Table field from changes.Tables when given, else the stored rows;
//  4. a change to the naming field is rejected;
//  5. the registered Validate hooks run on the merged document (in registration order, and they may
//     modify it; the naming field must stay as it is);
//  6. the framework validates the whole merged document (core.Validate), so a stored value that is
//     not valid any more (a required field that is null in the row, for example) is reported too,
//     and a hook cannot leave an invalid document;
//  7. Links are resolved, then every column field of the merged document is written, and the rows of
//     every Table field whose rows differ from the stored ones (given in changes or modified by a
//     hook) are replaced, so row ids change only for a table whose rows changed. version is
//     incremented in the same statement as the document row, which comes first.
//
// An update is an explicit save: it increments version and sets modified_at even when the merged
// document equals the stored one (for example when the changes repeat the stored values and the
// hooks change nothing). Every change to child rows bumps the version.
//
// A hook error is ErrHook and nothing is written. The DocType rules, the transaction and the Link
// handling are those of Insert, except that the caller's read check on the target applies only to
// new or changed Link values: a Link equal to the stored one (for rows: a name among the stored
// values of that field in that table) is kept and resolved without it.
//
// Docstatus. For a submittable DocType: a draft is updated as above; a cancelled document cannot be
// updated (ErrInvalidDocStatus); a submitted document is locked: only fields marked allow_on_submit
// may change, and its tables never (ErrFieldLocked names the first locked field). The check runs on
// the merged document before the hooks and again after them, so a hook cannot change a locked field
// either (a changes entry equal to the stored value is no change), and only the allow_on_submit
// columns are written. docstatus and amended_from are not fields and cannot be changed here.
//
// Workflow. workflow_state is not a field either: only a transition changes it. A draft in a state
// that has edit_roles can be updated only by a caller with one of those roles (ErrEditDenied,
// checked after the lock, before any hook; the system caller and an administrator pass). The
// caller's write right is needed as always.
//
// Optimistic concurrency. The update is applied only if the document's current version equals
// expectedVersion (the version the caller read). Of two concurrent updates with the same
// expectedVersion, exactly one succeeds. Conflict detection assumes READ COMMITTED, the PostgreSQL
// default: the second writer waits for the row lock and then re-checks the version. In a
// caller-owned transaction at REPEATABLE READ or SERIALIZABLE the losing writer instead gets a
// serialization failure (SQLSTATE 40001, ErrPostgres, not ErrConflict); retry the whole
// transaction.
func Update(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string, expectedVersion int64, changes core.Changes, caller core.Caller) (*StoredDocument, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	levels := levelsOf(dt, caller)
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	jobs := core.NewEnqueuedJobs()
	stored, err := updateIn(ctx, tx, registry, dt, name, expectedVersion, changes, caller, jobs)
	if err != nil {
		return nil, scrub(err, dt, levels)
	}
	if err := writeJobs(ctx, tx, registry, jobs, caller); err != nil {
		return nil, err
	}
	if err := writeWebhooks(ctx, tx, registry, dt, []string{"update"}, stored, caller); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return redact(stored, dt, levels), nil
}

func updateIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, changes core.Changes, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	g, err := grantFor(doctype, caller, core.RightWrite)
	if err != nil {
		return nil, err
	}
	if changes.IsEmpty() {
		return nil, &Error{Kind: ErrNoChanges}
	}
	// FOR NO KEY UPDATE is the lock the UPDATE itself takes: it serializes with other updates and
	// deletes but leaves Link lookups (FOR KEY SHARE) and foreign key checks unblocked. PostgreSQL
	// upgrades it to FOR UPDATE when a unique (key) column changes.
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR NO KEY UPDATE", g)
	if err != nil {
		return nil, err
	}
	submitted := false
	if current.DocStatus != nil {
		if *current.DocStatus == core.Cancelled {
			return nil, invalidStatus(doctype, current, "update")
		}
		submitted = *current.DocStatus == core.Submitted
	}
	if err := checkEditRoles(doctype, current, caller); err != nil {
		return nil, err
	}
	// Before the read-only check, which would name a hidden field.
	if err := checkInput(doctype, levelsOf(doctype, caller), changes.Values, changes.Tables, current); err != nil {
		return nil, err
	}
	if err := checkReadOnly(registry, doctype, changes.Values, changes.Tables, current); err != nil {
		return nil, err
	}
	doc := asDocument(current)
	for k, v := range changes.Values {
		doc.Values[k] = v
	}
	for k, rows := range changes.Tables {
		doc.Tables[k] = cloneRows(rows)
	}
	if submitted {
		if err := checkNotLocked(doctype, current, &doc); err != nil {
			return nil, err
		}
	}
	if doctype.Naming.Kind == core.NamingField {
		if _, ok := changes.Values[doctype.Naming.Field]; ok {
			return nil, &Error{Kind: ErrNaming, Message: fmt.Sprintf(
				"field `%s` gives the document its name and cannot be changed (renames are not supported yet)", doctype.Naming.Field)}
		}
	}
	hookCtx := core.NewHookContext(doctype.Name, caller.Name(), core.OpUpdate).WithJobs(jobs)
	p := &pipeline{registry: registry, doctype: doctype, target: target{
		name: name, expectedVersion: expectedVersion, caller: caller, read: g.read}}
	return updateTail(ctx, c, p, current, doc, hookCtx, submitted, "")
}

// pipeline is what the pipelines after the lock share: the registry, the DocType and the document
// the write is for.
type pipeline struct {
	registry *core.Registry
	doctype  *core.DocType
	target   target
}

// updateTail is the rest of an update, from the Validate hooks on: the stored document current has
// been locked and doc is the document to validate and write (the stored one with the changes merged
// in, or, for a transition, as BeforeTransition left it). With submitted only the allow_on_submit
// fields are written (the others are checked to be unchanged), and with a non-empty state the
// workflow state is written too (a transition).
func updateTail(ctx context.Context, c Queryer, p *pipeline, current *StoredDocument, doc core.Document,
	hookCtx core.HookContext, submitted bool, state string,
) (*StoredDocument, error) {
	registry, doctype := p.registry, p.doctype
	if err := runHooks(registry, doctype, "validate", func(h core.DocTypeHooks) error {
		return h.Validate(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	if doctype.Naming.Kind == core.NamingField {
		field := doctype.Naming.Field
		if v, ok := doc.Values[field]; !ok || !v.Equal(core.Text(current.Name)) {
			return nil, &Error{Kind: ErrNaming, Message: fmt.Sprintf(
				"field `%s` gives the document its name and cannot be changed, also not by a hook (renames are not supported yet)", field)}
		}
	}
	// A hook must not get around the lock either.
	if submitted {
		if err := checkNotLocked(doctype, current, &doc); err != nil {
			return nil, err
		}
	}
	errs := core.Validate(registry, doctype, &doc)
	// A submitted document only writes its allow_on_submit fields (the others are unchanged,
	// checked above).
	columns := columnValues(doctype, &doc, submitted)
	changed := changedTables(current, &doc)
	links, writes, err := resolve(ctx, c, registry, p.target.caller, doctype, columns, changed, current, &errs)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, &Error{Kind: ErrValidation, Validation: errs}
	}
	return writeUpdate(ctx, c, registry, doctype, &p.target, withState(columns, state), links, writes, nil)
}

// withState is columns with the workflow state added: the column is written like a field, as text.
func withState(columns map[string]core.Value, state string) map[string]core.Value {
	if state != "" {
		columns[core.WorkflowColumn] = core.Text(state)
	}
	return columns
}

// checkReadOnly refuses caller input for read_only fields (core.ErrReadOnly, wrapped in
// core.ErrInRow for a row). It runs on what the caller supplied, before the hooks, so a hook may
// still set such a field.
//
// A top-level value is refused when, for an insert (stored is nil), it is not null (null means not
// given), or, for an update (stored is the current document), it differs from the stored value, a
// missing stored value counting as null: sending the stored value back (null when it is null) is
// allowed, null over a stored value is not. A row of a table is refused when it has a non-null
// value for a read_only field of the child DocType, so a client that replaces a table leaves those
// columns out.
func checkReadOnly(registry *core.Registry, doctype *core.DocType, values map[string]core.Value, tables map[string][]core.Row, stored *StoredDocument) error {
	var errs []core.ValidationError
	for _, f := range doctype.ColumnFields() {
		if !f.ReadOnly {
			continue
		}
		value, ok := values[f.Name]
		if !ok {
			continue
		}
		var unchanged bool
		if stored != nil {
			unchanged = stored.Values[f.Name].Equal(value) // a missing stored value is null
		} else {
			unchanged = value.Kind == core.KindNull
		}
		if unchanged {
			continue
		}
		errs = append(errs, core.ValidationError{Kind: core.ErrReadOnly, Field: f.Name})
	}
	for _, tf := range doctype.TableFields() {
		rows, ok := tables[tf.Field.Name]
		child := registry.Get(tf.Target)
		if !ok || child == nil {
			continue
		}
		for i, row := range rows {
			for j := range child.Fields {
				f := &child.Fields[j]
				if !f.ReadOnly {
					continue
				}
				if v, ok := row[f.Name]; ok && v.Kind != core.KindNull {
					errs = append(errs, core.ValidationError{Kind: core.ErrInRow, Field: tf.Field.Name, Row: i + 1,
						Inner: &core.ValidationError{Kind: core.ErrReadOnly, Field: f.Name}})
				}
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &Error{Kind: ErrValidation, Validation: errs}
}

// columnValues are the column values to write for doc: every column field (a value a hook removed
// is cleared), or, with onlyAllowOnSubmit, only the fields marked allow_on_submit.
func columnValues(doctype *core.DocType, doc *core.Document, onlyAllowOnSubmit bool) map[string]core.Value {
	out := map[string]core.Value{}
	for _, f := range doctype.ColumnFields() {
		if onlyAllowOnSubmit && !f.AllowOnSubmit {
			continue
		}
		out[f.Name] = doc.Values[f.Name]
	}
	return out
}

// changedTables are the tables of doc whose rows differ from the stored ones.
func changedTables(current *StoredDocument, doc *core.Document) map[string][]core.Row {
	out := map[string][]core.Row{}
	for field, rows := range doc.Tables {
		if stored, ok := current.Tables[field]; ok && rowsEqual(stored, rows) {
			continue
		}
		out[field] = rows
	}
	return out
}

// checkNotLocked: a submitted document is locked: every column field that is not allow_on_submit
// and every table must equal the stored one in doc. (A key that doc does not have is not a change:
// a missing value is null as on update, and a table key that is absent keeps its rows.)
func checkNotLocked(doctype *core.DocType, current *StoredDocument, doc *core.Document) error {
	locked := func(field string) error {
		return &Error{Kind: ErrFieldLocked, DocType: doctype.Name, Name: current.Name, Field: field}
	}
	for _, f := range doctype.ColumnFields() {
		if f.AllowOnSubmit {
			continue
		}
		if !doc.Values[f.Name].Equal(current.Values[f.Name]) {
			return locked(f.Name)
		}
	}
	for _, tf := range doctype.TableFields() {
		if rows, ok := doc.Tables[tf.Field.Name]; ok {
			if stored, ok := current.Tables[tf.Field.Name]; !ok || !rowsEqual(stored, rows) {
				return locked(tf.Field.Name)
			}
		}
	}
	return nil
}

// writeUpdate writes the update of one document: the columns, the new rows of the tables in writes
// (replacing the stored ones) and, with status, the new docstatus; version is incremented and
// modified_at and modified_by are set in the same statement, which comes first.
func writeUpdate(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, t *target,
	columns map[string]core.Value, links map[string][16]byte, writes []tableWrite, status *core.DocStatus,
) (*StoredDocument, error) {
	actor := t.caller.Name()
	sql := updateSQL(doctype, columns, status)
	var args []any
	for i, field := range sortedKeys(columns) {
		var arg any
		var err error
		// columns holds column fields only, so every key is a field; the fallback is never used.
		if f := doctype.Field(field); f != nil {
			arg, err = sqlParam(f.Type, links, field, columns[field], i+1)
		} else {
			arg, err = bindValue("text", columns[field], i+1)
		}
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	args = append(args, actor, t.name, t.expectedVersion)

	stored, err := scanDocument(doctype, c.QueryRow(ctx, sql, withFormats(args...)...))
	if errors.Is(err, pgx.ErrNoRows) {
		// The row is locked and its version was checked, so it matches; this is only a safeguard.
		return nil, staleOrMissing(ctx, c, doctype, t)
	}
	if err != nil {
		return nil, fromWrite(err, doctype, levelsOf(doctype, t.caller))
	}
	for _, w := range writes {
		sql := fmt.Sprintf("DELETE FROM %s WHERE \"parent_id\" = $1::uuid", QuoteIdent(core.TableName(w.child.Name)))
		if _, err := c.Exec(ctx, sql, [16]byte(stored.ID)); err != nil {
			return nil, wrap(err)
		}
	}
	if err := writeRows(ctx, c, stored.ID, writes); err != nil {
		return nil, err
	}
	if stored.Tables, err = loadTables(ctx, c, registry, doctype, stored.ID); err != nil {
		return nil, err
	}
	return stored, nil
}

// Delete deletes the document with this name, and its table rows, if its current version equals
// expectedVersion. The caller's name is passed to the hooks (not stored, as there is no audit yet,
// and not authenticated); an empty one is ErrInvalidActor, as for Insert and Update. The caller
// needs the delete right and must be able to read the document, with the permission checks of
// Update (unreadable is ErrNotFound, a readable document of somebody else under an if_owner delete
// right is ErrPermissionDenied).
//
// A different version is ErrConflict (nothing is deleted; under REPEATABLE READ or SERIALIZABLE a
// stale writer gets SQLSTATE 40001 as ErrPostgres instead, see Update); an unknown name is
// ErrNotFound. The rows go with the document (the parent_id foreign key is ON DELETE CASCADE). A
// document that other documents still link to cannot be deleted: ErrLinkedDocumentExists names the
// linking DocType (the links are ON DELETE RESTRICT; for a link in a table row, the DocType that
// owns the row) only if the caller has read access to that DocType; otherwise ReferencedBy is nil
// and the error says only that the document is still linked (ADR 0011). A failed delete leaves a
// caller-owned transaction usable (the call ran in a savepoint).
//
// Pipeline. In this order: the stored document and its rows are read with a row lock (FOR UPDATE; an
// unknown name or another version is reported here, before any hook runs); the registered
// BeforeDelete hooks run with the stored document, rows included (a hook error is ErrHook and
// nothing is deleted); the document is deleted.
//
// A submitted document of a submittable DocType cannot be deleted (ErrInvalidDocStatus, checked
// after the lock and before the hooks): only a draft or a cancelled document can.
func Delete(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string, expectedVersion int64, caller core.Caller) error {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return err
	}
	defer end()
	jobs := core.NewEnqueuedJobs()
	deleted, err := deleteIn(ctx, tx, registry, dt, name, expectedVersion, caller, jobs)
	if err != nil {
		return err
	}
	if err := writeJobs(ctx, tx, registry, jobs, caller); err != nil {
		return err
	}
	if err := writeWebhooks(ctx, tx, registry, dt, []string{"delete"}, deleted, caller); err != nil {
		return err
	}
	return commit(ctx, tx)
}

func deleteIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	g, err := grantFor(doctype, caller, core.RightDelete)
	if err != nil {
		return nil, err
	}
	// FOR UPDATE makes concurrent linkers (FOR KEY SHARE) wait, so BeforeDelete hooks do not run
	// for a delete that a new link would then refuse.
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR UPDATE", g)
	if err != nil {
		return nil, err
	}
	if current.DocStatus != nil && *current.DocStatus == core.Submitted {
		return nil, invalidStatus(doctype, current, "delete")
	}
	hookCtx := core.NewHookContext(doctype.Name, caller.Name(), core.OpDelete).WithJobs(jobs)
	// A hook gets its own copy: the document is a read-only view, whatever a hook does to it.
	if err := runHooks(registry, doctype, "before_delete", func(h core.DocTypeHooks) error {
		doc := asDocument(current)
		return h.BeforeDelete(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	tag, err := c.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE \"name\" = $1::text AND \"version\" = $2::bigint",
		QuoteIdent(core.TableName(doctype.Name))), name, expectedVersion)
	if err != nil {
		e := fromDelete(err, doctype.Name, name)
		if e.Kind == ErrLinkedDocumentExists && e.ReferencedBy != nil {
			// Decision 9: name the referencing DocType only to a caller who may read it.
			by := owningDoctype(registry, *e.ReferencedBy)
			if canReadDoctype(registry, by, caller) {
				e.ReferencedBy = &by
			} else {
				e.ReferencedBy = nil
			}
		}
		return nil, e
	}
	if tag.RowsAffected() == 0 {
		return nil, staleOrMissing(ctx, c, doctype, &target{
			name: name, expectedVersion: expectedVersion, caller: caller, read: g.read})
	}
	// The history goes with the document: there is no audit trail yet, and a name can be reused.
	// Always, also for a DocType whose workflow was removed: the rows would otherwise outlive it.
	if _, err := c.Exec(ctx, "DELETE FROM fb_workflow_log WHERE doctype = $1::text AND name = $2::text",
		doctype.Name, name); err != nil {
		return nil, wrap(err)
	}
	// The document as it was, for the webhooks.
	return current, nil
}

// runHooks calls call for every hook set registered for the DocType, in registration order, and
// stops at the first error, which becomes ErrHook naming hook.
func runHooks(registry *core.Registry, doctype *core.DocType, hook string, call func(core.DocTypeHooks) error) error {
	for _, h := range registry.Hooks(doctype.Name) {
		if err := call(h); err != nil {
			message := err.Error()
			var he *core.HookError
			if errors.As(err, &he) {
				message = he.Message()
			}
			return &Error{Kind: ErrHook, DocType: doctype.Name, Hook: hook, Message: message}
		}
	}
	return nil
}

// asDocument is the stored document as the core.Document hooks and validation work on: a deep
// copy, so what a hook does to it never reaches the stored document.
func asDocument(stored *StoredDocument) core.Document {
	return core.Document{Name: stored.Name, Values: cloneValues(stored.Values), Tables: cloneTables(stored.Tables)}
}

func cloneValues(m map[string]core.Value) map[string]core.Value {
	out := make(map[string]core.Value, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneRows(rows []core.Row) []core.Row {
	out := make([]core.Row, len(rows))
	for i, r := range rows {
		out[i] = core.Row(cloneValues(r))
	}
	return out
}

func cloneTables(m map[string][]core.Row) map[string][]core.Row {
	out := make(map[string][]core.Row, len(m))
	for k, rows := range m {
		out[k] = cloneRows(rows)
	}
	return out
}

// cloneDocument is a deep copy of doc, with non-nil maps: the operations work on a copy, so a hook
// that changes its document never changes the caller's (in Rust the document is moved).
func cloneDocument(doc core.Document) core.Document {
	return core.Document{Name: doc.Name, Values: cloneValues(doc.Values), Tables: cloneTables(doc.Tables)}
}

// lockCurrent reads the document with this name and its rows, locking the document row with lock
// (a fixed FOR ... clause) until the transaction ends. In this order: a name that does not exist or
// that the caller cannot read is ErrNotFound (the read scope is part of the SQL, so an unreadable
// row is not even locked); a document the caller may not use g.right on is ErrPermissionDenied; a
// version other than expectedVersion is ErrConflict. Nothing else has been done at that point.
// Writers of the rows take this lock first, so the rows read here cannot change under the caller.
func lockCurrent(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, lock string, g *grant,
) (*StoredDocument, error) {
	scoped := g.read.andSQL(2)
	sql := getSQL(doctype, scoped.sql) + " " + lock
	args := append([]any{name}, scoped.args()...)
	stored, err := scanDocument(doctype, c.QueryRow(ctx, sql, withFormats(args...)...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &Error{Kind: ErrNotFound, DocType: doctype.Name, Name: name}
	}
	if err != nil {
		return nil, err
	}
	if g.rightOwned && stored.Owner != g.rightOwner {
		return nil, permissionDenied(doctype, g.right)
	}
	if stored.Version != expectedVersion {
		return nil, &Error{Kind: ErrConflict, DocType: doctype.Name, Name: name,
			ExpectedVersion: expectedVersion, CurrentVersion: stored.Version}
	}
	if stored.Tables, err = loadTables(ctx, c, registry, doctype, stored.ID); err != nil {
		return nil, err
	}
	return stored, nil
}

// staleOrMissing says why a write that matched no row (name and version) did nothing: the document
// does not exist or is not readable by the caller (ErrNotFound) or has another version
// (ErrConflict). The row was locked and checked before, so this is only a safeguard; it applies the
// read scope too, so even it cannot tell a hidden document from a missing one. The error it returns
// is the result; a failure of its own query is returned instead.
func staleOrMissing(ctx context.Context, c Queryer, doctype *core.DocType, t *target) error {
	scoped := t.read.andSQL(2)
	sql := fmt.Sprintf("SELECT \"version\" FROM %s WHERE \"name\" = $1::text%s",
		QuoteIdent(core.TableName(doctype.Name)), scoped.sql)
	args := append([]any{t.name}, scoped.args()...)
	var current int64
	err := c.QueryRow(ctx, sql, args...).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &Error{Kind: ErrNotFound, DocType: doctype.Name, Name: t.name}
	case err != nil:
		return wrap(err)
	}
	return &Error{Kind: ErrConflict, DocType: doctype.Name, Name: t.name,
		ExpectedVersion: t.expectedVersion, CurrentVersion: current}
}

// fromWrite converts an error of an INSERT or UPDATE on a DocType table: a unique violation
// (SQLSTATE 23505) becomes ErrUniqueViolation. Any other error is returned as it is.
//
// For a unique Link field the server's detail would show the internal id of the target document,
// which users never see elsewhere; the message names the field instead.
//
// A violation on a field the caller cannot read (levels, ADR 0024) names neither the field nor the
// value: a value the caller never saw is not told back. (Only a hook can set such a value, since
// the caller's own input for it is refused earlier.)
func fromWrite(err error, doctype *core.DocType, levels *core.FieldAccess) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		return err
	}
	var column *core.DocField
	if rest, ok := strings.CutPrefix(pg.Detail, "Key ("); ok {
		if name, _, ok := strings.Cut(rest, ")=("); ok {
			column = doctype.Field(name)
		}
	}
	if column != nil && levels != nil && !levels.Shows(column.PermLevel) {
		return &Error{Kind: ErrUniqueViolation, Message: "a value that must be unique already exists"}
	}
	message := describe(pg)
	if column != nil && column.Type.Kind == core.FieldLink {
		message = fmt.Sprintf("a `%s` document with this `%s` already exists (unique field `%s`)",
			doctype.Name, column.Name, column.Name)
	}
	// No driver error is kept: its detail names the column and value that a hidden field must not tell.
	return &Error{Kind: ErrUniqueViolation, Message: message}
}

// fromDelete converts an error of a DELETE on a DocType table: a foreign key violation (SQLSTATE
// 23503, a Link still points at the document) becomes ErrLinkedDocumentExists. The referencing
// DocType comes from the table the server names in the error (the table of the violated foreign
// key, doc_<doctype>), so no further query is needed. The caller maps a child table to the DocType
// that owns it. Any other error is wrapped like a driver error.
func fromDelete(err error, doctype, name string) *Error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		// No driver error is kept (Rust holds none): its table name could tell a hidden DocType.
		e := &Error{Kind: ErrLinkedDocumentExists, DocType: doctype, Name: name}
		if by, ok := strings.CutPrefix(pg.TableName, core.TablePrefix); ok {
			e.ReferencedBy = &by
		}
		return e
	}
	w := wrap(err)
	var e *Error
	if errors.As(w, &e) {
		return e
	}
	return &Error{Kind: ErrPostgres, Message: describe(err), Err: err}
}
