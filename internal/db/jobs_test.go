package db_test

import (
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The outbox of the job queue (M6.1) against a real PostgreSQL: jobs that hooks enqueue are written in
// the operation's transaction. Ported from crates/forgebase-db/tests/jobs.rs, the part that is the
// outbox (enqueue). The claim, complete, fail, release, list and retry functions and their tests are
// P5.

const jobsQuote = `
name = "quote"
label = "Quote"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
`

// jobsLedger is a DocType whose title is unique, to make a write fail after the hooks.
const jobsLedger = `
name = "ledger"
label = "Ledger"
[[fields]]
name = "title"
type = "data"
required = true
unique = true
`

// enqueuer enqueues app.audit from every hook point, with the operation in the payload.
type enqueuer struct {
	core.BaseHooks
	job string
}

func (e enqueuer) enqueue(ctx core.HookContext) error {
	return ctx.Enqueue(e.job, map[string]any{"operation": opName(ctx.Operation)})
}

func (e enqueuer) BeforeInsert(ctx core.HookContext, _ *core.Document) error { return e.enqueue(ctx) }

func (e enqueuer) Validate(ctx core.HookContext, _ *core.Document) error {
	// Runs for insert, update, submit and amend; the insert and amend ones are already covered by
	// BeforeInsert, so only the others enqueue here.
	if ctx.Operation == core.OpUpdate || ctx.Operation == core.OpSubmit {
		return e.enqueue(ctx)
	}
	return nil
}

func (e enqueuer) BeforeDelete(ctx core.HookContext, _ *core.Document) error { return e.enqueue(ctx) }
func (e enqueuer) BeforeCancel(ctx core.HookContext, _ *core.Document) error { return e.enqueue(ctx) }

// rejecter rejects a document whose title is reject.
type rejecter struct{ core.BaseHooks }

func (rejecter) Validate(_ core.HookContext, doc *core.Document) error {
	if v, ok := doc.Values["title"]; ok && v.Equal(text("reject")) {
		return core.NewHookError("rejected")
	}
	return nil
}

func jobsRegistry(t *testing.T, hooks ...core.DocTypeHooks) *core.Registry {
	t.Helper()
	reg := registry(t, jobsQuote, jobsLedger)
	for _, h := range hooks {
		for _, doctype := range []string{"quote", "ledger"} {
			if err := reg.AddHooks(doctype, h); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !reg.AddJob("app.audit") {
		t.Fatal("job")
	}
	return reg
}

func jobsSetup(t *testing.T, hooks ...core.DocTypeHooks) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := jobsRegistry(t, hooks...)
	mustSync(t, d, reg)
	return d, reg
}

func quoteTitled(title string) core.Document { return doc("title", text(title)) }

func titleChange(title string) core.Changes { return changes("title", text(title)) }

func queued(d *dbtest.DB) []string {
	out := []string{}
	for _, r := range d.Rows("SELECT payload->>'operation' || '/' || enqueued_by || '/' || status FROM fb_job ORDER BY id") {
		out = append(out, r[0])
	}
	return out
}

// Rust: jobs.rs::hooks_enqueue_in_the_transaction_of_every_operation
func TestHooksEnqueueInTheTransactionOfEveryOperation(t *testing.T) {
	d, reg := jobsSetup(t, enqueuer{job: "app.audit"})
	ann := sys("ann")
	stored := mustInsert(t, d, reg, "quote", quoteTitled("one"), ann)
	stored = mustUpdate(t, d, reg, "quote", stored.Name, 1, titleChange("two"), ann)
	stored = mustSubmit(t, d, reg, "quote", stored.Name, 2, ann)
	stored = mustCancel(t, d, reg, "quote", stored.Name, 3, ann)
	amended := mustAmend(t, d, reg, "quote", stored.Name, 4, ann)
	if err := db.Delete(bg, d.Pool, reg, "quote", amended.Name, 1, ann); err != nil {
		t.Fatal(err)
	}
	want(t, queued(d), []string{
		"Insert/ann/queued", "Update/ann/queued", "Submit/ann/queued", "Cancel/ann/queued", "Amend/ann/queued", "Delete/ann/queued",
	}, "jobs")
}

// Rust: jobs.rs::a_later_rejecting_hook_leaves_no_job
func TestALaterRejectingHookLeavesNoJob(t *testing.T) {
	d, reg := jobsSetup(t, enqueuer{job: "app.audit"}, rejecter{})
	// BeforeInsert of the first set enqueued; Validate of the second refuses.
	_, err := db.Insert(bg, d.Pool, reg, "quote", quoteTitled("reject"), sys("ann"))
	wantKind(t, err, db.ErrHook)
	if got := queued(d); len(got) != 0 {
		t.Error(got)
	}
}

// Rust: jobs.rs::a_database_failure_after_the_hooks_leaves_no_job
func TestADatabaseFailureAfterTheHooksLeavesNoJob(t *testing.T) {
	d, reg := jobsSetup(t, enqueuer{job: "app.audit"})
	ann := sys("ann")
	mustInsert(t, d, reg, "ledger", quoteTitled("same"), ann)
	if len(queued(d)) != 1 {
		t.Fatal(queued(d))
	}
	// The second insert runs its hooks (they enqueue) and then violates the unique title.
	_, err := db.Insert(bg, d.Pool, reg, "ledger", quoteTitled("same"), ann)
	wantKind(t, err, db.ErrUniqueViolation)
	if len(queued(d)) != 1 {
		t.Error(queued(d))
	}
}

// Rust: jobs.rs::a_job_name_that_is_not_known_fails_the_operation
func TestAJobNameThatIsNotKnownFailsTheOperation(t *testing.T) {
	d, reg := jobsSetup(t, enqueuer{job: "app.nobody"})
	_, err := db.Insert(bg, d.Pool, reg, "quote", quoteTitled("one"), sys("ann"))
	if e := wantKind(t, err, db.ErrJob); !strings.Contains(e.Message, "app.nobody") {
		t.Error(e.Message)
	}
	if len(queued(d)) != 0 {
		t.Error("a job was written")
	}
	want(t, d.Rows("SELECT count(*)::text FROM doc_quote"), [][]string{{"0"}}, "count")
}

// Rust: jobs.rs::an_oversize_payload_is_refused_by_the_hook_context_and_by_the_queue
func TestAnOversizePayloadIsRefusedByTheHookContextAndByTheQueue(t *testing.T) {
	big := map[string]any{"text": strings.Repeat("x", core.MaxJobPayloadBytes)}
	// The context refuses it at once, so the hook can react.
	jobs := core.NewEnqueuedJobs()
	ctx := core.NewHookContext("quote", "ann", core.OpInsert).WithJobs(jobs)
	if err := ctx.Enqueue("app.audit", big); err == nil {
		t.Error("the context accepted it")
	}
	if got := jobs.Take(); len(got) != 0 {
		t.Error(got)
	}
	// The queue refuses it too (every way of enqueueing is checked).
	d, reg := jobsSetup(t)
	_, err := db.Enqueue(bg, d.Pool, reg, "app.audit", big, "ann")
	wantKind(t, err, db.ErrJob)
	if _, err := db.Enqueue(bg, d.Pool, reg, "app.audit", map[string]any{"text": "a\x00b"}, "ann"); err == nil {
		t.Error("a NUL was accepted")
	}
	if len(queued(d)) != 0 {
		t.Error("a job was written")
	}
	for _, actor := range []string{"", "a\x00b"} {
		_, err := db.Enqueue(bg, d.Pool, reg, "app.audit", map[string]any{}, actor)
		wantKind(t, err, db.ErrInvalidActor)
	}
	if len(queued(d)) != 0 {
		t.Error("a job was written")
	}
}
