package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Docstatus: Submit, Cancel and Amend for DocTypes with Submittable = true (M2.4). Part of the Rust
// module document.rs.

// requireSubmittable: the DocType must be submittable for operation (submit, cancel or amend).
func requireSubmittable(doctype *core.DocType, operation string) error {
	if doctype.Submittable {
		return nil
	}
	return &Error{Kind: ErrNotSubmittable, DocType: doctype.Name, Operation: operation}
}

func invalidStatus(doctype *core.DocType, current *StoredDocument, operation string) error {
	if current.DocStatus != nil {
		return &Error{Kind: ErrInvalidDocStatus, DocType: doctype.Name, Name: current.Name,
			DocStatus: *current.DocStatus, Operation: operation}
	}
	return &Error{Kind: ErrNotSubmittable, DocType: doctype.Name, Operation: operation}
}

// requireStatus: the stored document must have docstatus needed for operation.
func requireStatus(doctype *core.DocType, current *StoredDocument, needed core.DocStatus, operation string) error {
	if current.DocStatus != nil && *current.DocStatus == needed {
		return nil
	}
	return invalidStatus(doctype, current, operation)
}

// Submit submits a draft of a submittable DocType: its docstatus becomes Submitted and the
// document is locked (see Update). Returns the document as stored.
//
// doctype must have Submittable = true (ErrNotSubmittable) and the document must be a draft
// (ErrInvalidDocStatus). A DocType with a workflow refuses it first, before anything is looked up
// (ErrWorkflowRequired): use a transition. The caller needs the submit right and must be able to
// read the document; expectedVersion and the permission checks work as for Update; version is
// incremented, modified_at and modified_by are set.
//
// Pipeline. In this order: the stored document and its rows are read with a row lock (FOR NO KEY
// UPDATE, the lock an update takes, which leaves Link lookups of other documents unblocked; an
// unknown name and another version are reported here, before any hook runs); the docstatus is
// checked; the registered Validate hooks run on the whole stored document (with core.OpSubmit),
// then the BeforeSubmit hooks, and both may modify it; the framework validates the whole result;
// Links are resolved; the column values and the changed tables are written like an update does (so
// a value a hook changed is persisted), with docstatus 1 (Links already stored are kept without the
// caller's read check on the target, as for Update; only values a hook changed are checked). A hook
// error is ErrHook and nothing is changed. The transaction and optimistic concurrency are those of
// Update.
func Submit(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string, expectedVersion int64, caller core.Caller) (*StoredDocument, error) {
	return docstatusOperation(ctx, client, registry, doctype, name, expectedVersion, caller, "submit", submitIn)
}

// Cancel cancels a submitted document: its docstatus becomes Cancelled. It can then be deleted or
// amended (see Amend); it cannot be updated. Returns the document as stored.
//
// Documents that link to this one are not touched (no automatic cancellation of linked documents).
// A DocType with a workflow refuses it first, before anything is looked up (ErrWorkflowRequired):
// use a transition. doctype must be submittable (ErrNotSubmittable) and the document submitted
// (ErrInvalidDocStatus). The caller needs the cancel right and must be able to read the document;
// expectedVersion and the permission checks work as for Update; version is incremented,
// modified_at and modified_by are set.
//
// Pipeline. In this order: the stored document and its rows are read with a row lock (FOR NO KEY
// UPDATE, as for Submit; an unknown name and another version are reported here); the docstatus is
// checked; the registered BeforeCancel hooks run with the stored document, rows included
// (read-only; a hook error is ErrHook and nothing is changed); docstatus 2 is written. There is no
// validation: nothing else changes.
func Cancel(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string, expectedVersion int64, caller core.Caller) (*StoredDocument, error) {
	return docstatusOperation(ctx, client, registry, doctype, name, expectedVersion, caller, "cancel", cancelIn)
}

type docstatusStep func(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs) (*StoredDocument, error)

// docstatusOperation is the shell of Submit and Cancel: the transaction, the outbox, the webhook
// event named like the operation, and the redaction of the result.
func docstatusOperation(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string,
	expectedVersion int64, caller core.Caller, event string, step docstatusStep,
) (*StoredDocument, error) {
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
	stored, err := step(ctx, tx, registry, dt, name, expectedVersion, caller, jobs)
	if err != nil {
		return nil, scrub(err, dt, levels)
	}
	if err := writeJobs(ctx, tx, registry, jobs, caller); err != nil {
		return nil, err
	}
	if err := writeWebhooks(ctx, tx, registry, dt, []string{event}, stored, caller); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return redact(stored, dt, levels), nil
}

func submitIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	if err := requireNoWorkflow(doctype, "submit"); err != nil {
		return nil, err
	}
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	if err := requireSubmittable(doctype, "submit"); err != nil {
		return nil, err
	}
	g, err := grantFor(doctype, caller, core.RightSubmit)
	if err != nil {
		return nil, err
	}
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR NO KEY UPDATE", g)
	if err != nil {
		return nil, err
	}
	if err := requireStatus(doctype, current, core.Draft, "submit"); err != nil {
		return nil, err
	}
	doc := asDocument(current)
	hookCtx := core.NewHookContext(doctype.Name, caller.Name(), core.OpSubmit).WithJobs(jobs)
	p := &pipeline{registry: registry, doctype: doctype, target: target{
		name: name, expectedVersion: expectedVersion, caller: caller, read: g.read}}
	return submitTail(ctx, c, p, current, doc, hookCtx, "")
}

// submitTail is the rest of a submit, from the Validate hooks on: doc is the stored draft, or, for
// a transition, as BeforeTransition left it. Writes the values, the changed tables, docstatus 1 and,
// with a non-empty state, the workflow state.
func submitTail(ctx context.Context, c Queryer, p *pipeline, current *StoredDocument, doc core.Document,
	hookCtx core.HookContext, state string,
) (*StoredDocument, error) {
	registry, doctype := p.registry, p.doctype
	if err := runHooks(registry, doctype, "validate", func(h core.DocTypeHooks) error {
		return h.Validate(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	if err := runHooks(registry, doctype, "before_submit", func(h core.DocTypeHooks) error {
		return h.BeforeSubmit(hookCtx, &doc)
	}); err != nil {
		return nil, err
	}
	errs := core.Validate(registry, doctype, &doc)
	columns := columnValues(doctype, &doc, false)
	changed := changedTables(current, &doc)
	links, writes, err := resolve(ctx, c, registry, p.target.caller, doctype, columns, changed, current, &errs)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, &Error{Kind: ErrValidation, Validation: errs}
	}
	status := core.Submitted
	return writeUpdate(ctx, c, registry, doctype, &p.target, withState(columns, state), links, writes, &status)
}

func cancelIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	if err := requireNoWorkflow(doctype, "cancel"); err != nil {
		return nil, err
	}
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	if err := requireSubmittable(doctype, "cancel"); err != nil {
		return nil, err
	}
	g, err := grantFor(doctype, caller, core.RightCancel)
	if err != nil {
		return nil, err
	}
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR NO KEY UPDATE", g)
	if err != nil {
		return nil, err
	}
	if err := requireStatus(doctype, current, core.Submitted, "cancel"); err != nil {
		return nil, err
	}
	doc := asDocument(current)
	hookCtx := core.NewHookContext(doctype.Name, caller.Name(), core.OpCancel).WithJobs(jobs)
	p := &pipeline{registry: registry, doctype: doctype, target: target{
		name: name, expectedVersion: expectedVersion, caller: caller, read: g.read}}
	return cancelTail(ctx, c, p, current, doc, hookCtx, "")
}

// cancelTail is the rest of a cancel, from the BeforeCancel hooks on: doc is the stored document,
// or, for a transition, as BeforeTransition left it (the caller has refused a change to a locked
// field). Writes docstatus 2 and, with a non-empty state, the workflow state; a change to an
// allow_on_submit field is validated and written too. There is no other validation: nothing else
// changes.
func cancelTail(ctx context.Context, c Queryer, p *pipeline, current *StoredDocument, doc core.Document,
	hookCtx core.HookContext, state string,
) (*StoredDocument, error) {
	registry, doctype := p.registry, p.doctype
	// A hook gets its own copy: the document is a read-only view, whatever a hook does to it.
	if err := runHooks(registry, doctype, "before_cancel", func(h core.DocTypeHooks) error {
		view := cloneDocument(doc)
		return h.BeforeCancel(hookCtx, &view)
	}); err != nil {
		return nil, err
	}
	// Only a transition can have changed anything (an allow_on_submit field, see the caller).
	columns := map[string]core.Value{}
	for field, value := range columnValues(doctype, &doc, true) {
		if !current.Values[field].Equal(value) {
			columns[field] = value
		}
	}
	var errs []core.ValidationError
	if len(columns) > 0 {
		errs = core.Validate(registry, doctype, &doc)
	}
	links, writes, err := resolve(ctx, c, registry, p.target.caller, doctype, columns, nil, current, &errs)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, &Error{Kind: ErrValidation, Validation: errs}
	}
	status := core.Cancelled
	return writeUpdate(ctx, c, registry, doctype, &p.target, withState(columns, state), links, writes, &status)
}

// Amend amends a cancelled document: creates a new draft with a copy of its values and table rows
// and returns it as stored. The cancelled document is not changed.
//
// doctype must be submittable (ErrNotSubmittable) and the document cancelled (ErrInvalidDocStatus).
// expectedVersion is the cancelled document's version, as for Delete (it keeps a stale caller from
// amending a document it has not read); the caller becomes the owner and modified_by of the new
// draft. The caller needs the amend right and must be able to read the cancelled document (an
// unreadable one is ErrNotFound); it does not need the create right.
//
// Name. The new document is named <root>-<n>, not by the naming rule of the DocType (no series
// number is taken). If the cancelled document is itself an amendment (it has AmendedFrom) and its
// name is <root>-<k>, the new name is <root>-<k+1>; otherwise it is <name>-1. So X is amended as
// X-1, and X-1 (once cancelled) as X-2. The new document's AmendedFrom is the cancelled document's
// name.
//
// A cancelled document can be amended once: if the document with the new name already exists and
// amends it, the call fails with ErrAlreadyAmended (delete the amendment to amend again). Concurrent
// amendments of the same document serialize on its row lock (FOR NO KEY UPDATE).
//
// Edge case: stale ErrAlreadyAmended. amended_from has no foreign key and the check looks only at the
// document with the new name. If a cancelled document in a chain is deleted and re-created by
// amending its predecessor, a later amend can name an amendment of the deleted document: X, X-1,
// X-2; delete X-1; amend X again (a new X-1), submit and cancel it; amending it then fails with
// AmendedBy "X-2". Delete X-2 to amend again. There is no code change for this.
//
// Edge case: unique fields. The copy repeats every value, and the cancelled original still holds
// them, so a DocType with a unique field fails with ErrUniqueViolation unless a BeforeInsert or
// Validate hook checks ctx.Operation == core.OpAmend and changes the value (the original cannot be
// deleted or updated first: amend needs it, and a cancelled document cannot be updated).
//
// Pipeline. The cancelled document is read and locked, as for Submit; then the insert pipeline of
// Insert runs on the copy: the BeforeInsert and Validate hooks run with core.OpAmend (the document
// they see has the new name in Name; a hook that changes it is refused with ErrNaming), the
// framework validates it, Links are resolved (a target that was deleted since is a validation
// error), the new name is used and the draft is written. A hook error is ErrHook and nothing is
// changed. The webhook event of the new draft is insert.
func Amend(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name string, expectedVersion int64, caller core.Caller) (*StoredDocument, error) {
	// The amendment is a new document: its event is insert.
	return docstatusOperation(ctx, client, registry, doctype, name, expectedVersion, caller, "insert", amendIn)
}

func amendIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, error) {
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	if err := requireSubmittable(doctype, "amend"); err != nil {
		return nil, err
	}
	g, err := grantFor(doctype, caller, core.RightAmend)
	if err != nil {
		return nil, err
	}
	// The amendment is a new document with a new name: see refuseRestrictedCreate.
	if err := refuseRestrictedCreate(doctype, caller, core.RightAmend); err != nil {
		return nil, err
	}
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR NO KEY UPDATE", g)
	if err != nil {
		return nil, err
	}
	if err := requireStatus(doctype, current, core.Cancelled, "amend"); err != nil {
		return nil, err
	}
	newName := amendedName(current.Name, current.AmendedFrom != "")
	var amendedFrom *string
	err = c.QueryRow(ctx, fmt.Sprintf("SELECT \"amended_from\" FROM %s WHERE \"name\" = $1::text",
		QuoteIdent(core.TableName(doctype.Name))), newName).Scan(&amendedFrom)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, wrap(err)
	default:
		if amendedFrom != nil && *amendedFrom == current.Name {
			return nil, &Error{Kind: ErrAlreadyAmended, DocType: doctype.Name, Name: current.Name, AmendedBy: newName}
		}
		// Some other document has the name: the insert below reports the unique violation.
	}
	doc := asDocument(current)
	doc.Name = newName
	return insertIn(ctx, c, registry, doctype, doc, caller, &amendOf{from: current.Name, name: newName}, jobs)
}
