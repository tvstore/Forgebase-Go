package db_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Workflows (M8.1, ADR 0015) against a real PostgreSQL: the authorization of transitions, the pipelines of
// the four docstatus moves, atomicity with the log and the jobs, what changes for a DocType with a
// workflow, the schema and the backfill of migrate. Ported from crates/forgebase-db/tests/workflow.rs.

const wfOrder = `
name = "order"
label = "Order"
submittable = true

[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "total"
type = "int"
[[fields]]
name = "remarks"
type = "text"
allow_on_submit = true

# Clerk writes anyone's drafts; Manager does everything; Auditor has the
# submit and cancel rights without being a role of any transition but Void; Reviewer is a role
# of Approve but has no right to submit; Self Service may change only its own documents; Spy
# reads only its own documents.
[[permissions]]
role = "Clerk"
read = true
create = true
write = true
[[permissions]]
role = "Manager"
read = true
create = true
write = true
delete = true
submit = true
cancel = true
amend = true
[[permissions]]
role = "Auditor"
read = true
submit = true
cancel = true
[[permissions]]
role = "Reviewer"
read = true
write = true
[[permissions]]
role = "Self Service"
read = true
[[permissions]]
role = "Self Service"
create = true
write = true
if_owner = true
[[permissions]]
role = "Spy"
read = true
write = true
submit = true
if_owner = true

[workflow]
[[workflow.states]]
name = "Draft"
docstatus = 0
[[workflow.states]]
name = "Pending"
docstatus = 0
style = "warning"
edit_roles = ["Manager"]
[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"
[[workflow.states]]
name = "Archived"
docstatus = 1
[[workflow.states]]
name = "Cancelled"
docstatus = 2
style = "danger"

[[workflow.transitions]]
action = "Send"
from = "Draft"
to = "Pending"
roles = ["Clerk", "Self Service", "Spy"]
[[workflow.transitions]]
action = "Reject"
from = "Pending"
to = "Draft"
roles = ["Manager"]
[[workflow.transitions]]
action = "Approve"
from = "Pending"
to = "Approved"
roles = ["Manager", "Reviewer", "Spy"]
[[workflow.transitions]]
action = "Archive"
from = "Approved"
to = "Archived"
roles = ["Manager", "Reviewer"]
[[workflow.transitions]]
action = "Cancel"
from = "Approved"
to = "Cancelled"
roles = ["Manager"]
[[workflow.transitions]]
action = "Void"
from = "Approved"
to = "Cancelled"
roles = ["Auditor"]
`

// wfOrderWithoutWorkflow is the same DocType before it had a workflow (for the backfill): no [workflow],
// same rules.
func wfOrderWithoutWorkflow() string {
	return strings.SplitN(wfOrder, "[workflow]", 2)[0]
}

// wfRecorder records <hook>:<operation>[:<action>:<from>><to>] for the hooks of a transition; fails or
// changes the document where it is told to.
type wfRecorder struct {
	core.BaseHooks
	log *log
	// veto vetoes BeforeTransition with this message.
	veto string
	// vetoValidate vetoes Validate for this operation (0: none).
	vetoValidate core.Operation
	// set are set in BeforeTransition (only for the action setOn, when given).
	set   map[string]core.Value
	setOn string
	// job is enqueued in BeforeTransition.
	job string
}

func (r *wfRecorder) note(hook string, ctx core.HookContext) {
	if r.log == nil {
		return
	}
	suffix := ""
	if ctx.Transition != nil {
		suffix = fmt.Sprintf(":%s:%s>%s", ctx.Transition.Action, ctx.Transition.From, ctx.Transition.To)
	}
	r.log.add(hook + ":" + opName(ctx.Operation) + suffix)
}

func (r *wfRecorder) BeforeTransition(ctx core.HookContext, d *core.Document) error {
	r.note("before_transition", ctx)
	if r.setOn == "" || (ctx.Transition != nil && ctx.Transition.Action == r.setOn) {
		for k, v := range r.set {
			d.Values[k] = v
		}
	}
	if r.job != "" {
		if err := ctx.Enqueue(r.job, map[string]any{}); err != nil {
			return err
		}
	}
	if r.veto != "" {
		return core.NewHookError(r.veto)
	}
	return nil
}

func (r *wfRecorder) Validate(ctx core.HookContext, _ *core.Document) error {
	r.note("validate", ctx)
	if r.vetoValidate != 0 && r.vetoValidate == ctx.Operation {
		return core.NewHookError("validate says no")
	}
	return nil
}

func (r *wfRecorder) BeforeSubmit(ctx core.HookContext, _ *core.Document) error {
	r.note("before_submit", ctx)
	return nil
}

func (r *wfRecorder) BeforeCancel(ctx core.HookContext, _ *core.Document) error {
	r.note("before_cancel", ctx)
	return nil
}

type world struct {
	t   *testing.T
	d   *dbtest.DB
	reg *core.Registry
}

func newWorld(t *testing.T) *world { return newWorldWith(t, nil) }

func newWorldWith(t *testing.T, recorder *wfRecorder) *world {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := registry(t, wfOrder)
	reg.AddJob("app.notify")
	if recorder != nil {
		if err := reg.AddHooks("order", recorder); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Sync(reg); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return &world{t: t, d: d, reg: reg}
}

func (w *world) insert(title string, caller core.Caller) *db.StoredDocument {
	w.t.Helper()
	return mustInsert(w.t, w.d, w.reg, "order", doc("title", text(title)), caller)
}

func (w *world) take(stored *db.StoredDocument, action string, caller core.Caller) (*db.StoredDocument, error) {
	return db.Transition(bg, w.d.Pool, w.reg, "order", stored.Name, action, stored.Version, caller)
}

func (w *world) mustTake(stored *db.StoredDocument, action string, caller core.Caller) *db.StoredDocument {
	w.t.Helper()
	got, err := w.take(stored, action, caller)
	if err != nil {
		w.t.Fatalf("%s: %v", action, err)
	}
	return got
}

// walk takes each action as the system context and returns the last document.
func (w *world) walk(stored *db.StoredDocument, actions ...string) *db.StoredDocument {
	w.t.Helper()
	for _, a := range actions {
		stored = w.mustTake(stored, a, sys("setup"))
	}
	return stored
}

func (w *world) get(name string) *db.StoredDocument {
	w.t.Helper()
	return mustGet(w.t, w.d, w.reg, "order", name, sys("setup"))
}

// actions are the state and the actions of the transitions caller may take now.
func (w *world) actions(name string, caller core.Caller) (string, []string, error) {
	available, err := db.AvailableTransitions(bg, w.d.Pool, w.reg, "order", name, caller)
	if err != nil {
		return "", nil, err
	}
	actions := []string{}
	for _, t := range available.Transitions {
		actions = append(actions, t.Action)
	}
	return available.State, actions, nil
}

func (w *world) logRows() [][]string {
	return w.d.Rows("SELECT doctype, name, action, from_state, to_state, actor FROM fb_workflow_log ORDER BY id")
}

func (w *world) count(sql string) string { return w.d.Rows(sql)[0][0] }

func sameDoc(t *testing.T, got, want *db.StoredDocument) {
	t.Helper()
	if fmt.Sprintf("%+v", *got) != fmt.Sprintf("%+v", *want) {
		t.Fatalf("document changed:\n got %+v\nwant %+v", *got, *want)
	}
}

func wantState(t *testing.T, got *db.StoredDocument, state string) {
	t.Helper()
	if got.WorkflowState != state {
		t.Fatalf("state %q, want %q", got.WorkflowState, state)
	}
}

func wantStatus(t *testing.T, got *db.StoredDocument, status core.DocStatus) {
	t.Helper()
	if got.DocStatus == nil || *got.DocStatus != status {
		t.Fatalf("docstatus %v, want %v", got.DocStatus, status)
	}
}

func strsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func rowsEqual(t *testing.T, got, want [][]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Rust: workflow.rs::a_transition_needs_a_role_of_the_transition_and_the_right
func TestATransitionNeedsARoleOfTheTransitionAndTheRight(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send")
	wantState(t, stored, "Pending")

	// The role without the right: Reviewer is a role of Approve but cannot submit.
	_, err := w.take(stored, "Approve", user("rita", "Reviewer"))
	e := wantKind(t, err, db.ErrTransitionDenied)
	if e.Right != core.RightSubmit || e.Action != "Approve" {
		t.Fatalf("%v", e)
	}
	// The right without the role: Auditor can submit but is not a role of Approve.
	_, err = w.take(stored, "Approve", user("ada", "Auditor"))
	wantKind(t, err, db.ErrTransitionDenied)
	// A user with neither, and a user who cannot even read.
	_, err = w.take(stored, "Approve", user("oto", "Other"))
	if e := wantKind(t, err, db.ErrPermissionDenied); e.Right != core.RightRead {
		t.Fatalf("%v", e)
	}
	// Nothing changed: the log has only the Send above.
	sameDoc(t, w.get(stored.Name), stored)
	if len(w.logRows()) != 1 {
		t.Fatal(w.logRows())
	}

	// The role and the right: the transition is taken, and the log has the actor.
	approved := w.mustTake(stored, "Approve", user("mia", "Manager"))
	wantState(t, approved, "Approved")
	wantStatus(t, approved, core.Submitted)
	if approved.Version != stored.Version+1 || approved.ModifiedBy != "mia" {
		t.Fatalf("%+v", approved)
	}
	// Void is for Auditor, who has the cancel right.
	voided := w.mustTake(approved, "Void", user("ada", "Auditor"))
	wantState(t, voided, "Cancelled")
	wantStatus(t, voided, core.Cancelled)
}

// Rust: workflow.rs::administrator_and_the_system_context_pass_the_role_check
func TestAdministratorAndTheSystemContextPassTheRoleCheck(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	stored = w.mustTake(stored, "Send", user("root", "Administrator"))
	// Neither has a role of Approve nor any permission rule.
	stored = w.mustTake(stored, "Approve", sys("setup"))
	stored = w.mustTake(stored, "Cancel", user("root", "Administrator"))
	wantState(t, stored, "Cancelled")
	var actors []string
	for _, r := range w.logRows() {
		actors = append(actors, r[5])
	}
	strsEqual(t, actors, []string{"root", "setup", "root"})
}

// Rust: workflow.rs::if_owner_applies_to_the_right_of_a_transition
func TestIfOwnerAppliesToTheRightOfATransition(t *testing.T) {
	w := newWorld(t)
	mine := w.insert("mine", user("sam", "Self Service"))
	theirs := w.insert("theirs", user("tia", "Self Service"))

	// Own document: allowed. Somebody else's, which Self Service can read: refused, like update.
	sent := w.mustTake(mine, "Send", user("sam", "Self Service"))
	wantState(t, sent, "Pending")
	_, err := w.take(theirs, "Send", user("sam", "Self Service"))
	wantKind(t, err, db.ErrTransitionDenied)
	_, err = db.Update(bg, w.d.Pool, w.reg, "order", theirs.Name, theirs.Version, changes("total", core.Int(1)), user("sam", "Self Service"))
	wantKind(t, err, db.ErrPermissionDenied)

	// A role whose read right is if_owner does not see the document of somebody else.
	spy := user("sue", "Spy")
	_, err = w.take(theirs, "Send", spy)
	wantKind(t, err, db.ErrNotFound)
	// Even with a stale version: not found comes first.
	stale := *theirs
	stale.Version = 99
	_, err = w.take(&stale, "Send", spy)
	wantKind(t, err, db.ErrNotFound)
	// Its own document (the system context records sue as its owner): Send needs write and Approve needs
	// submit, which Spy has only if_owner.
	own := w.insert("own", sys("sue"))
	own = w.mustTake(own, "Send", spy)
	own = w.mustTake(own, "Approve", spy)
	wantState(t, own, "Approved")
}

// Rust: workflow.rs::a_wrong_state_and_an_unknown_action_are_unknown_transitions
func TestAWrongStateAndAnUnknownActionAreUnknownTransitions(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	// Approve exists, but not from Draft.
	_, err := w.take(stored, "Approve", sys("setup"))
	e := wantKind(t, err, db.ErrUnknownTransition)
	if e.State != "Draft" || e.Action != "Approve" {
		t.Fatalf("%v", e)
	}
	if want := fmt.Sprintf("order `%s` is in state `Draft`: no transition `Approve` from it", stored.Name); err.Error() != want {
		t.Fatalf("%q", err)
	}
	for _, action := range []string{"Nope", "send", ""} {
		_, err := w.take(stored, action, sys("setup"))
		wantKind(t, err, db.ErrUnknownTransition)
	}
	// A document without a state has no transitions.
	w.d.Exec(fmt.Sprintf("UPDATE doc_order SET workflow_state = NULL WHERE name = '%s'", stored.Name))
	_, err = w.take(stored, "Send", sys("setup"))
	if e := wantKind(t, err, db.ErrUnknownTransition); e.State != "(none)" {
		t.Fatalf("%v", e)
	}
	if len(w.logRows()) != 0 {
		t.Fatal(w.logRows())
	}
}

// Rust: workflow.rs::a_stale_version_is_a_conflict_before_the_action_is_looked_at
func TestAStaleVersionIsAConflictBeforeTheActionIsLookedAt(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	stale := *stored
	stale.Version = 7
	for _, action := range []string{"Send", "Nope"} {
		_, err := w.take(&stale, action, sys("setup"))
		if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 7 || e.CurrentVersion != 1 {
			t.Fatalf("%s: %v", action, e)
		}
	}
}

// Rust: workflow.rs::of_two_concurrent_transitions_one_wins_and_the_other_conflicts
func TestOfTwoConcurrentTransitionsOneWinsAndTheOtherConflicts(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	// The first transaction takes the transition and has not committed yet.
	tx, err := w.d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := db.Transition(bg, tx, w.reg, "order", stored.Name, "Send", stored.Version, sys("setup")); err != nil {
		t.Fatal(err)
	}
	// The second holds the same version and waits for the row lock ...
	done := make(chan error, 1)
	go func() {
		_, err := db.Transition(bg, w.d.Pool, w.reg, "order", stored.Name, "Send", stored.Version, sys("two"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the second transition should wait for the first transaction: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// ... and once the first commits the version no longer matches.
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	e := wantKind(t, <-done, db.ErrConflict)
	if e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Fatalf("%v", e)
	}
	if len(w.logRows()) != 1 {
		t.Fatal(w.logRows())
	}
	wantState(t, w.get(stored.Name), "Pending")
}

// Rust: workflow.rs::a_draft_to_draft_transition_updates_the_whole_document
func TestADraftToDraftTransitionUpdatesTheWholeDocument(t *testing.T) {
	l := &log{}
	w := newWorldWith(t, &wfRecorder{log: l, set: map[string]core.Value{"total": core.Int(42)}})
	stored := w.insert("a", sys("setup"))
	l.taken()

	sent := w.mustTake(stored, "Send", sys("setup"))
	wantState(t, sent, "Pending")
	wantStatus(t, sent, core.Draft)
	if sent.Version != 2 {
		t.Fatal(sent.Version)
	}
	// The hook's change is validated and written with the state.
	if !sent.Values["total"].Equal(core.Int(42)) {
		t.Fatal(sent.Values["total"])
	}
	// ctx.Transition is filled in for BeforeTransition, and the Validate hooks of a draft-to-draft move
	// run with OpTransition.
	strsEqual(t, l.taken(), []string{
		"before_transition:Transition:Send:Draft>Pending",
		"validate:Transition:Send:Draft>Pending",
	})
	rowsEqual(t, w.logRows(), [][]string{{"order", stored.Name, "Send", "Draft", "Pending", "setup"}})
}

// Rust: workflow.rs::a_draft_to_submitted_transition_runs_the_submit_pipeline_after_before_transition
func TestADraftToSubmittedTransitionRunsTheSubmitPipelineAfterBeforeTransition(t *testing.T) {
	l := &log{}
	w := newWorldWith(t, &wfRecorder{log: l})
	stored := w.mustTake(w.insert("a", sys("setup")), "Send", sys("setup"))
	l.taken()

	approved := w.mustTake(stored, "Approve", sys("setup"))
	wantStatus(t, approved, core.Submitted)
	wantState(t, approved, "Approved")
	// before_transition first, then validate and before_submit with OpSubmit.
	strsEqual(t, l.taken(), []string{
		"before_transition:Transition:Approve:Pending>Approved",
		"validate:Submit",
		"before_submit:Submit",
	})
}

// Rust: workflow.rs::a_submitted_to_submitted_transition_changes_allow_on_submit_fields_only
func TestASubmittedToSubmittedTransitionChangesAllowOnSubmitFieldsOnly(t *testing.T) {
	// The hook changes total, which is locked once the document is submitted.
	w := newWorldWith(t, &wfRecorder{set: map[string]core.Value{"total": core.Int(5)}, setOn: "Archive"})
	stored := w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	_, err := w.take(stored, "Archive", sys("setup"))
	if e := wantKind(t, err, db.ErrFieldLocked); e.Field != "total" {
		t.Fatalf("%v", e)
	}
	sameDoc(t, w.get(stored.Name), stored)
	if len(w.logRows()) != 2 {
		t.Fatal("Send and Approve only")
	}

	// remarks is allow_on_submit: the change is written with the new state.
	w = newWorldWith(t, &wfRecorder{set: map[string]core.Value{"remarks": text("checked")}, setOn: "Archive"})
	stored = w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	archived := w.mustTake(stored, "Archive", sys("setup"))
	wantState(t, archived, "Archived")
	wantStatus(t, archived, core.Submitted)
	if !archived.Values["remarks"].Equal(text("checked")) || !archived.Values["title"].Equal(text("a")) || archived.Version != stored.Version+1 {
		t.Fatalf("%+v", archived)
	}
}

// Rust: workflow.rs::a_submitted_to_cancelled_transition_runs_the_cancel_pipeline
func TestASubmittedToCancelledTransitionRunsTheCancelPipeline(t *testing.T) {
	l := &log{}
	w := newWorldWith(t, &wfRecorder{log: l})
	stored := w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	l.taken()

	cancelled := w.mustTake(stored, "Cancel", sys("setup"))
	wantStatus(t, cancelled, core.Cancelled)
	wantState(t, cancelled, "Cancelled")
	strsEqual(t, l.taken(), []string{
		"before_transition:Transition:Cancel:Approved>Cancelled",
		"before_cancel:Cancel",
	})
}

// Rust: workflow.rs::before_transition_may_change_only_allow_on_submit_fields_of_a_cancel
func TestBeforeTransitionMayChangeOnlyAllowOnSubmitFieldsOfACancel(t *testing.T) {
	w := newWorldWith(t, &wfRecorder{set: map[string]core.Value{"total": core.Int(5)}, setOn: "Cancel"})
	stored := w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	_, err := w.take(stored, "Cancel", sys("setup"))
	if e := wantKind(t, err, db.ErrFieldLocked); e.Field != "total" {
		t.Fatalf("%v", e)
	}
	sameDoc(t, w.get(stored.Name), stored)

	w = newWorldWith(t, &wfRecorder{set: map[string]core.Value{"remarks": text("why")}, setOn: "Cancel"})
	stored = w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	cancelled := w.mustTake(stored, "Cancel", sys("setup"))
	wantStatus(t, cancelled, core.Cancelled)
	if !cancelled.Values["remarks"].Equal(text("why")) {
		t.Fatal(cancelled.Values["remarks"])
	}
}

// Rust: workflow.rs::a_veto_leaves_the_document_the_log_and_the_jobs_unchanged
func TestAVetoLeavesTheDocumentTheLogAndTheJobsUnchanged(t *testing.T) {
	w := newWorldWith(t, &wfRecorder{veto: "not today", job: "app.notify"})
	stored := w.insert("a", sys("setup"))
	_, err := w.take(stored, "Send", sys("setup"))
	if e := wantKind(t, err, db.ErrHook); e.Hook != "before_transition" || e.Message != "not today" {
		t.Fatalf("%v", e)
	}
	sameDoc(t, w.get(stored.Name), stored)
	if len(w.logRows()) != 0 || w.count("SELECT count(*)::text FROM fb_job") != "0" {
		t.Fatal("log or jobs changed")
	}
}

// Rust: workflow.rs::a_failure_after_the_hook_rolls_everything_back
func TestAFailureAfterTheHookRollsEverythingBack(t *testing.T) {
	// before_transition enqueues a job, then a validate hook refuses the draft-to-draft move.
	w := newWorldWith(t, &wfRecorder{job: "app.notify", vetoValidate: core.OpTransition})
	stored := w.insert("a", sys("setup"))
	_, err := w.take(stored, "Send", sys("setup"))
	if e := wantKind(t, err, db.ErrHook); e.Hook != "validate" {
		t.Fatalf("%v", e)
	}
	sameDoc(t, w.get(stored.Name), stored)
	if len(w.logRows()) != 0 || w.count("SELECT count(*)::text FROM fb_job") != "0" {
		t.Fatal("log or jobs changed")
	}

	// The framework validation after the hook: the hook clears a required field.
	w = newWorldWith(t, &wfRecorder{job: "app.notify", set: map[string]core.Value{"title": core.Null()}})
	stored = w.insert("a", sys("setup"))
	_, err = w.take(stored, "Send", sys("setup"))
	problems := validationOf(t, err)
	if len(problems) != 1 || !hasProblem(problems, core.ValidationError{Kind: core.ErrRequired, Field: "title"}) {
		t.Fatalf("%v", problems)
	}
	sameDoc(t, w.get(stored.Name), stored)
	if len(w.logRows()) != 0 || w.count("SELECT count(*)::text FROM fb_job") != "0" {
		t.Fatal("log or jobs changed")
	}
}

// Rust: workflow.rs::a_committed_transition_has_exactly_one_log_row_and_its_jobs
func TestACommittedTransitionHasExactlyOneLogRowAndItsJobs(t *testing.T) {
	w := newWorldWith(t, &wfRecorder{job: "app.notify"})
	stored := w.insert("a", sys("setup"))
	w.mustTake(stored, "Send", sys("setup"))
	if len(w.logRows()) != 1 || w.count("SELECT count(*)::text FROM fb_job") != "1" {
		t.Fatal("expected one log row and one job")
	}

	// In a caller-owned transaction that is rolled back, the log row goes with it.
	stored = w.get(stored.Name)
	tx, err := w.d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Transition(bg, tx, w.reg, "order", stored.Name, "Approve", stored.Version, sys("setup")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if len(w.logRows()) != 1 || w.count("SELECT count(*)::text FROM fb_job") != "1" {
		t.Fatal("the rollback kept something")
	}
	wantState(t, w.get(stored.Name), "Pending")
}

// Rust: workflow.rs::plain_submit_and_cancel_are_refused_before_anything_is_looked_up
func TestPlainSubmitAndCancelAreRefusedBeforeAnythingIsLookedUp(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send")
	// The system context included, for a draft, for a document that does not exist and with a stale
	// version: the answer never depends on the document.
	for _, name := range []string{stored.Name, "nobody"} {
		for _, version := range []int64{stored.Version, 99} {
			_, err := db.Submit(bg, w.d.Pool, w.reg, "order", name, version, sys("setup"))
			if e := wantKind(t, err, db.ErrWorkflowRequired); e.Operation != "submit" || e.DocType != "order" {
				t.Fatalf("%v", e)
			}
			_, err = db.Cancel(bg, w.d.Pool, w.reg, "order", name, version, sys("setup"))
			if e := wantKind(t, err, db.ErrWorkflowRequired); e.Operation != "cancel" {
				t.Fatalf("%v", e)
			}
		}
	}
	// Also for a caller who may not even read the DocType.
	_, err := db.Submit(bg, w.d.Pool, w.reg, "order", stored.Name, stored.Version, user("oto", "Other"))
	wantKind(t, err, db.ErrWorkflowRequired)
	if err.Error() != "DocType `order` has a workflow; use a transition (cannot submit)" {
		t.Fatalf("%q", err)
	}
	sameDoc(t, w.get(stored.Name), stored)
}

// Rust: workflow.rs::an_update_in_a_state_with_edit_roles_needs_one_of_them
func TestAnUpdateInAStateWithEditRolesNeedsOneOfThem(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	clerk := user("cleo", "Clerk")
	// Draft has no edit_roles: the write right alone decides.
	stored = mustUpdate(t, w.d, w.reg, "order", stored.Name, 1, changes("total", core.Int(1)), clerk)
	pending := w.mustTake(stored, "Send", clerk)
	wantState(t, pending, "Pending")

	// Pending has edit_roles = [Manager]: a Clerk with the write right is refused ...
	_, err := db.Update(bg, w.d.Pool, w.reg, "order", pending.Name, pending.Version, changes("total", core.Int(2)), clerk)
	if e := wantKind(t, err, db.ErrEditDenied); e.State != "Pending" {
		t.Fatalf("%v", e)
	}
	// ... but a Manager, an administrator and the system context pass.
	for i, who := range []core.Caller{user("mia", "Manager"), user("root", "Administrator"), sys("setup")} {
		current := w.get(pending.Name)
		n := int64(3 + i)
		updated := mustUpdate(t, w.d, w.reg, "order", current.Name, current.Version, changes("total", core.Int(n)), who)
		if !updated.Values["total"].Equal(core.Int(n)) {
			t.Fatal(updated.Values["total"])
		}
		wantState(t, updated, "Pending") // an update never changes the state
	}
}

// Rust: workflow.rs::insert_and_amend_start_in_the_initial_state
func TestInsertAndAmendStartInTheInitialState(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	wantState(t, stored, "Draft")
	wantStatus(t, stored, core.Draft)
	rowsEqual(t, w.d.Rows("SELECT workflow_state FROM doc_order"), [][]string{{"Draft"}})

	cancelled := w.walk(stored, "Send", "Approve", "Cancel")
	wantState(t, cancelled, "Cancelled")
	amended := mustAmend(t, w.d, w.reg, "order", cancelled.Name, cancelled.Version, user("mia", "Manager"))
	wantState(t, amended, "Draft")
	wantStatus(t, amended, core.Draft)
	if amended.AmendedFrom != cancelled.Name {
		t.Fatal(amended.AmendedFrom)
	}
	// The history stays with the cancelled document; the amendment starts with none.
	for _, row := range w.logRows() {
		if row[1] != cancelled.Name {
			t.Fatal(w.logRows())
		}
	}
}

// Rust: workflow.rs::deleting_a_document_deletes_its_log_rows_in_the_same_transaction
func TestDeletingADocumentDeletesItsLogRowsInTheSameTransaction(t *testing.T) {
	w := newWorld(t)
	kept := w.insert("kept", sys("setup"))
	w.walk(kept, "Send")
	gone := w.walk(w.insert("gone", sys("setup")), "Send", "Reject")
	if len(w.logRows()) != 3 {
		t.Fatal(w.logRows())
	}
	// A refused delete keeps the log (a stale version).
	wantKind(t, db.Delete(bg, w.d.Pool, w.reg, "order", gone.Name, 1, user("mia", "Manager")), db.ErrConflict)
	if len(w.logRows()) != 3 {
		t.Fatal(w.logRows())
	}
	if err := db.Delete(bg, w.d.Pool, w.reg, "order", gone.Name, gone.Version, user("mia", "Manager")); err != nil {
		t.Fatal(err)
	}
	log := w.logRows()
	if len(log) != 1 || log[0][1] != kept.Name {
		t.Fatal(log)
	}

	// A name can be reused: the new document has a clean history.
	w.d.Exec(fmt.Sprintf("UPDATE doc_order SET name = 'reused' WHERE name = '%s'", kept.Name))
	w.d.Exec(fmt.Sprintf("UPDATE fb_workflow_log SET name = 'reused' WHERE name = '%s'", kept.Name))
	reused := w.get("reused")
	entries, err := db.History(bg, w.d.Pool, w.reg, "order", "reused", sys("setup"))
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	// Reject it back to draft and delete it: the log goes too.
	reused = w.mustTake(reused, "Reject", sys("setup"))
	if err := db.Delete(bg, w.d.Pool, w.reg, "order", "reused", reused.Version, sys("setup")); err != nil {
		t.Fatal(err)
	}
	if len(w.logRows()) != 0 {
		t.Fatal(w.logRows())
	}
}

// Rust: workflow.rs::a_client_cannot_set_the_workflow_state
func TestAClientCannotSetTheWorkflowState(t *testing.T) {
	w := newWorld(t)
	d := doc("title", text("a"))
	d.Values["workflow_state"] = text("Approved")
	_, err := db.Insert(bg, w.d.Pool, w.reg, "order", d, sys("setup"))
	problems := validationOf(t, err)
	if len(problems) != 1 || !hasProblem(problems, core.ValidationError{Kind: core.ErrUnknownField, Field: "workflow_state"}) {
		t.Fatalf("%v", problems)
	}
	if w.count("SELECT count(*)::text FROM doc_order") != "0" {
		t.Fatal("a document was written")
	}
	stored := w.insert("a", sys("setup"))
	_, err = db.Update(bg, w.d.Pool, w.reg, "order", stored.Name, stored.Version, changes("workflow_state", text("Approved")), sys("setup"))
	wantKind(t, err, db.ErrValidation)
	sameDoc(t, w.get(stored.Name), stored)
}

// Rust: workflow.rs::a_doctype_without_a_workflow_has_no_state_column_and_no_transitions
func TestADoctypeWithoutAWorkflowHasNoStateColumnAndNoTransitions(t *testing.T) {
	d, reg := setup(t, wfOrderWithoutWorkflow())
	for _, c := range d.Columns("doc_order") {
		if c[0] == "workflow_state" {
			t.Fatalf("%v", d.Columns("doc_order"))
		}
	}
	stored := mustInsert(t, d, reg, "order", doc("title", text("a")), sys("setup"))
	if stored.WorkflowState != "" {
		t.Fatal(stored.WorkflowState)
	}
	_, err := db.Transition(bg, d.Pool, reg, "order", stored.Name, "Send", 1, sys("setup"))
	if e := wantKind(t, err, db.ErrNoWorkflow); e.DocType != "order" {
		t.Fatalf("%v", e)
	}
	_, err = db.AvailableTransitions(bg, d.Pool, reg, "order", stored.Name, sys("setup"))
	wantKind(t, err, db.ErrNoWorkflow)
	_, err = db.History(bg, d.Pool, reg, "order", stored.Name, sys("setup"))
	wantKind(t, err, db.ErrNoWorkflow)
	// Plain submit and cancel work as before, and the state is not a column to filter by.
	submitted := mustSubmit(t, d, reg, "order", stored.Name, 1, sys("setup"))
	wantStatus(t, submitted, core.Submitted)
	_, err = db.List(bg, d.Pool, reg, "order", sys("setup"), db.ListQuery{Filters: []db.Filter{{Field: "workflow_state", Op: db.IsNull(true)}}})
	wantKind(t, err, db.ErrInvalidQuery)
}

// Rust: workflow.rs::available_transitions_follow_the_role_the_right_and_if_owner
func TestAvailableTransitionsFollowTheRoleTheRightAndIfOwner(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("sue")), "Send")
	// Pending: Reject (Manager) and Approve (Manager, Reviewer, Spy).
	state, list, err := w.actions(stored.Name, user("mia", "Manager"))
	if err != nil || state != "Pending" {
		t.Fatal(state, err)
	}
	strsEqual(t, list, []string{"Reject", "Approve"})
	// The role without the right gives nothing; so does the right without the role.
	for _, c := range []core.Caller{user("rita", "Reviewer"), user("ada", "Auditor")} {
		if _, list, err := w.actions(stored.Name, c); err != nil || len(list) != 0 {
			t.Fatal(list, err)
		}
	}
	// Spy: submit if_owner, and the document is sue's.
	_, list, err = w.actions(stored.Name, user("sue", "Spy"))
	if err != nil {
		t.Fatal(err)
	}
	strsEqual(t, list, []string{"Approve"})
	_, _, err = w.actions(stored.Name, user("sam", "Spy"))
	wantKind(t, err, db.ErrNotFound) // not readable: not found
	// Administrator and the system context may take everything from the state.
	for _, c := range []core.Caller{user("root", "Administrator"), sys("setup")} {
		_, list, err = w.actions(stored.Name, c)
		if err != nil {
			t.Fatal(err)
		}
		strsEqual(t, list, []string{"Reject", "Approve"})
	}
	// Not readable at all: a permission error, as for get.
	_, _, err = w.actions(stored.Name, user("oto", "Other"))
	wantKind(t, err, db.ErrPermissionDenied)
	// A missing document.
	_, err = db.AvailableTransitions(bg, w.d.Pool, w.reg, "order", "nobody", sys("setup"))
	wantKind(t, err, db.ErrNotFound)
	// No state, no transitions.
	w.d.Exec("UPDATE doc_order SET workflow_state = NULL")
	state, list, err = w.actions(stored.Name, sys("setup"))
	if err != nil || state != "" || len(list) != 0 {
		t.Fatal(state, list, err)
	}
}

// Rust: workflow.rs::the_history_is_oldest_first_and_needs_read_access
func TestTheHistoryIsOldestFirstAndNeedsReadAccess(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("sue")), "Send")
	stored = w.mustTake(stored, "Reject", user("mia", "Manager"))
	w.mustTake(stored, "Send", user("cleo", "Clerk"))

	entries, err := db.History(bg, w.d.Pool, w.reg, "order", stored.Name, user("mia", "Manager"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, e := range entries {
		rows = append(rows, strings.Join([]string{e.Action, e.From, e.To, e.Actor}, ","))
	}
	strsEqual(t, rows, []string{"Send,Draft,Pending,setup", "Reject,Pending,Draft,mia", "Send,Draft,Pending,cleo"})
	// at is the transaction time: not decreasing.
	for i := 1; i < len(entries); i++ {
		if entries[i].At.Before(entries[i-1].At) {
			t.Fatal(entries)
		}
	}
	// A reader who cannot see the document gets not found, and a DocType-level refusal.
	_, err = db.History(bg, w.d.Pool, w.reg, "order", stored.Name, user("sam", "Spy"))
	wantKind(t, err, db.ErrNotFound)
	_, err = db.History(bg, w.d.Pool, w.reg, "order", stored.Name, user("oto", "Other"))
	wantKind(t, err, db.ErrPermissionDenied)
	_, err = db.History(bg, w.d.Pool, w.reg, "order", "nobody", sys("setup"))
	wantKind(t, err, db.ErrNotFound)
}

// Rust: workflow.rs::the_history_is_capped_at_the_newest_500_rows
func TestTheHistoryIsCappedAtTheNewest500Rows(t *testing.T) {
	w := newWorld(t)
	stored := w.insert("a", sys("setup"))
	w.d.Exec(fmt.Sprintf("INSERT INTO fb_workflow_log (doctype, name, action, from_state, to_state, actor) "+
		"SELECT 'order', '%s', 'A' || n, 'x', 'y', 'bulk' FROM generate_series(1, 502) n", stored.Name))
	// Another document's rows are not mixed in.
	w.d.Exec("INSERT INTO fb_workflow_log (doctype, name, action, from_state, to_state, actor) VALUES ('order', 'other', 'Z', 'x', 'y', 'bulk')")
	entries, err := db.History(bg, w.d.Pool, w.reg, "order", stored.Name, sys("setup"))
	if err != nil || len(entries) != 500 || entries[0].Action != "A3" || entries[499].Action != "A502" {
		t.Fatal(len(entries), err)
	}
}

// Rust: workflow.rs::the_state_can_be_filtered_and_sorted_in_a_list
func TestTheStateCanBeFilteredAndSortedInAList(t *testing.T) {
	w := newWorld(t)
	a := w.insert("a", sys("setup"))
	b := w.insert("b", sys("setup"))
	w.insert("c", sys("setup"))
	w.walk(a, "Send", "Approve")
	w.walk(b, "Send")
	titles := func(q db.ListQuery) []string {
		docs, err := db.List(bg, w.d.Pool, w.reg, "order", sys("setup"), q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range docs {
			out = append(out, d.Values["title"].Text)
		}
		return out
	}
	strsEqual(t, titles(db.ListQuery{Filters: []db.Filter{{Field: "workflow_state", Op: db.Eq(text("Pending"))}}}), []string{"b"})
	strsEqual(t, titles(db.ListQuery{
		Filters: []db.Filter{{Field: "workflow_state", Op: db.In(text("Pending"), text("Draft"))}},
		Sort:    []db.Sort{db.Desc("workflow_state")},
	}), []string{"b", "c"})
	docs, err := db.List(bg, w.d.Pool, w.reg, "order", sys("setup"), db.ListQuery{Filters: []db.Filter{{Field: "workflow_state", Op: db.Like("App%")}}})
	if err != nil || len(docs) != 1 || docs[0].WorkflowState != "Approved" {
		t.Fatal(docs, err)
	}
}

// Rust: workflow.rs::the_table_of_a_workflow_doctype_has_a_nullable_state_column
func TestTheTableOfAWorkflowDoctypeHasANullableStateColumn(t *testing.T) {
	w := newWorld(t)
	columns := w.d.Columns("doc_order")
	tail := columns[7:10]
	want := []dbtest.Column{{"docstatus", "smallint/NO"}, {"amended_from", "text/YES"}, {"workflow_state", "text/YES"}}
	if fmt.Sprint(tail) != fmt.Sprint(want) {
		t.Fatalf("%v", tail)
	}
	// In sync: nothing to do. Drift in the column is a conflict, like the docstatus columns.
	report, err := w.d.Sync(w.reg)
	if err != nil || len(report.Steps) != 0 {
		t.Fatal(report, err)
	}
	w.d.Exec("ALTER TABLE doc_order ALTER COLUMN workflow_state TYPE varchar(10)")
	_, err = w.d.Sync(w.reg)
	e := wantKind(t, err, db.ErrSyncConflicts)
	if len(e.Conflicts) != 1 || e.Conflicts[0].Kind != db.ConflictColumnMismatch || e.Conflicts[0].Column != "workflow_state" {
		t.Fatalf("%v", e.Conflicts)
	}
	w.d.Exec("ALTER TABLE doc_order ALTER COLUMN workflow_state TYPE text")
	w.d.Exec("ALTER TABLE doc_order ALTER COLUMN workflow_state SET NOT NULL")
	_, err = w.d.Sync(w.reg)
	wantKind(t, err, db.ErrSyncConflicts)
}

// Rust: workflow.rs::edit_roles_apply_to_drafts_only
func TestEditRolesApplyToDraftsOnly(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send", "Approve")
	wantStatus(t, stored, core.Submitted)
	// A stale state (a workflow edited after the document was written) must not block an allow_on_submit
	// update: Pending has edit_roles, but the document is not a draft.
	w.d.Exec(fmt.Sprintf("UPDATE doc_order SET workflow_state = 'Pending' WHERE name = '%s'", stored.Name))
	updated := mustUpdate(t, w.d, w.reg, "order", stored.Name, stored.Version, changes("remarks", text("late note")), user("cleo", "Clerk"))
	if !updated.Values["remarks"].Equal(text("late note")) {
		t.Fatal(updated.Values["remarks"])
	}
}

// Rust: workflow.rs::available_transitions_skip_those_the_docstatus_disagrees_with
func TestAvailableTransitionsSkipThoseTheDocstatusDisagreesWith(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send")
	_, before, err := w.actions(stored.Name, sys("setup"))
	if err != nil {
		t.Fatal(err)
	}
	strsEqual(t, before, []string{"Reject", "Approve"})
	// The stored docstatus disagrees with the state Pending (draft): Transition would refuse these, so
	// they are not offered.
	w.d.Exec(fmt.Sprintf("UPDATE doc_order SET docstatus = 1 WHERE name = '%s'", stored.Name))
	state, after, err := w.actions(stored.Name, sys("setup"))
	if err != nil || state != "Pending" || len(after) != 0 {
		t.Fatal(state, after, err)
	}
}

// Rust: workflow.rs::deleting_removes_the_log_rows_also_after_the_workflow_was_removed
func TestDeletingRemovesTheLogRowsAlsoAfterTheWorkflowWasRemoved(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send", "Reject")
	if len(w.logRows()) != 2 {
		t.Fatal(w.logRows())
	}
	plain := registry(t, wfOrderWithoutWorkflow())
	if err := db.Delete(bg, w.d.Pool, plain, "order", stored.Name, stored.Version, sys("setup")); err != nil {
		t.Fatal(err)
	}
	if len(w.logRows()) != 0 {
		t.Fatal(w.logRows())
	}
}

// Rust: workflow.rs::removing_the_workflow_keeps_the_column_and_its_data_with_a_warning
func TestRemovingTheWorkflowKeepsTheColumnAndItsDataWithAWarning(t *testing.T) {
	w := newWorld(t)
	stored := w.walk(w.insert("a", sys("setup")), "Send")
	plain := registry(t, wfOrderWithoutWorkflow())
	report, err := w.d.Sync(plain)
	if err != nil || len(report.Steps) != 0 {
		t.Fatal(report, err)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Kind != db.WarningColumnNotInMetadata ||
		report.Warnings[0].Table != "doc_order" || report.Warnings[0].Column != "workflow_state" {
		t.Fatalf("%v", report.Warnings)
	}
	rowsEqual(t, w.d.Rows(fmt.Sprintf("SELECT workflow_state FROM doc_order WHERE name = '%s'", stored.Name)), [][]string{{"Pending"}})
	// Without the workflow the document behaves as before: no state in the document, and a plain submit is
	// possible again.
	got := mustGet(t, w.d, plain, "order", stored.Name, sys("setup"))
	if got.WorkflowState != "" {
		t.Fatal(got.WorkflowState)
	}
	mustSubmit(t, w.d, plain, "order", stored.Name, got.Version, sys("setup"))
}

// Rust: workflow.rs::migrate_gives_existing_documents_their_state_and_is_idempotent
func TestMigrateGivesExistingDocumentsTheirStateAndIsIdempotent(t *testing.T) {
	d := dbtest.New(t)
	before := registry(t, wfOrderWithoutWorkflow())
	options := db.DefaultSyncOptions()
	if _, err := db.Migrate(bg, d.Pool, before, options); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, d, before, "order", doc("title", text("draft")), sys("setup"))
	submitted := mustInsert(t, d, before, "order", doc("title", text("submitted")), sys("setup"))
	mustSubmit(t, d, before, "order", submitted.Name, 1, sys("setup"))
	cancelled := mustInsert(t, d, before, "order", doc("title", text("cancelled")), sys("setup"))
	cancelled = mustSubmit(t, d, before, "order", cancelled.Name, 1, sys("setup"))
	mustCancel(t, d, before, "order", cancelled.Name, cancelled.Version, sys("setup"))

	// The workflow is added later: the column is added and the documents get the first state with their
	// docstatus. The first state of docstatus 1 is Approved, of docstatus 2 Cancelled.
	after := registry(t, wfOrder)
	report, err := db.Migrate(bg, d.Pool, after, options)
	if err != nil || len(report.WorkflowWarnings) != 0 {
		t.Fatal(report, err)
	}
	const query = `SELECT title, docstatus::text, workflow_state, version::text FROM doc_order ORDER BY title COLLATE "C"`
	first := d.Rows(query)
	// versions are not touched
	rowsEqual(t, first, [][]string{{"cancelled", "2", "Cancelled", "3"}, {"draft", "0", "Draft", "1"}, {"submitted", "1", "Approved", "2"}})

	// Again: nothing changes.
	report, err = db.Migrate(bg, d.Pool, after, options)
	if err != nil || len(report.Sync.Steps) != 0 || len(report.WorkflowWarnings) != 0 {
		t.Fatal(report, err)
	}
	rowsEqual(t, d.Rows(query), first)
	// A dry run backfills nothing either (the column of a dry run does not exist yet).
	dry := options
	dry.DryRun = true
	if _, err := db.Migrate(bg, d.Pool, after, dry); err != nil {
		t.Fatal(err)
	}

	// A state the workflow no longer defines is reported once per DocType, and left alone.
	d.Exec("UPDATE doc_order SET workflow_state = 'Gone' WHERE title IN ('draft', 'submitted')")
	report, err = db.Migrate(bg, d.Pool, after, options)
	if err != nil {
		t.Fatal(err)
	}
	strsEqual(t, report.WorkflowWarnings, []string{"DocType `order`: 2 documents are in a workflow state that no longer exists; no transition applies to them"})
	d.Exec("UPDATE doc_order SET workflow_state = 'Draft' WHERE title = 'draft'")
	report, err = db.Migrate(bg, d.Pool, after, options)
	if err != nil {
		t.Fatal(err)
	}
	strsEqual(t, report.WorkflowWarnings, []string{"DocType `order`: 1 document is in a workflow state that no longer exists; no transition applies to them"})
	rowsEqual(t, d.Rows("SELECT workflow_state FROM doc_order WHERE title = 'submitted'"), [][]string{{"Gone"}})
	// Such a document has no transitions.
	stuck := mustGet(t, d, after, "order", submitted.Name, sys("setup"))
	_, err = db.Transition(bg, d.Pool, after, "order", stuck.Name, "Archive", stuck.Version, sys("setup"))
	if e := wantKind(t, err, db.ErrUnknownTransition); e.State != "Gone" {
		t.Fatalf("%v", e)
	}
}
