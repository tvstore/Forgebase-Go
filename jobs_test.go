package forgebase_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Background jobs through the public API (M6.1, ADR 0013), with forgebase/testing: the outbox from a hook over
// HTTP, delivery, retry and dead jobs, the documents handle, jobs that enqueue jobs, and the startup errors.
// Ported from crates/forgebase/tests/jobs.rs. The worker's own behaviour (leases, shutdown, concurrency) is
// tested next to its code, in internal/app/worker_test.go.

const jobNote = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "qty"
type = "int"
`

const jobInvoice = `
name = "invoice"
label = "Invoice"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
`

const jobTicket = `
name = "ticket"
label = "Ticket"
[[fields]]
name = "title"
type = "data"
required = true

[[permissions]]
role = "Agent"
read = true
write = true
create = true
`

func titled(title string) forgebase.Document {
	return forgebase.Document{Values: map[string]forgebase.Value{"title": forgebase.Text(title)}}
}

// behavior is what a test job does. One handler with a behavior keeps the tests short.
type behavior int

const (
	// insertNote inserts a note titled after the payload's title, then succeeds.
	insertNote behavior = iota
	// insertThenFail inserts a note, then fails with a retryable error.
	insertThenFail
	// permanent fails with a permanent error.
	permanent
	// insertInvalid inserts a note without its required title.
	insertInvalid
	// doPanic panics.
	doPanic
	// insertThenSleep inserts a note, then sleeps for a long time (until its context is cancelled).
	insertThenSleep
	// tour uses every operation of the documents handle on an invoice.
	tour
)

// enqueueJob enqueues the named job with the payload {"title": "child"}, then succeeds, or fails when
// thenFail.
func enqueueJob(name string, thenFail bool) forgebase.JobHandler {
	return func(ctx context.Context, job *forgebase.JobContext) error {
		if err := job.Enqueue(ctx, name, map[string]string{"title": "child"}); err != nil {
			return err
		}
		if thenFail {
			return forgebase.NewJobError("after enqueue")
		}
		return nil
	}
}

func script(b behavior) forgebase.JobHandler {
	return func(ctx context.Context, job *forgebase.JobContext) error {
		switch b {
		case insertNote:
			var p struct {
				Title string `json:"title"`
			}
			title := "untitled"
			if err := job.DecodePayload(&p); err == nil && p.Title != "" {
				title = p.Title
			}
			_, err := job.Documents().Insert(ctx, "note", titled(title))
			return err
		case insertThenFail:
			if _, err := job.Documents().Insert(ctx, "note", titled("rolled back")); err != nil {
				return err
			}
			return forgebase.NewJobError("boom")
		case permanent:
			return forgebase.NewPermanentJobError("never works")
		case insertInvalid:
			_, err := job.Documents().Insert(ctx, "note", forgebase.Document{})
			return err
		case doPanic:
			panic("handler bug")
		case insertThenSleep:
			if _, err := job.Documents().Insert(ctx, "note", titled("slow")); err != nil {
				return err
			}
			select {
			case <-time.After(30 * time.Second):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case tour:
			return runTour(ctx, job)
		}
		return nil
	}
}

func runTour(ctx context.Context, job *forgebase.JobContext) error {
	docs := job.Documents()
	check := func(ok bool, format string, args ...any) error {
		if !ok {
			return forgebase.NewPermanentJobError("tour: " + fmt.Sprintf(format, args...))
		}
		return nil
	}
	stored, err := docs.Insert(ctx, "invoice", titled("inv"))
	if err != nil {
		return err
	}
	if err := check(stored.Version == 1 && stored.DocStatus != nil && *stored.DocStatus == forgebase.DocDraft, "insert %+v", stored); err != nil {
		return err
	}
	name := stored.Name

	got, err := docs.Get(ctx, "invoice", name)
	if err != nil {
		return err
	}
	if err := check(got != nil && got.Document.Values["title"].Equal(forgebase.Text("inv")), "get %+v", got); err != nil {
		return err
	}
	if missing, err := docs.Get(ctx, "invoice", "nobody"); err != nil || missing != nil {
		return check(false, "a missing document: %v, %v", missing, err)
	}

	updated, err := docs.Update(ctx, "invoice", name, 1, forgebase.Changes{Values: map[string]forgebase.Value{"title": forgebase.Text("inv 2")}})
	if err != nil {
		return err
	}
	if err := check(updated.Version == 2, "update version %d", updated.Version); err != nil {
		return err
	}

	// A stale version is a permanent error, and the job's transaction stays usable.
	_, err = docs.Update(ctx, "invoice", name, 1, forgebase.Changes{Values: map[string]forgebase.Value{"title": forgebase.Text("x")}})
	var de *forgebase.DocError
	if !errors.As(err, &de) || de.IsRetryable() {
		return check(false, "a stale version: %v", err)
	}

	found, err := docs.List(ctx, "invoice", forgebase.NewListQuery().
		Filter("title", forgebase.FilterLike, forgebase.Text("inv%")).OrderBy("name", true).Limit(5))
	if err != nil {
		return err
	}
	if err := check(len(found) == 1, "list %d", len(found)); err != nil {
		return err
	}
	_, err = docs.List(ctx, "invoice", forgebase.NewListQuery().Filter("title", forgebase.FilterLike, forgebase.Int(1)))
	if !errors.As(err, &de) || de.IsRetryable() {
		return check(false, "a like filter on an int: %v", err)
	}

	submitted, err := docs.Submit(ctx, "invoice", name, 2)
	if err != nil {
		return err
	}
	if err := check(*submitted.DocStatus == forgebase.DocSubmitted, "submit"); err != nil {
		return err
	}
	cancelled, err := docs.Cancel(ctx, "invoice", name, 3)
	if err != nil {
		return err
	}
	if err := check(*cancelled.DocStatus == forgebase.DocCancelled, "cancel"); err != nil {
		return err
	}
	amended, err := docs.Amend(ctx, "invoice", name, 4)
	if err != nil {
		return err
	}
	if err := check(*amended.DocStatus == forgebase.DocDraft, "amend"); err != nil {
		return err
	}
	return docs.Delete(ctx, "invoice", amended.Name, amended.Version)
}

// ticketHooks asks for support.notify for every ticket; the title bad asks for a job that does not exist.
type ticketHooks struct{ forgebase.BaseHooks }

func (ticketHooks) BeforeInsert(ctx forgebase.HookContext, doc *forgebase.Document) error {
	title, ok := doc.Values["title"]
	if !ok || title.Kind != forgebase.KindText {
		return nil
	}
	job := "support.notify"
	if title.Text == "bad" {
		job = "support.nobody"
	}
	return ctx.Enqueue(job, map[string]string{"title": title.Text})
}

type supportJob struct {
	name    string
	handler forgebase.JobHandler
	options forgebase.JobOptions
}

func scripted(name string, b behavior) supportJob {
	return supportJob{name, script(b), forgebase.NewJobOptions()}
}

func support(jobs ...supportJob) *forgebase.Binary {
	app := forgebase.NewApp("support").
		DocTypes(docTypes("doctypes/note.toml", jobNote, "doctypes/invoice.toml", jobInvoice, "doctypes/ticket.toml", jobTicket)).
		Hooks("ticket", ticketHooks{})
	for _, j := range jobs {
		app = app.JobWith(j.name, j.handler, j.options)
	}
	return forgebase.New().App(app)
}

func ready(t *testing.T, b *forgebase.Binary) *fbtest.DB {
	t.Helper()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatal(err)
	}
	return db
}

func queueJob(db *fbtest.DB, name, payload string) {
	db.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by) "+
		"VALUES (gen_random_uuid(), '%s', '%s', 'queued', 'test')", name, payload))
}

func jobRow(t *testing.T, db *fbtest.DB) []string {
	t.Helper()
	rows := db.Rows("SELECT status, attempts::text, coalesce(last_error, '-') FROM fb_job ORDER BY id")
	if len(rows) != 1 {
		t.Fatalf("%v", rows)
	}
	return rows[0]
}

func noteTitles(db *fbtest.DB) []string {
	out := []string{}
	for _, r := range db.Rows("SELECT title FROM doc_note ORDER BY title") {
		out = append(out, r[0])
	}
	return out
}

func counts(run fbtest.JobRun) [3]int { return [3]int{run.Done, run.Retried, run.Dead} }

func expect(t *testing.T, got, want any) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

// ---- the outbox, over HTTP ------------------------------------------------------------------

// Rust: jobs.rs::a_hook_enqueues_over_http_and_the_job_runs_with_its_documents
func TestAHookEnqueuesOverHTTPAndTheJobRunsWithItsDocuments(t *testing.T) {
	binary := support(scripted("notify", insertNote))
	db := ready(t, binary)
	db.CreateUser("ann", "Agent")
	ann := db.Client(binary).Login("ann")

	reply := ann.Post("/api/v1/resource/ticket", map[string]any{"title": "printer"})
	if reply.Status != 201 {
		t.Fatalf("%d %v", reply.Status, reply.Body)
	}
	expect(t, db.Rows("SELECT name, status, enqueued_by, payload::text FROM fb_job"),
		[][]string{{"support.notify", "queued", "ann", `{"title": "printer"}`}})

	run := fbtest.RunJobs(binary, db)
	expect(t, counts(run), [3]int{1, 0, 0})
	expect(t, noteTitles(db), []string{"printer"})
	// The handler's insert is attributed to the job.
	expect(t, db.Rows("SELECT owner, modified_by FROM doc_note"), [][]string{{"job:support.notify", "job:support.notify"}})
	expect(t, jobRow(t, db), []string{"done", "1", "-"})
}

// Rust: jobs.rs::a_hook_that_enqueues_an_unknown_job_fails_the_request_and_writes_nothing
func TestAHookThatEnqueuesAnUnknownJobFailsTheRequestAndWritesNothing(t *testing.T) {
	binary := support(scripted("notify", insertNote))
	db := ready(t, binary)
	db.CreateUser("ann", "Agent")
	ann := db.Client(binary).Login("ann")
	reply := ann.Post("/api/v1/resource/ticket", map[string]any{"title": "bad"})
	if reply.Status != 500 || reply.ErrorCode() != "internal" {
		t.Fatalf("%d %v", reply.Status, reply.Body)
	}
	expect(t, db.Rows("SELECT count(*)::text FROM doc_ticket"), [][]string{{"0"}})
	expect(t, db.Rows("SELECT count(*)::text FROM fb_job"), [][]string{{"0"}})
}

// ---- delivery -------------------------------------------------------------------------------

// Rust: jobs.rs::a_failed_attempt_rolls_back_and_retries_after_the_backoff
func TestAFailedAttemptRollsBackAndRetriesAfterTheBackoff(t *testing.T) {
	binary := support(scripted("flaky", insertThenFail))
	db := ready(t, binary)
	queueJob(db, "support.flaky", "{}")

	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	if got := noteTitles(db); len(got) != 0 {
		t.Errorf("the handler's insert was not rolled back: %v", got)
	}
	expect(t, jobRow(t, db), []string{"queued", "1", "boom"})
	// The default backoff is 30 s; the clock is the database's.
	expect(t, db.Rows("SELECT (run_at BETWEEN now() + interval '25 seconds' AND now() + interval '35 seconds')::text FROM fb_job"),
		[][]string{{"true"}})
	// It is not due, so another call runs nothing.
	if fbtest.RunJobs(binary, db).Retried != 0 {
		t.Error("a job that is not due ran")
	}
}

// Rust: jobs.rs::a_job_is_dead_after_its_attempts_and_a_permanent_error_after_one
func TestAJobIsDeadAfterItsAttemptsAndAPermanentErrorAfterOne(t *testing.T) {
	options := forgebase.NewJobOptions().MaxAttempts(2).Backoff(time.Hour, time.Hour)
	binary := support(
		supportJob{"flaky", script(insertThenFail), options},
		scripted("never", permanent),
	)
	db := ready(t, binary)
	queueJob(db, "support.flaky", "{}")
	run := fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Retried, run.Dead}, [2]int{1, 0})
	db.Exec("UPDATE fb_job SET run_at = now()")
	run = fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Retried, run.Dead}, [2]int{0, 1})
	expect(t, jobRow(t, db), []string{"dead", "2", "boom"})

	db.Exec("DELETE FROM fb_job")
	queueJob(db, "support.never", "{}")
	run = fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Retried, run.Dead}, [2]int{0, 1})
	expect(t, jobRow(t, db), []string{"dead", "1", "never works"})
}

// Rust: jobs.rs::a_validation_error_of_the_documents_handle_is_permanent
func TestAValidationErrorOfTheDocumentsHandleIsPermanent(t *testing.T) {
	binary := support(scripted("invalid", insertInvalid))
	db := ready(t, binary)
	queueJob(db, "support.invalid", "{}")
	run := fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Retried, run.Dead}, [2]int{0, 1})
	row := jobRow(t, db)
	expect(t, row[:2], []string{"dead", "1"})
	if !strings.Contains(row[2], "validation failed") {
		t.Errorf("%v", row)
	}
}

// Rust: jobs.rs::a_panic_is_a_retryable_failure_and_the_run_goes_on
func TestAPanicIsARetryableFailureAndTheRunGoesOn(t *testing.T) {
	binary := support(scripted("crash", doPanic), scripted("notify", insertNote))
	db := ready(t, binary)
	queueJob(db, "support.crash", "{}")
	queueJob(db, "support.notify", `{"title": "after"}`)
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{1, 1, 0})
	expect(t, db.Rows("SELECT status, last_error FROM fb_job WHERE name = 'support.crash'"),
		[][]string{{"queued", "panicked: handler bug"}})
	expect(t, noteTitles(db), []string{"after"})
}

// Rust: jobs.rs::a_timeout_fails_the_attempt_and_rolls_back_its_effects
func TestATimeoutFailsTheAttemptAndRollsBackItsEffects(t *testing.T) {
	options := forgebase.NewJobOptions().Timeout(300 * time.Millisecond)
	binary := support(supportJob{"slow", script(insertThenSleep), options})
	db := ready(t, binary)
	queueJob(db, "support.slow", "{}")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	if got := noteTitles(db); len(got) != 0 {
		t.Errorf("the effects were not rolled back: %v", got)
	}
	row := jobRow(t, db)
	expect(t, row[:2], []string{"queued", "1"})
	if !strings.HasPrefix(row[2], "timed out after") {
		t.Errorf("%v", row)
	}
}

// Rust: jobs.rs::a_worker_leaves_jobs_it_does_not_know_alone
func TestAWorkerLeavesJobsItDoesNotKnowAlone(t *testing.T) {
	binary := support(scripted("notify", insertNote))
	db := ready(t, binary)
	queueJob(db, "elsewhere.thing", "{}")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 0, 0})
	expect(t, jobRow(t, db), []string{"queued", "0", "-"})
}

// ---- jobs that enqueue jobs and use documents ------------------------------------------------

// Rust: jobs.rs::a_job_enqueues_a_job_in_its_own_transaction
func TestAJobEnqueuesAJobInItsOwnTransaction(t *testing.T) {
	binary := support(
		supportJob{"parent", enqueueJob("support.child", false), forgebase.NewJobOptions()},
		scripted("child", insertNote),
		supportJob{"failing", enqueueJob("support.child", true), forgebase.NewJobOptions()},
		supportJob{"unknown", enqueueJob("support.nobody", false), forgebase.NewJobOptions()},
	)
	db := ready(t, binary)

	// The parent completes: the child exists and runs in the same call.
	queueJob(db, "support.parent", "{}")
	if run := fbtest.RunJobs(binary, db); run.Done != 2 {
		t.Fatalf("%+v", run)
	}
	expect(t, noteTitles(db), []string{"child"})
	expect(t, db.Rows("SELECT enqueued_by FROM fb_job WHERE name = 'support.child'"), [][]string{{"job:support.parent"}})

	// A job that fails after enqueueing leaves no child behind.
	db.Exec("DELETE FROM fb_job; DELETE FROM doc_note")
	queueJob(db, "support.failing", "{}")
	run := fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Done, run.Retried}, [2]int{0, 1})
	expect(t, db.Rows("SELECT count(*)::text FROM fb_job"), [][]string{{"1"}})

	// An unknown name is a permanent error.
	db.Exec("DELETE FROM fb_job")
	queueJob(db, "support.unknown", "{}")
	run = fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Retried, run.Dead}, [2]int{0, 1})
	if row := jobRow(t, db); !strings.Contains(row[2], "support.nobody") {
		t.Errorf("%v", row)
	}
}

// Rust: jobs.rs::the_documents_handle_does_every_operation
func TestTheDocumentsHandleDoesEveryOperation(t *testing.T) {
	binary := support(scripted("tour", tour))
	db := ready(t, binary)
	queueJob(db, "support.tour", "{}")
	run := fbtest.RunJobs(binary, db)
	if counts(run) != [3]int{1, 0, 0} {
		t.Fatalf("%+v %v", run, db.Rows("SELECT status, last_error FROM fb_job"))
	}
	// The cancelled original is left; the amendment was deleted.
	expect(t, db.Rows("SELECT docstatus::text, owner FROM doc_invoice"), [][]string{{"2", "job:support.tour"}})
}

// ---- startup errors --------------------------------------------------------------------------

func noJob(context.Context, *forgebase.JobContext) error { return nil }

// Rust: jobs.rs::job_mistakes_are_startup_errors
func TestJobMistakesAreStartupErrors(t *testing.T) {
	badOptions := forgebase.NewJobOptions().MaxAttempts(0).Timeout(0).Backoff(9*time.Second, time.Second)
	app := forgebase.NewApp("shop").
		Job("Bad-Name", noJob).
		Job("fine", noJob).
		Job("fine", noJob).
		JobWith("tuned", noJob, badOptions)
	err := forgebase.New().App(app).Check()
	problems := startupProblems(t, err)
	if len(problems) != 5 {
		t.Fatalf("%q", problems)
	}
	for i, part := range []string{
		`job name "Bad-Name"`,
		"job `fine` is registered more than once",
		"job `tuned`: max_attempts must be at least 1",
		"timeout must be more than zero",
		"backoff cap",
	} {
		if !strings.Contains(problems[i], part) {
			t.Errorf("problem %d: %q does not contain %q", i, problems[i], part)
		}
	}
	if err := forgebase.New().App(forgebase.NewApp("shop").Job("fine", noJob)).Check(); err != nil {
		t.Errorf("a valid job: %v", err)
	}
	// The same name in another app is fine.
	two := forgebase.New().App(forgebase.NewApp("a").Job("fine", noJob)).App(forgebase.NewApp("b").Job("fine", noJob))
	if err := two.Check(); err != nil {
		t.Errorf("the same job name in two apps: %v", err)
	}
	// Go has nil: a job without a handler is a startup error too (Rust cannot build one).
	err = forgebase.New().App(forgebase.NewApp("shop").Job("none", nil)).Check()
	if got := startupProblems(t, err); len(got) != 1 || !strings.Contains(got[0], "job `none` has no handler") {
		t.Errorf("%q", got)
	}
}

func startupProblems(t *testing.T, err error) []string {
	t.Helper()
	var startup *forgebase.StartupError
	if !errors.As(err, &startup) {
		t.Fatalf("not a startup error: %v", err)
	}
	return startup.Problems()
}

// Rust: jobs.rs::a_hook_context_made_by_hand_cannot_enqueue_until_it_has_a_collector
func TestAHookContextMadeByHandCannotEnqueueUntilItHasACollector(t *testing.T) {
	ctx := forgebase.NewHookContext("ticket", "ann", forgebase.OpInsert)
	if err := ctx.Enqueue("support.notify", nil); err == nil {
		t.Error("a context without a collector enqueued")
	}
	jobs := forgebase.NewEnqueuedJobs()
	ctx = ctx.WithJobs(jobs)
	doc := titled("printer")
	if err := (ticketHooks{}).BeforeInsert(ctx, &doc); err != nil {
		t.Fatal(err)
	}
	taken := jobs.Take()
	if len(taken) != 1 || taken[0].Name() != "support.notify" {
		t.Fatalf("%v", taken)
	}
	var payload map[string]string
	if err := json.Unmarshal(taken[0].Payload(), &payload); err != nil || payload["title"] != "printer" {
		t.Errorf("%s %v", taken[0].Payload(), err)
	}
}
