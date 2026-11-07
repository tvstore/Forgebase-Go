package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Documents: what a job or a method may do with documents (the Rust module documents.rs). A job's handle is the
// trusted system context; a method's handle (M9.1, ADR 0019) is the type with the authenticated user as the
// caller.
//
// A job runs as the trusted system context (core.SystemCaller, no permission rules) with the actor
// job:<app>.<job>, which is recorded as owner / modified_by and given to hooks. Every call uses the same
// document functions as the CLI and the HTTP API, so validation, naming and the lifecycle hooks run as
// usual; each write (and each enqueue) is one savepoint inside the surrounding transaction, so a refused
// write leaves it usable and a refused or failed job rolls all of them back. Reads (Get, List) run in a
// savepoint as well: a read that the server refuses rolls back to its savepoint and leaves the transaction
// usable, so a handler that ignores the error neither loses its earlier writes nor fails its next call. Jobs
// enqueued by the hooks of these calls are written in the same transaction.

// DocError is why a [Documents] call failed: the database function's own error, wrapped. In a job, returning
// it makes the job fail as described on [JobError]: a database or connection error is retried, every refusal
// is permanent. In a method, returning it (wrapped or not) answers with the status and code the document API
// gives the same error, and a database or connection error is a generic 500 ([MethodHandler]).
//
// The Error text is meant for the server's log and a job's last_error: it can name a constraint or a server
// message, so do not copy it into a message for users. Err is the *db.Error behind it; the package that
// owns it is internal, so match on the text or on [DocError.IsRetryable] only.
type DocError struct {
	// Err is the database error.
	Err *db.Error
}

func (e *DocError) Error() string { return describeDB(e.Err) }

// Unwrap gives errors.Is and errors.As access to the database error.
func (e *DocError) Unwrap() error { return e.Err }

// IsRetryable reports whether the same call may work if it is tried again later: a database or connection
// error. A refusal (validation, permission, hook veto, not found, conflict, ...) is not retryable.
func (e *DocError) IsRetryable() bool { return !fromDB(e.Err).Permanent }

func docError(err error) error {
	if err == nil {
		return nil
	}
	return &DocError{Err: asDBError(err)}
}

// StoredDocument is a document as a job reads it.
type StoredDocument struct {
	// Name is the document's name.
	Name string
	// Version is the version: pass it to Update, Delete, Submit, Cancel or Amend.
	Version int64
	// DocStatus is the docstatus of a document of a submittable DocType; nil for any other.
	DocStatus *core.DocStatus
	// Document has the values and the table rows. A Link value is the target document's name. (A list does
	// not return table rows.)
	Document core.Document
}

func newStoredDocument(s *db.StoredDocument) *StoredDocument {
	return &StoredDocument{
		Name:      s.Name,
		Version:   s.Version,
		DocStatus: s.DocStatus,
		Document:  core.Document{Name: s.Name, Values: s.Values, Tables: s.Tables},
	}
}

// FilterOp is how a [ListQuery] filter compares a field with a value.
type FilterOp int

// The filter operators.
const (
	// FilterEq is =.
	FilterEq FilterOp = iota + 1
	// FilterNe is <>.
	FilterNe
	// FilterLt is <.
	FilterLt
	// FilterLe is <=.
	FilterLe
	// FilterGt is >.
	FilterGt
	// FilterGe is >=.
	FilterGe
	// FilterLike is a SQL LIKE pattern (% and _); the value is text, and so must be the field.
	FilterLike
)

type listFilter struct {
	field string
	op    FilterOp
	value core.Value
}

type listOrder struct {
	field      string
	descending bool
}

// ListQuery is a query for [Documents.List]: filters (all must match), sort keys and a page size. Build one
// with [NewListQuery] and the methods below, each of which returns a changed copy. The field and value rules
// are those of `forgebase doc list`: the value has the kind of the field and is never Null; an unknown field
// is an error.
//
// name is always the last sort key. The default page is 20 documents and at most 500 are returned; to go
// through more, sort by a field and filter on the last value seen.
type ListQuery struct {
	filters []listFilter
	order   []listOrder
	limit   *uint32
}

// NewListQuery is a query without filters, in name order.
func NewListQuery() ListQuery { return ListQuery{} }

// Filter adds a condition on a field (a field of the DocType, or name, owner, created_at, modified_at,
// modified_by, version).
func (q ListQuery) Filter(field string, op FilterOp, value core.Value) ListQuery {
	q.filters = append(append([]listFilter(nil), q.filters...), listFilter{field, op, value})
	return q
}

// OrderBy adds a sort key; the first one added is the most significant.
func (q ListQuery) OrderBy(field string, descending bool) ListQuery {
	q.order = append(append([]listOrder(nil), q.order...), listOrder{field, descending})
	return q
}

// Limit sets the page size.
func (q ListQuery) Limit(limit uint32) ListQuery {
	q.limit = &limit
	return q
}

func (q ListQuery) toDB() (db.ListQuery, error) {
	out := db.ListQuery{Limit: q.limit}
	for _, f := range q.filters {
		var op db.FilterOp
		switch f.op {
		case FilterEq:
			op = db.Eq(f.value)
		case FilterNe:
			op = db.Ne(f.value)
		case FilterLt:
			op = db.Lt(f.value)
		case FilterLe:
			op = db.Le(f.value)
		case FilterGt:
			op = db.Gt(f.value)
		case FilterGe:
			op = db.Ge(f.value)
		case FilterLike:
			if f.value.Kind != core.KindText {
				return out, docError(&db.Error{Kind: db.ErrInvalidQuery, Message: fmt.Sprintf(
					"the `like` filter on `%s` needs a text value", f.field)})
			}
			op = db.Like(f.value.Text)
		default:
			return out, docError(&db.Error{Kind: db.ErrInvalidQuery, Message: fmt.Sprintf(
				"the filter on `%s` has no operator", f.field)})
		}
		out.Filters = append(out.Filters, db.Filter{Field: f.field, Op: op})
	}
	for _, o := range q.order {
		out.Sort = append(out.Sort, db.Sort{Field: o.field, Descending: o.descending})
	}
	return out, nil
}

// Documents are the documents a job or a method can reach. Get it from [JobContext.Documents] (the system
// context) or [MethodContext.Documents] (the caller). Every method returns an error that is a *[DocError]; see
// there for what a job and a method make of it.
//
// The arguments are those of the matching `forgebase doc` command: the DocType name, the document name, and
// for a change the version that was read (a document changed by someone else in between is a version
// conflict, which is not retryable).
//
// Every call acts as the caller the handle was made with, through the same functions as the HTTP API, so the
// permission rules (if_owner, read rules on Get and List, read-only fields, workflow roles) are the only
// decision. A job's handle is the system context, which no rule restricts; a method's is the user who called,
// so a method can do no more than that user could with the document API.
type Documents struct {
	tx       pgx.Tx
	registry *core.Registry
	caller   core.Caller
}

// inSavepoint runs read in a savepoint of the transaction: a read the server refuses aborts only the
// savepoint.
func (d *Documents) inSavepoint(ctx context.Context, read func(pgx.Tx) error) error {
	sp, err := d.tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := read(sp); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return sp.Commit(ctx)
}

// Get returns the document, or nil when there is none with this name (or the caller may not read it).
func (d *Documents) Get(ctx context.Context, doctype, name string) (*StoredDocument, error) {
	var found *db.StoredDocument
	err := d.inSavepoint(ctx, func(sp pgx.Tx) (err error) {
		found, err = db.Get(ctx, sp, d.registry, doctype, name, d.caller)
		return err
	})
	if err != nil {
		return nil, docError(err)
	}
	if found == nil {
		return nil, nil
	}
	return newStoredDocument(found), nil
}

// List returns a page of documents (only those the caller may read); their table rows are not listed.
func (d *Documents) List(ctx context.Context, doctype string, query ListQuery) ([]*StoredDocument, error) {
	q, err := query.toDB()
	if err != nil {
		return nil, err
	}
	var found []*db.StoredDocument
	err = d.inSavepoint(ctx, func(sp pgx.Tx) (err error) {
		found, err = db.List(ctx, sp, d.registry, doctype, d.caller, q)
		return err
	})
	if err != nil {
		return nil, docError(err)
	}
	out := make([]*StoredDocument, len(found))
	for i, s := range found {
		out[i] = newStoredDocument(s)
	}
	return out, nil
}

// Insert inserts a document and returns it as stored. Its hooks run.
func (d *Documents) Insert(ctx context.Context, doctype string, document core.Document) (*StoredDocument, error) {
	stored, err := db.Insert(ctx, d.tx, d.registry, doctype, document, d.caller)
	return d.result(stored, err)
}

// Update changes a document that is at version, and returns it as stored.
func (d *Documents) Update(ctx context.Context, doctype, name string, version int64, changes core.Changes) (*StoredDocument, error) {
	stored, err := db.Update(ctx, d.tx, d.registry, doctype, name, version, changes, d.caller)
	return d.result(stored, err)
}

// Delete deletes a document that is at version.
func (d *Documents) Delete(ctx context.Context, doctype, name string, version int64) error {
	return docError(db.Delete(ctx, d.tx, d.registry, doctype, name, version, d.caller))
}

// Submit submits a draft of a submittable DocType and returns it.
func (d *Documents) Submit(ctx context.Context, doctype, name string, version int64) (*StoredDocument, error) {
	stored, err := db.Submit(ctx, d.tx, d.registry, doctype, name, version, d.caller)
	return d.result(stored, err)
}

// Cancel cancels a submitted document and returns it.
func (d *Documents) Cancel(ctx context.Context, doctype, name string, version int64) (*StoredDocument, error) {
	stored, err := db.Cancel(ctx, d.tx, d.registry, doctype, name, version, d.caller)
	return d.result(stored, err)
}

// Transition takes the workflow transition action of a document of a DocType with a workflow and returns the
// document as stored. The document must be at version. Like every call of the handle it acts as the handle's
// caller: in a method the roles of the transition and the rights of the user are checked (permission_denied
// when the user may not take it); only a job's handle is the system context, for which they are not. The
// BeforeTransition hooks and the validation run in both.
func (d *Documents) Transition(ctx context.Context, doctype, name, action string, version int64) (*StoredDocument, error) {
	stored, err := db.Transition(ctx, d.tx, d.registry, doctype, name, action, version, d.caller)
	return d.result(stored, err)
}

// Amend amends a cancelled document: a new draft with a copy of its values, which is returned.
func (d *Documents) Amend(ctx context.Context, doctype, name string, version int64) (*StoredDocument, error) {
	stored, err := db.Amend(ctx, d.tx, d.registry, doctype, name, version, d.caller)
	return d.result(stored, err)
}

func (d *Documents) result(stored *db.StoredDocument, err error) (*StoredDocument, error) {
	if err != nil {
		return nil, docError(err)
	}
	return newStoredDocument(stored), nil
}
