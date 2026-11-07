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

// The job queue and the outbox (M6.1) against a real PostgreSQL: jobs that hooks enqueue are written in
// the operation's transaction, and the queue functions claim, complete, fail, release and retry rows with
// the guards and leases of ADR 0013. Ported from crates/forgebase-db/tests/jobs.rs.

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

var knownAudit = []db.KnownJob{{Name: "app.audit", Lease: 60 * time.Second}}

func enqueueOne(t *testing.T, d *dbtest.DB, reg *core.Registry) string {
	t.Helper()
	id, err := db.Enqueue(bg, d.Pool, reg, "app.audit", map[string]any{"n": 1}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func mustClaim(t *testing.T, d *dbtest.DB, worker string) *db.ClaimedJob {
	t.Helper()
	claimed, err := db.ClaimJob(bg, d.Pool, knownAudit, worker)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func claimed(t *testing.T, d *dbtest.DB, worker string) bool {
	t.Helper()
	return mustClaim(t, d, worker) != nil
}

func boolResult(t *testing.T) func(bool, error) bool {
	return func(ok bool, err error) bool {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
}

// Rust: jobs.rs::claim_takes_due_known_jobs_oldest_first_and_counts_the_attempt
func TestClaimTakesDueKnownJobsOldestFirstAndCountsTheAttempt(t *testing.T) {
	d, reg := jobsSetup(t)
	first := enqueueOne(t, d, reg)
	second := enqueueOne(t, d, reg)
	// A future job and a job of a name the worker does not know are not claimed.
	d.Exec("INSERT INTO fb_job (id, name, payload, status, enqueued_by, run_at) VALUES " +
		"(gen_random_uuid(), 'app.audit', '{}', 'queued', 'x', now() + interval '1 hour'), " +
		"(gen_random_uuid(), 'other.job', '{}', 'queued', 'x', now() - interval '1 hour')")

	a := mustClaim(t, d, "w1")
	if a == nil || a.ID.String() != first || a.Attempt != 1 {
		t.Fatalf("first claim: %+v", a)
	}
	if string(a.Payload) != `{"n": 1}` {
		t.Errorf("payload %s", a.Payload)
	}
	if b := mustClaim(t, d, "w1"); b == nil || b.ID.String() != second {
		t.Fatalf("second claim: %+v", b)
	}
	if claimed(t, d, "w1") {
		t.Error("a third job was claimed")
	}
	want(t, d.Rows(fmt.Sprintf("SELECT status, attempts::text, locked_by, (locked_until > now() + interval '50 seconds')::text FROM fb_job WHERE id = '%s'", first)),
		[][]string{{"running", "1", "w1", "true"}}, "row")
	want(t, d.Rows("SELECT count(*)::text FROM fb_job WHERE status = 'queued'"), [][]string{{"2"}}, "queued")
}

// Rust: jobs.rs::an_expired_lease_is_claimed_again_and_a_live_one_is_not
func TestAnExpiredLeaseIsClaimedAgainAndALiveOneIsNot(t *testing.T) {
	d, reg := jobsSetup(t)
	id := enqueueOne(t, d, reg)
	crashed := mustClaim(t, d, "w1")
	if claimed(t, d, "w2") {
		t.Fatal("a live lease was claimed")
	}
	d.Exec("UPDATE fb_job SET locked_until = now() - interval '1 second'")
	again := mustClaim(t, d, "w2")
	if again == nil || again.ID != crashed.ID || again.Attempt != 2 {
		t.Fatalf("again: %+v", again)
	}
	want(t, d.Rows(fmt.Sprintf("SELECT locked_by FROM fb_job WHERE id = '%s'", id)), [][]string{{"w2"}}, "locked_by")
	ok := boolResult(t)
	// The first worker woke up: it holds nothing any more.
	if ok(db.CompleteJob(bg, d.Pool, crashed.ID, "w1", 1)) {
		t.Error("w1 completed")
	}
	if ok(db.CompleteJob(bg, d.Pool, crashed.ID, "w2", 1)) {
		t.Error("w2 completed on the old attempt")
	}
	if !ok(db.CompleteJob(bg, d.Pool, crashed.ID, "w2", 2)) {
		t.Error("w2 did not complete")
	}
}

// Rust: jobs.rs::fail_retries_with_a_delay_or_kills_and_release_gives_the_attempt_back
func TestFailRetriesWithADelayOrKillsAndReleaseGivesTheAttemptBack(t *testing.T) {
	d, reg := jobsSetup(t)
	id := enqueueOne(t, d, reg)
	first := mustClaim(t, d, "w1")
	ok := boolResult(t)

	long := strings.Repeat("e", 5000)
	if !ok(db.FailJob(bg, d.Pool, first.ID, "w1", 1, long, db.Failure{RetryAfter: 30 * time.Second})) {
		t.Fatal("fail")
	}
	want(t, d.Rows(fmt.Sprintf("SELECT status, attempts::text, length(last_error)::text, (locked_by IS NULL)::text, "+
		"(run_at > now() + interval '25 seconds' AND run_at < now() + interval '35 seconds')::text FROM fb_job WHERE id = '%s'", id)),
		[][]string{{"queued", "1", "2000", "true", "true"}}, "retried")
	// A guard that does not hold changes nothing.
	if ok(db.FailJob(bg, d.Pool, first.ID, "w1", 1, "x", db.Failure{Dead: true})) {
		t.Error("an unguarded fail changed the row")
	}

	d.Exec("UPDATE fb_job SET run_at = now()")
	second := mustClaim(t, d, "w1")
	if second.Attempt != 2 {
		t.Fatalf("attempt %d", second.Attempt)
	}
	if !ok(db.ReleaseJob(bg, d.Pool, second.ID, "w1", 2)) {
		t.Fatal("release")
	}
	want(t, d.Rows(fmt.Sprintf("SELECT status, attempts::text FROM fb_job WHERE id = '%s'", id)), [][]string{{"queued", "1"}}, "released")
	third := mustClaim(t, d, "w1")
	if third.Attempt != 2 {
		t.Fatalf("attempt %d", third.Attempt)
	}
	if !ok(db.FailJob(bg, d.Pool, third.ID, "w1", 2, "boom", db.Failure{Dead: true})) {
		t.Fatal("dead")
	}
	want(t, d.Rows(fmt.Sprintf("SELECT status, last_error, (finished_at IS NOT NULL)::text FROM fb_job WHERE id = '%s'", id)),
		[][]string{{"dead", "boom", "true"}}, "dead")
}

// Rust: jobs.rs::list_show_and_retry_serve_the_command_line
func TestListShowAndRetryServeTheCommandLine(t *testing.T) {
	d, reg := jobsSetup(t)
	a := enqueueOne(t, d, reg)
	b := enqueueOne(t, d, reg)
	first := mustClaim(t, d, "w1")
	if ok, err := db.FailJob(bg, d.Pool, first.ID, "w1", 1, "boom", db.Failure{Dead: true}); err != nil || !ok {
		t.Fatal(ok, err)
	}

	ids := func(jobs []db.Job) []string {
		out := []string{}
		for _, j := range jobs {
			out = append(out, j.ID.String())
		}
		return out
	}
	all, err := db.ListJobs(bg, d.Pool, db.JobFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	want(t, ids(all), []string{b, a}, "newest first")
	dead, err := db.ListJobs(bg, d.Pool, db.JobFilter{Status: "dead"}, 50)
	if err != nil || len(dead) != 1 || dead[0].ID != first.ID {
		t.Fatal(dead, err)
	}
	none, err := db.ListJobs(bg, d.Pool, db.JobFilter{Name: "x.y"}, 50)
	if err != nil || len(none) != 0 {
		t.Fatal(none, err)
	}
	if one, err := db.ListJobs(bg, d.Pool, db.JobFilter{}, 1); err != nil || len(one) != 1 {
		t.Fatal(one, err)
	}

	shown, err := db.GetJob(bg, d.Pool, a)
	if err != nil || shown == nil {
		t.Fatal(shown, err)
	}
	want(t, []string{shown.Status, shown.EnqueuedBy}, []string{"dead", "ann"}, "shown")
	if _, err := db.GetJob(bg, d.Pool, "not-a-uuid"); err == nil {
		t.Error("a bad id was accepted")
	} else {
		wantKind(t, err, db.ErrJob)
	}

	queuedID := a
	if first.ID.String() == a {
		queuedID = b
	}
	result, status, err := db.RetryDeadJob(bg, d.Pool, queuedID)
	if err != nil || result != db.RetryNotDead || status != "queued" {
		t.Fatal(result, status, err)
	}
	result, _, err = db.RetryDeadJob(bg, d.Pool, "00000000-0000-7000-8000-000000000000")
	if err != nil || result != db.RetryNotFound {
		t.Fatal(result, err)
	}
	result, _, err = db.RetryDeadJob(bg, d.Pool, first.ID.String())
	if err != nil || result != db.RetryQueued {
		t.Fatal(result, err)
	}
	back, err := db.GetJob(bg, d.Pool, first.ID.String())
	if err != nil || back == nil {
		t.Fatal(back, err)
	}
	if back.Status != "queued" || back.Attempts != 0 || back.LastError != nil || back.FinishedAt != nil {
		t.Errorf("%+v", back)
	}
}
