package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Workflows (M8.1, ADR 0015): taking a transition, the list of what a caller may take, the history of
// a document and the backfill that migrate runs (the Rust module workflow.rs).
//
// The rules (states, docstatus moves, who may take a transition) are in internal/core
// (core.Workflow, core.TransitionAccess); this file runs them in a transaction. A transition does not
// have a pipeline of its own: after the lock, the checks and the BeforeTransition hooks it continues
// in the update, submit or cancel pipeline (updateTail, submitTail, cancelTail), which writes the new
// state together with the document.

// MaxHistory is the most rows History returns: the newest ones.
const MaxHistory = 500

// Transition takes the transition action of a document of a DocType with a workflow and returns the
// document as stored. The document must be at expectedVersion.
//
// The caller needs the read right on the DocType and the document (ErrNotFound for one it cannot
// read, as everywhere), then one of the transition's roles and the right its docstatus move needs:
// write for draft to draft and submitted to submitted, submit for draft to submitted, cancel for
// submitted to cancelled. The right is decided by the evaluator of internal/core and if_owner works as
// for update, submit and cancel; the role is checked by core.TransitionAccess. The system context and
// an administrator pass both (ErrTransitionDenied otherwise).
//
// Pipeline. One transaction, in this order:
//
//  1. the DocType must have a workflow (ErrNoWorkflow); the document is read and locked (FOR NO KEY
//     UPDATE): an unknown or unreadable name is ErrNotFound, a version other than expectedVersion is
//     ErrConflict;
//  2. the transition is found from the document's state and action (ErrUnknownTransition; a document
//     with no state has none), then the right and the role are checked;
//  3. the BeforeTransition hooks run on the whole document (OpTransition, with HookContext.Transition);
//  4. the pipeline of the docstatus move continues, and writes the new state with the rest: draft to
//     draft is an update of the whole document (Validate hooks with OpTransition, framework
//     validation, Links); draft to submitted is the submit pipeline (OpSubmit); submitted to
//     submitted is an update of a submitted document (only allow_on_submit fields may change,
//     ErrFieldLocked); submitted to cancelled is the cancel pipeline (OpCancel), where only
//     allow_on_submit fields may be changed by BeforeTransition, too. version is incremented and
//     modified_at and modified_by are set;
//  5. a row is added to fb_workflow_log, the jobs the hooks enqueued are written and the webhook
//     events (transition, and submit or cancel when the move is one).
//
// A hook error is ErrHook, and a veto, a failed validation or any other error leaves the document, its
// state, the log and the jobs unchanged: the log row exists if and only if the transition committed. Of
// two concurrent transitions of the same version one wins and the other gets ErrConflict.
func Transition(ctx context.Context, client TxStarter, registry *core.Registry, doctype, name, action string,
	expectedVersion int64, caller core.Caller,
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
	stored, entry, err := transitionIn(ctx, tx, registry, dt, name, action, expectedVersion, caller, jobs)
	if err != nil {
		return nil, scrub(err, dt, levels)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO fb_workflow_log (doctype, name, action, from_state, to_state, actor) "+
		"VALUES ($1::text, $2::text, $3::text, $4::text, $5::text, $6::text)",
		dt.Name, stored.Name, entry.action, entry.from, entry.to, caller.Name()); err != nil {
		return nil, wrap(err)
	}
	if err := writeJobs(ctx, tx, registry, jobs, caller); err != nil {
		return nil, err
	}
	// A transition that submits or cancels the document is also that event.
	events := []string{"transition"}
	if entry.docstatusEvent != "" {
		events = append(events, entry.docstatusEvent)
	}
	if err := writeWebhooks(ctx, tx, registry, dt, events, stored, caller); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return redact(stored, dt, levels), nil
}

// logged is what a transition logs.
type logged struct {
	action, from, to string
	// docstatusEvent is submit or cancel when the transition moved the docstatus to 1 or 2.
	docstatusEvent string
}

func transitionIn(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, name, action string,
	expectedVersion int64, caller core.Caller, jobs *core.EnqueuedJobs,
) (*StoredDocument, *logged, error) {
	if err := checkActor(caller.Name()); err != nil {
		return nil, nil, err
	}
	workflow, err := workflowOf(doctype)
	if err != nil {
		return nil, nil, err
	}
	g, err := readGrant(doctype, caller)
	if err != nil {
		return nil, nil, err
	}
	// The right depends on the transition, which is found from the state in the document, so the
	// document is locked with the read grant first (and a hidden one is not found).
	current, err := lockCurrent(ctx, c, registry, doctype, name, expectedVersion, "FOR NO KEY UPDATE", g)
	if err != nil {
		return nil, nil, err
	}
	var transition *core.Transition
	if current.WorkflowState != "" {
		transition = workflow.Transition(current.WorkflowState, action)
	}
	if transition == nil {
		state := current.WorkflowState
		if state == "" {
			state = "(none)"
		}
		return nil, nil, &Error{Kind: ErrUnknownTransition, DocType: doctype.Name, Name: current.Name, State: state, Action: action}
	}
	denied := func(right core.Right) error {
		return &Error{Kind: ErrTransitionDenied, DocType: doctype.Name, Action: action, Right: right}
	}
	movement, ok := workflow.MoveOf(transition)
	if !ok {
		// A loaded workflow has no such transition; refused rather than half done.
		return nil, nil, denied(core.RightWrite)
	}
	switch core.TransitionAccess(doctype, caller, transition) {
	case core.AccessDenied:
		return nil, nil, denied(movement.Right())
	case core.AccessOwnerOnly:
		if current.Owner != caller.Name() {
			return nil, nil, denied(movement.Right())
		}
	}
	// The state says which docstatus the document must have; a workflow edited after the document was
	// written can disagree. Refused, like submit of a submitted document.
	if doctype.Submittable {
		if from := workflow.State(transition.From); from != nil {
			if err := requireStatus(doctype, current, from.DocStatus, "transition"); err != nil {
				return nil, nil, err
			}
		}
	}

	info := core.NewTransitionInfo(action, transition.From, transition.To)
	doc := asDocument(current)
	hookCtx := core.NewHookContext(doctype.Name, caller.Name(), core.OpTransition).WithTransition(info).WithJobs(jobs)
	if err := runHooks(registry, doctype, "before_transition", func(h core.DocTypeHooks) error {
		return h.BeforeTransition(hookCtx, &doc)
	}); err != nil {
		return nil, nil, err
	}

	p := &pipeline{registry: registry, doctype: doctype, target: target{
		name: name, expectedVersion: expectedVersion, caller: caller, read: g.read}}
	state := transition.To
	opCtx := func(op core.Operation) core.HookContext {
		return core.NewHookContext(doctype.Name, caller.Name(), op).WithJobs(jobs)
	}
	var stored *StoredDocument
	event := ""
	switch movement {
	case core.MoveEdit:
		stored, err = updateTail(ctx, c, p, current, doc, hookCtx, false, state)
	case core.MoveEditSubmitted:
		stored, err = updateTail(ctx, c, p, current, doc, hookCtx, true, state)
	case core.MoveSubmit:
		event = "submit"
		stored, err = submitTail(ctx, c, p, current, doc, opCtx(core.OpSubmit), state)
	case core.MoveCancel:
		event = "cancel"
		// Only allow_on_submit fields may differ, as for an update of a submitted document.
		if err = checkNotLocked(doctype, current, &doc); err == nil {
			stored, err = cancelTail(ctx, c, p, current, doc, opCtx(core.OpCancel), state)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return stored, &logged{action: action, from: transition.From, to: transition.To, docstatusEvent: event}, nil
}

func workflowOf(doctype *core.DocType) (*core.Workflow, error) {
	if doctype.Workflow == nil {
		return nil, &Error{Kind: ErrNoWorkflow, DocType: doctype.Name}
	}
	return doctype.Workflow, nil
}

// AvailableTransitionsResult is what AvailableTransitions finds.
type AvailableTransitionsResult struct {
	// State is the state the document is in; "" for a document that has none.
	State string
	// Transitions are the transitions from that state that the caller may take, in declaration order.
	Transitions []core.Transition
}

// AvailableTransitions are the transitions from the document's state that caller may take now: one of
// the transition's roles and the right of its docstatus move, with if_owner applied to this document
// (the same decision as Transition). The BeforeTransition hooks are not run, so a hook may still
// refuse one of them.
//
// The caller needs the read right: a document it cannot read is ErrNotFound, as for Get. A DocType
// without a workflow is ErrNoWorkflow.
func AvailableTransitions(ctx context.Context, client Client, registry *core.Registry, doctype, name string,
	caller core.Caller,
) (*AvailableTransitionsResult, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	workflow, err := workflowOf(dt)
	if err != nil {
		return nil, err
	}
	stored, err := existing(ctx, client, registry, dt, name, caller)
	if err != nil {
		return nil, err
	}
	out := &AvailableTransitionsResult{State: stored.WorkflowState, Transitions: []core.Transition{}}
	if stored.WorkflowState == "" {
		return out, nil
	}
	for _, t := range workflow.TransitionsFrom(stored.WorkflowState) {
		// A transition whose from-state disagrees with the stored docstatus (a workflow edited after
		// the document was written) is refused by Transition: not offered.
		if dt.Submittable {
			if from := workflow.State(t.From); from != nil && (stored.DocStatus == nil || *stored.DocStatus != from.DocStatus) {
				continue
			}
		}
		switch core.TransitionAccess(dt, caller, t) {
		case core.AccessAll:
		case core.AccessOwnerOnly:
			if stored.Owner != caller.Name() {
				continue
			}
		default:
			continue
		}
		out.Transitions = append(out.Transitions, *t)
	}
	return out, nil
}

// HistoryEntry is one committed transition of a document.
type HistoryEntry struct {
	// Action is the action taken.
	Action string
	// From is the state before and To the state after.
	From, To string
	// Actor is who took it: Caller.Name.
	Actor string
	// At is when: the start time of the transition's transaction.
	At time.Time
}

// History is the transitions a document has taken, oldest first, at most MaxHistory (the newest ones
// when there are more). The caller needs the read right: a document it cannot read is ErrNotFound. A
// DocType without a workflow is ErrNoWorkflow.
//
// The log is deleted with the document, and amend does not copy it: a new draft starts with an empty
// history.
func History(ctx context.Context, client Client, registry *core.Registry, doctype, name string, caller core.Caller) ([]HistoryEntry, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if _, err := workflowOf(dt); err != nil {
		return nil, err
	}
	stored, err := existing(ctx, client, registry, dt, name, caller)
	if err != nil {
		return nil, err
	}
	rows, err := client.Query(ctx, "SELECT action, from_state, to_state, actor, at FROM "+
		"(SELECT id, action, from_state, to_state, actor, at FROM fb_workflow_log "+
		"WHERE doctype = $1::text AND name = $2::text ORDER BY id DESC LIMIT $3::bigint) newest "+
		"ORDER BY id", dt.Name, stored.Name, int64(MaxHistory))
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	entries := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Action, &e.From, &e.To, &e.Actor, &e.At); err != nil {
			return nil, wrap(err)
		}
		e.At = e.At.UTC()
		entries = append(entries, e)
	}
	return entries, wrap(rows.Err())
}

// existing is the document the caller may read, or ErrNotFound.
func existing(ctx context.Context, client Client, registry *core.Registry, doctype *core.DocType, name string, caller core.Caller) (*StoredDocument, error) {
	stored, err := Get(ctx, client, registry, doctype.Name, name, caller)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, &Error{Kind: ErrNotFound, DocType: doctype.Name, Name: name}
	}
	return stored, nil
}

// backfillWorkflowStates gives the documents that have no workflow state one, for every DocType with
// a workflow; run by migrate after the schema sync, so it covers documents that existed before the
// workflow and a workflow that was added later. A document with no state gets the first state with its
// docstatus (a document whose docstatus no state has stays without one). Documents that have a state
// are not touched, so it can run again and changes nothing the second time. It does not change version
// or modified_at: nothing about the document changed, only the framework's bookkeeping.
//
// It returns one warning line per DocType that has documents in a state the workflow no longer
// defines (a renamed or removed state), for example "DocType `order`: 3 documents are in a workflow
// state that no longer exists; no transition applies to them". Nothing is done for those documents.
func backfillWorkflowStates(ctx context.Context, conn *pgx.Conn, registry *core.Registry) ([]string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var warnings []string
	for _, dt := range registry.Iter() {
		if dt.IsChild || dt.Workflow == nil {
			continue
		}
		table := QuoteIdent(core.TableName(dt.Name))
		statuses := []core.DocStatus{core.Draft}
		if dt.Submittable {
			statuses = []core.DocStatus{core.Draft, core.Submitted, core.Cancelled}
		}
		for _, status := range statuses {
			state := dt.Workflow.FirstStateWith(status)
			if state == nil {
				continue
			}
			// docstatus is a constant of the enum, never a value from outside.
			byStatus := ""
			if dt.Submittable {
				byStatus = fmt.Sprintf(" AND \"docstatus\" = %d", int(status))
			}
			if _, err := tx.Exec(ctx, "UPDATE "+table+" SET \"workflow_state\" = $1::text WHERE \"workflow_state\" IS NULL"+byStatus,
				state.Name); err != nil {
				return nil, wrap(err)
			}
		}
		names := make([]string, len(dt.Workflow.States))
		for i, s := range dt.Workflow.States {
			names[i] = s.Name
		}
		var unknown int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE \"workflow_state\" IS NOT NULL "+
			"AND NOT (\"workflow_state\" = ANY($1::text[]))", names).Scan(&unknown); err != nil {
			return nil, wrap(err)
		}
		if unknown > 0 {
			documents := "1 document is"
			if unknown != 1 {
				documents = fmt.Sprintf("%d documents are", unknown)
			}
			warnings = append(warnings, fmt.Sprintf(
				"DocType `%s`: %s in a workflow state that no longer exists; no transition applies to them", dt.Name, documents))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return warnings, nil
}
