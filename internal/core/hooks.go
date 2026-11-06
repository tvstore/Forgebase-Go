package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

// Lifecycle hooks: app code that runs at fixed points of a document operation.
//
// A hook set is a type that implements DocTypeHooks and is registered for one DocType with
// Registry.AddHooks, explicitly at startup (ADR 0006, ADR 0009, ADR 0026). The framework provides
// BaseHooks, whose methods do nothing: a hook set embeds it and overrides only the points it
// needs. This file performs no I/O: internal/db calls the hooks from its document operations.
//
// # Pipeline
//
// Hooks run inside the operation's database transaction, in the order below, and for each point in
// registration order (all hook sets of the DocType: the first registered runs first). Steps 1 and
// 3 mean that hooks run before the framework's own validation: a hook may see input that is
// invalid (unknown fields, wrong value kinds, missing required fields) and must be defensive. The
// framework validates the document again after the hooks, so a hook cannot leave an invalid
// document behind.
//
//	insert:     BeforeInsert, Validate, framework validation, Link resolution, naming, write
//	update:     lock and load the stored document (not found and version conflict are reported
//	            here, before any hook), merge the changes, Validate, framework validation of the
//	            whole document, Link resolution, write
//	delete:     lock and load the stored document (not found and version conflict as above),
//	            docstatus check, BeforeDelete, delete
//	submit:     lock and load the stored document (as above), docstatus check (draft only),
//	            Validate, BeforeSubmit, framework validation of the whole document, Link
//	            resolution, write with docstatus 1
//	cancel:     lock and load the stored document (as above), docstatus check (submitted only),
//	            BeforeCancel, write with docstatus 2
//	amend:      lock and load the cancelled document (as above), docstatus check (cancelled only,
//	            not amended yet), then the insert pipeline on a copy of it: BeforeInsert, Validate,
//	            framework validation, Link resolution, naming (the amended name), write as a draft
//	transition: lock and load the stored document (as above), find the transition from its state,
//	            check the right and the role, BeforeTransition, then the pipeline of the docstatus
//	            move (below), write the new state and the log row
//
// A transition (M8.1, ADR 0015) exists only for a DocType with a [workflow]. After BeforeTransition
// (with OpTransition; the hook sees the transition in HookContext.Transition) it continues like the
// operation it stands for:
//
//	draft to draft:         Validate (OpTransition), framework validation of the whole document,
//	                        Link resolution, write
//	draft to submitted:     the submit pipeline: Validate and BeforeSubmit (OpSubmit), framework
//	                        validation, Link resolution, write with docstatus 1
//	submitted to submitted: like an update of a submitted document: Validate (OpTransition), only
//	                        allow_on_submit fields may differ, framework validation, write
//	submitted to cancelled: the cancel pipeline: BeforeCancel (OpCancel), write with docstatus 2
//
// On a DocType with a workflow plain submit and cancel are refused, so the submit and cancel hooks
// run for transitions only. In a submitted-to-submitted or submitted-to-cancelled transition
// BeforeTransition may change allow_on_submit fields only; any other change is refused with
// DbError FieldLocked.
//
// Permissions come first (M3.2, ADR 0011): internal/db checks the caller's rights before the first
// step, and in the "lock and load" steps a document the caller cannot read is "not found", so a
// refused operation runs no hook. Hooks therefore see operations that are allowed; they are not a
// place to check permissions.
//
// submit, cancel and amend exist only for DocTypes with submittable = true. A hook sees which
// operation it runs for in HookContext.Operation: Validate runs for OpInsert, OpUpdate, OpSubmit
// and OpAmend, and BeforeInsert for OpInsert and OpAmend (an amended document is a new draft, so
// the hooks that every new document goes through run for it; check the operation where the two
// must differ).
//
// # Rules
//
//   - No database access. A hook gets the document and a HookContext, nothing else. Hooks that read
//     other documents are not supported yet (ADR 0009).
//   - Effects after commit go through the outbox. A hook may call HookContext.Enqueue to ask for a
//     background job. The job is written in the same transaction as the document change, after all
//     hooks have run, so it exists if and only if the change is committed (ADR 0013).
//   - An error aborts the operation. A hook returns an error to reject the operation; the
//     transaction (a savepoint inside a caller-owned transaction) is rolled back and nothing is
//     persisted. internal/db reports it as a hook error. The error text is shown to the caller.
//   - A panic is not caught. A hook is app code: a panic aborts the operation like any other panic
//     in the caller (the transaction is rolled back).
//   - Child DocTypes have no hooks. The rows of a child table are part of the parent document that
//     hooks of the parent DocType receive (doc.Tables).
//   - Hooks are synchronous and must be quick: they run while the document's row is locked. A hook
//     set is shared by every request, so it must be safe for concurrent use.

// MaxJobPayloadBytes is the largest payload of a job: 64 KiB of serialized JSON. It applies to every
// way of enqueueing a job.
const MaxJobPayloadBytes = 64 * 1024

// CheckJobPayload checks a job payload (any value encoding/json can marshal): at most
// MaxJobPayloadBytes of serialized JSON, and no string with a NUL character (PostgreSQL jsonb cannot
// store one). It returns the reason when refused, "" when the payload is fine.
func CheckJobPayload(payload any) string {
	_, reason := marshalPayload(payload)
	return reason
}

// marshalPayload serializes a payload to compact JSON (HTML escaping off, like serde_json) and
// checks it. Note that encoding/json writes U+2028 and U+2029 as six-byte escapes where serde_json
// writes them as three raw bytes, so a payload full of those characters counts a little larger near
// the 64 KiB limit than in Rust.
func marshalPayload(payload any) (json.RawMessage, string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	size := -1
	if err := enc.Encode(payload); err == nil {
		size = buf.Len() - 1 // Encode appends a newline
	}
	if size < 0 || size > MaxJobPayloadBytes {
		return nil, fmt.Sprintf("the job payload is larger than %d bytes of JSON", MaxJobPayloadBytes)
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err == nil && hasNul(generic) {
		return nil, "the job payload contains a NUL character"
	}
	return json.RawMessage(append([]byte(nil), raw...)), ""
}

func hasNul(v any) bool {
	switch x := v.(type) {
	case string:
		return containsNul(x)
	case []any:
		for _, item := range x {
			if hasNul(item) {
				return true
			}
		}
	case map[string]any:
		for k, item := range x {
			if containsNul(k) || hasNul(item) {
				return true
			}
		}
	}
	return false
}

func containsNul(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// EnqueuedJob is a job that a hook asked for, waiting to be written to the job table with the
// document change.
type EnqueuedJob struct {
	name    string
	payload any
}

// Name is the full job name, <app>.<job>.
func (j EnqueuedJob) Name() string { return j.name }

// Payload is the payload as the JSON text it was serialized to when it was enqueued (a
// json.RawMessage); unmarshal it into what the job handler expects.
func (j EnqueuedJob) Payload() any { return j.payload }

// EnqueuedJobs collects the jobs that the hooks of one operation enqueue. internal/db makes one per
// operation, gives it to every hook call through HookContext.WithJobs and, after the hooks and
// before the commit, writes what it holds to the job table in the operation's transaction. An app's
// unit test can make one to see what a hook enqueued. It is safe for concurrent use.
type EnqueuedJobs struct {
	mu   sync.Mutex
	jobs []EnqueuedJob
}

// NewEnqueuedJobs is an empty collector.
func NewEnqueuedJobs() *EnqueuedJobs { return &EnqueuedJobs{} }

// Take takes the collected jobs, in the order they were enqueued, and leaves the collector empty.
func (e *EnqueuedJobs) Take() []EnqueuedJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	jobs := e.jobs
	e.jobs = nil
	return jobs
}

func (e *EnqueuedJobs) push(job EnqueuedJob) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobs = append(e.jobs, job)
}

// Operation is the operation a hook runs for.
type Operation int

const (
	// OpInsert: a new document is being inserted.
	OpInsert Operation = iota + 1
	// OpUpdate: an existing document is being updated.
	OpUpdate
	// OpDelete: an existing document is being deleted.
	OpDelete
	// OpSubmit: a draft of a submittable DocType is being submitted.
	OpSubmit
	// OpCancel: a submitted document is being cancelled.
	OpCancel
	// OpAmend: a cancelled document is being amended: a new draft is created from a copy of it.
	OpAmend
	// OpTransition: a document of a DocType with a workflow is taking a transition (M8.1, ADR
	// 0015). The BeforeTransition hook runs for it, and so do the Validate hooks of a move that
	// does not submit or cancel; HookContext.Transition says which transition. The hooks of a move
	// that submits or cancels run with OpSubmit or OpCancel.
	OpTransition
)

// TransitionInfo is the transition a hook runs for, see HookContext.Transition.
type TransitionInfo struct {
	// Action is the action, as written in the workflow (the label of the button).
	Action string
	// From is the state the document is in.
	From string
	// To is the state the document will be in.
	To string
}

// NewTransitionInfo is the transition action from state from to state to.
func NewTransitionInfo(action, from, to string) *TransitionInfo {
	return &TransitionInfo{Action: action, From: from, To: to}
}

// HookContext is what a hook is told about the operation, besides the document. Build one with
// NewHookContext (for example in a test that calls a hook directly); the fields are exported to
// read. It is a small value: copy it freely; a copy shares the collector of the jobs the hooks
// enqueue (see Enqueue).
type HookContext struct {
	// DocType is the name of the DocType.
	DocType string
	// Actor is the name of the caller of the operation (Caller.Name): the username of a user, or
	// the actor of the trusted system context. Set for every operation, including OpDelete; insert
	// and update also store it, delete only gives it to the hooks (there is no audit trail yet). By
	// the time a hook runs the caller's permissions have been checked (ADR 0011); hooks do not see
	// the roles.
	Actor string
	// Operation is the operation.
	Operation Operation
	// Transition is the transition, for OpTransition; nil for every other operation.
	Transition *TransitionInfo
	jobs       *EnqueuedJobs
}

// NewHookContext is a context for operation on doctype by actor. The framework builds these for the
// hooks it calls; an app needs this only to call a hook directly in a test.
func NewHookContext(doctype, actor string, operation Operation) HookContext {
	return HookContext{DocType: doctype, Actor: actor, Operation: operation}
}

// WithTransition is this context for the transition t. The framework sets it for OpTransition; an
// app's unit test of a BeforeTransition hook uses it.
func (c HookContext) WithTransition(t *TransitionInfo) HookContext {
	c.Transition = t
	return c
}

// WithJobs is this context with a collector for the jobs a hook enqueues. The framework gives
// every hook call of an operation the same collector; an app's unit test uses it to see what a hook
// enqueued.
func (c HookContext) WithJobs(jobs *EnqueuedJobs) HookContext {
	c.jobs = jobs
	return c
}

// Equal compares the contexts: the same doctype, actor, operation and transition (by value), and
// the same collector (by identity).
func (c HookContext) Equal(o HookContext) bool {
	if c.DocType != o.DocType || c.Actor != o.Actor || c.Operation != o.Operation || c.jobs != o.jobs {
		return false
	}
	if c.Transition == nil || o.Transition == nil {
		return c.Transition == o.Transition
	}
	return *c.Transition == *o.Transition
}

// The payload is serialized to JSON when Enqueue is called, so changing the value afterwards has
// no effect on the job (in Rust the payload is moved).
//
// Enqueue asks for a background job name (<app>.<job>) with payload. The job is written to the job
// table in the same transaction as the document change, after all hooks have run, so it exists if
// and only if the change is committed (the transactional outbox, ADR 0013). An unknown job name
// fails the whole operation when the job is written. Never put secrets in a payload: it is stored
// in clear text.
//
// It fails if the payload is larger than MaxJobPayloadBytes bytes of JSON, or when this context has
// no collector (a context made by NewHookContext in a test: add one with WithJobs).
func (c HookContext) Enqueue(name string, payload any) error {
	if c.jobs == nil {
		return NewHookError("jobs cannot be enqueued here: this HookContext has no job collector (use HookContext::with_jobs in a test)")
	}
	raw, reason := marshalPayload(payload)
	if reason != "" {
		return NewHookError(reason)
	}
	c.jobs.push(EnqueuedJob{name: name, payload: raw})
	return nil
}

// HookError is what a hook returns to reject an operation; any error rejects it, this type is only
// a convenient one. The message is shown to the caller.
type HookError struct {
	message string
}

// NewHookError is an error with the given message.
func NewHookError(message string) *HookError { return &HookError{message: message} }

// Message is the message.
func (e *HookError) Message() string { return e.message }

func (e *HookError) Error() string { return e.message }

// DocTypeHooks are the lifecycle hooks of one DocType; see the comment at the top of this file for
// the pipeline order and the rules every hook follows. Embed BaseHooks to get a method for every
// point that does nothing, and override the points you need.
//
// A hook that returns an error rejects the whole operation; nothing is persisted.
type DocTypeHooks interface {
	// BeforeInsert runs first when a document is inserted, on the input document, which may be
	// modified. The document is not validated yet.
	BeforeInsert(ctx HookContext, doc *Document) error

	// Validate runs on insert (after BeforeInsert), on update, on submit (before BeforeSubmit) and
	// on amend (after BeforeInsert). The document may be modified. It is not validated yet by the
	// framework.
	//
	// On update doc is the whole document: the stored values with the changes applied, and the rows
	// of every table (the stored ones for a table the changes do not mention). doc.Name is the
	// stored name; changing the naming field is rejected afterwards. A value a hook removes from
	// doc.Values is cleared (as on insert, an absent value is null); to remove all rows of a table
	// set an empty list, because a table key that is removed from doc.Tables keeps the stored rows.
	// (The exception: for a required Table field the framework validation then reports Required,
	// since the document has no rows for it.)
	//
	// Changing doc.Name is ignored on update (the stored name is kept; the naming field is checked
	// as said above). On insert the name is decided by the naming rule afterwards, and a doc.Name
	// that does not agree with it is rejected with a naming error.
	Validate(ctx HookContext, doc *Document) error

	// BeforeDelete runs before a document is deleted. doc is a read-only view of the stored
	// document, including its table rows: do not change it. (Rust passes a shared reference; Go
	// cannot enforce that, so the pipeline ignores any change a hook makes to doc.)
	BeforeDelete(ctx HookContext, doc *Document) error

	// BeforeSubmit runs when a draft is submitted, after Validate. doc is the whole stored
	// document (values and the rows of every table) and may be modified, like in Validate (a
	// submit hook typically finalizes values, for example a total); the framework validates the
	// result afterwards, and the changes are written together with the new docstatus. Changing
	// doc.Name is ignored.
	BeforeSubmit(ctx HookContext, doc *Document) error

	// BeforeCancel runs before a submitted document is cancelled. doc is a read-only view of the
	// stored document, including its table rows (like BeforeDelete: a veto, not a modification;
	// the pipeline ignores any change a hook makes to doc).
	BeforeCancel(ctx HookContext, doc *Document) error

	// BeforeTransition runs when a document of a DocType with a workflow takes a transition (M8.1,
	// ADR 0015), before the Validate, BeforeSubmit and BeforeCancel hooks of the move. ctx has
	// OpTransition and HookContext.Transition (the action and the two states); the caller's right
	// and role were checked before. doc is the whole stored document and may be modified: for a
	// draft-to-draft and a draft-to-submitted transition the changes are validated and written with
	// the new state; for a transition from a submitted state only allow_on_submit fields may change
	// (any other change is refused with a field-locked error). Changing doc.Name is ignored.
	//
	// This is where a condition on a transition is written, in Go (the workflow file has no
	// expressions): return an error to veto it. Like every hook it is synchronous, has no database
	// access and may enqueue jobs (ADR 0013): a job exists if and only if the transition committed,
	// which is how an app notifies someone about it.
	BeforeTransition(ctx HookContext, doc *Document) error
}

// BaseHooks implements DocTypeHooks with methods that do nothing. Embed it in a hook set and
// override the points the set needs.
type BaseHooks struct{}

// BeforeInsert does nothing.
func (BaseHooks) BeforeInsert(HookContext, *Document) error { return nil }

// Validate does nothing.
func (BaseHooks) Validate(HookContext, *Document) error { return nil }

// BeforeDelete does nothing.
func (BaseHooks) BeforeDelete(HookContext, *Document) error { return nil }

// BeforeSubmit does nothing.
func (BaseHooks) BeforeSubmit(HookContext, *Document) error { return nil }

// BeforeCancel does nothing.
func (BaseHooks) BeforeCancel(HookContext, *Document) error { return nil }

// BeforeTransition does nothing.
func (BaseHooks) BeforeTransition(HookContext, *Document) error { return nil }
