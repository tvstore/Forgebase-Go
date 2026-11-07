package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The worker against a real PostgreSQL: leases, guards, shutdown, concurrency and the tick. Ported from the
// #[cfg(test)] module of crates/forgebase/src/worker.rs.

const labSeen = "name = \"seen\"\nlabel = \"Seen\"\n[[fields]]\nname = \"job_id\"\ntype = \"data\"\nrequired = true\nunique = true\n"

// lab is one handler for all tests; the payload's sleep_ms says how long it works.
type lab struct {
	mu      sync.Mutex
	running map[string]bool
	clashes atomic.Int32
	runs    atomic.Int32
}

func (l *lab) handler(ctx context.Context, job *JobContext) error {
	l.runs.Add(1)
	id := job.ID()
	// The same job must never run twice at once.
	l.mu.Lock()
	if l.running[id] {
		l.clashes.Add(1)
	}
	l.running[id] = true
	l.mu.Unlock()
	var p struct {
		SleepMS int `json:"sleep_ms"`
		// Stubborn ignores the context: the handler sleeps the whole time.
		Stubborn bool `json:"stubborn"`
		// PanicOnCancel panics when the context is cancelled.
		PanicOnCancel bool `json:"panic_on_cancel"`
	}
	_ = job.DecodePayload(&p)
	_, inserted := job.Documents().Insert(ctx, "seen", core.Document{Values: map[string]core.Value{"job_id": core.Text(id)}})
	if p.Stubborn {
		time.Sleep(time.Duration(p.SleepMS) * time.Millisecond)
	} else {
		select {
		case <-time.After(time.Duration(p.SleepMS) * time.Millisecond):
		case <-ctx.Done():
			if p.PanicOnCancel {
				panic("cancelled")
			}
		}
	}
	l.mu.Lock()
	delete(l.running, id)
	l.mu.Unlock()
	return inserted
}

// logBuffer is a log the worker writes while the test reads it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type labWorld struct {
	t      *testing.T
	d      *dbtest.DB
	pool   *pgxpool.Pool
	lab    *lab
	worker *worker
	log    *logBuffer
}

func workerConfigFor(grace time.Duration) workerConfig {
	return workerConfig{poll: 20 * time.Millisecond, grace: grace, margin: time.Second, abortWait: 300 * time.Millisecond}
}

// setupWith is a worker on a migrated database. No schedule is left in the database unless the test asks for
// one, so a built-in job that falls due while a test runs (at the turn of the hour) cannot add a job to its
// counts.
func setupWith(t *testing.T, grace time.Duration, schedule *Schedule) *labWorld {
	t.Helper()
	l := &lab{running: map[string]bool{}}
	app := NewApp("lab").
		DocTypes(files("seen.toml", labSeen)).
		JobWith("work", l.handler, NewJobOptions().Timeout(3*time.Second))
	if schedule != nil {
		app = app.Schedule("work", *schedule)
	}
	composed, startup := compose([]*App{app}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	d := dbtest.New(t)
	if _, err := composed.Migrate(context.Background(), d.Pool, db.SyncOptions{LockTimeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.ConnectSized(context.Background(), d.URL(), 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	log := &logBuffer{}
	return &labWorld{t: t, d: d, pool: pool, lab: l, log: log,
		worker: newWorker(pool, composed.registry, composed, workerConfigFor(grace), log)}
}

func setupLab(t *testing.T, grace time.Duration) *labWorld {
	t.Helper()
	w := setupWith(t, grace, nil)
	w.d.Exec("DELETE FROM fb_schedule")
	return w
}

func (w *labWorld) enqueue(payload string) {
	w.d.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by) "+
		"VALUES (gen_random_uuid(), 'lab.work', '%s', 'queued', 'test')", payload))
}

func (w *labWorld) one(sql string) []string { return w.d.Rows(sql)[0] }

func (w *labWorld) seen() string { return w.one("SELECT count(*)::text FROM doc_seen")[0] }

func (w *labWorld) runOne(id string) outcome {
	w.t.Helper()
	got, err := w.worker.runOne(context.Background(), id, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// waitRow polls (every 20 ms, for at most 10 s) until sql returns at least one row, and returns the first. A
// test waits for the real precondition with this, never with a fixed sleep.
//
// It may run in a goroutine of the test, so it reports with Errorf and returns an empty row: a failed wait
// must not hang the test.
func (w *labWorld) waitRow(sql string) []string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if rows := w.d.Rows(sql); len(rows) > 0 {
			return rows[0]
		}
		if time.Now().After(deadline) {
			w.t.Errorf("the condition did not hold within 10 s: %s", sql)
			return []string{""}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitHandlerSleeping waits until the job is claimed by worker and its handler has begun its transaction: the
// row is running and locked by worker, and a backend is idle in a transaction (the handler sleeps). It returns
// that backend's pid.
func (w *labWorld) waitHandlerSleeping(worker string) string {
	w.waitRow(fmt.Sprintf("SELECT 1::text FROM fb_job WHERE status = 'running' AND locked_by = '%s'", worker))
	database := w.one("SELECT current_database()")[0]
	return w.waitRow(fmt.Sprintf("SELECT pid::text FROM pg_stat_activity WHERE datname = '%s' AND state = 'idle in transaction'", database))[0]
}

func eqRow(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Rust: worker.rs::only_one_tick_is_due_per_poll_interval_whatever_the_slots
func TestOnlyOneTickIsDuePerPollIntervalWhateverTheSlots(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.worker.config.poll = time.Hour
	// The first slot to ask ticks; the other slots of the interval do not.
	if !w.worker.tickDue() || w.worker.tickDue() || w.worker.tickDue() {
		t.Error("the tick is not due exactly once per interval")
	}
}

// Rust: worker.rs::an_expired_lease_is_recovered_and_the_job_runs
func TestAnExpiredLeaseIsRecoveredAndTheJobRuns(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	// A worker that crashed: running, lease in the past, one attempt used.
	w.d.Exec("INSERT INTO fb_job (id, name, payload, status, attempts, locked_by, locked_until, enqueued_by) " +
		"VALUES (gen_random_uuid(), 'lab.work', '{}', 'running', 1, 'dead-worker', now() - interval '1 minute', 'test')")
	if got := w.runOne("w"); got != outcomeDone {
		t.Fatalf("outcome %v", got)
	}
	if w.lab.runs.Load() != 1 {
		t.Errorf("runs %d", w.lab.runs.Load())
	}
	eqRow(t, w.one("SELECT status, attempts::text, locked_by FROM fb_job"), "done", "2", "w")
}

// Rust: worker.rs::a_live_lease_is_left_alone_and_exhausted_attempts_are_not_run_again
func TestALiveLeaseIsLeftAloneAndExhaustedAttemptsAreNotRunAgain(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.d.Exec("INSERT INTO fb_job (id, name, payload, status, attempts, locked_by, locked_until, enqueued_by) " +
		"VALUES (gen_random_uuid(), 'lab.work', '{}', 'running', 1, 'other', now() + interval '1 minute', 'test')")
	if got := w.runOne("w"); got != outcomeNone {
		t.Fatalf("outcome %v", got)
	}

	// The job already used all 5 attempts (the workers died on each): it is dead, not run.
	w.d.Exec("UPDATE fb_job SET attempts = 5, locked_until = now() - interval '1 second'")
	if got := w.runOne("w"); got != outcomeDead {
		t.Fatalf("outcome %v", got)
	}
	if w.lab.runs.Load() != 0 {
		t.Errorf("runs %d", w.lab.runs.Load())
	}
	eqRow(t, w.one("SELECT status, attempts::text FROM fb_job"), "dead", "6")
}

// Rust: worker.rs::a_lost_lease_rolls_back_the_handler_and_marks_nothing
func TestALostLeaseRollsBackTheHandlerAndMarksNothing(t *testing.T) {
	for _, steal := range []string{
		"UPDATE fb_job SET locked_by = 'thief'",
		"UPDATE fb_job SET attempts = attempts + 1",
	} {
		w := setupLab(t, 5*time.Second)
		w.enqueue(`{"sleep_ms": 2000}`)
		// The handler's backend is idle in transaction (it sleeps); another session takes the job.
		go func() {
			w.waitHandlerSleeping("w")
			w.d.Exec(steal)
		}()
		if got := w.runOne("w"); got != outcomeLeaseLost {
			t.Errorf("%s: outcome %v", steal, got)
		}
		if w.seen() != "0" {
			t.Errorf("%s: the handler's insert was not rolled back", steal)
		}
		eqRow(t, w.one("SELECT status FROM fb_job"), "running")
	}
}

// Rust: worker.rs::a_timed_out_handler_loses_its_connection_instead_of_returning_it_to_the_pool
func TestATimedOutHandlerLosesItsConnectionInsteadOfReturningItToThePool(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.enqueue(`{"sleep_ms": 20000}`)
	// While the handler sleeps its connection is idle in a transaction (it has begun its transaction).
	pids := make(chan string, 1)
	go func() { pids <- w.waitHandlerSleeping("w") }()
	if got := w.runOne("w"); got != outcomeRetried {
		t.Fatalf("outcome %v", got)
	}
	var pid string
	select {
	case pid = <-pids:
	case <-time.After(15 * time.Second):
		t.Fatal("the handler never slept")
	}
	// The backend of the handler is gone: the connection was closed, not pooled.
	deadline := time.Now().Add(5 * time.Second)
	for w.one(fmt.Sprintf("SELECT count(*)::text FROM pg_stat_activity WHERE pid = %s", pid))[0] != "0" {
		if time.Now().After(deadline) {
			t.Fatal("the connection is still open")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if w.seen() != "0" {
		t.Error("the aborted attempt was not rolled back")
	}
	eqRow(t, w.one("SELECT status, attempts::text FROM fb_job"), "queued", "1")
}

func signals() (chan struct{}, <-chan struct{}) {
	c := make(chan struct{}, 4)
	return c, c
}

// Rust: worker.rs::four_slots_run_200_jobs_exactly_once_each
func TestFourSlotsRun200JobsExactlyOnceEach(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.d.Exec("INSERT INTO fb_job (id, name, payload, status, enqueued_by) " +
		"SELECT gen_random_uuid(), 'lab.work', jsonb_build_object('sleep_ms', 5, 'n', n), 'queued', 'test' " +
		"FROM generate_series(1, 200) AS n")
	// Two workers with two slots each, on one pool.
	stopA, signalsA := signals()
	stopB, signalsB := signals()
	resultA, resultB := make(chan bool, 1), make(chan bool, 1)
	go func() { resultA <- w.worker.runWorker(context.Background(), 2, "a", signalsA) }()
	go func() { resultB <- w.worker.runWorker(context.Background(), 2, "b", signalsB) }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		done := w.one("SELECT count(*)::text FROM fb_job WHERE status = 'done'")[0]
		if done == "200" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %s of 200 jobs are done", done)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopA <- struct{}{}
	stopB <- struct{}{}
	if !<-resultA || !<-resultB {
		t.Error("a slot panicked")
	}
	if w.lab.runs.Load() != 200 {
		t.Errorf("runs %d, every job ran exactly once", w.lab.runs.Load())
	}
	if w.lab.clashes.Load() != 0 {
		t.Error("a job ran twice at once")
	}
	eqRow(t, w.one("SELECT count(*)::text FROM fb_job WHERE attempts <> 1 OR status <> 'done'"), "0")
	if w.seen() != "200" {
		t.Errorf("seen %s: no unique violation, no lost insert", w.seen())
	}
}

// Rust: worker.rs::the_first_signal_lets_a_running_job_finish_within_the_grace_period
func TestTheFirstSignalLetsARunningJobFinishWithinTheGracePeriod(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.enqueue(`{"sleep_ms": 1500}`)
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 1, "w", receiver) }()
	w.waitHandlerSleeping("w/0")
	stop <- struct{}{}
	if !<-result {
		t.Error("a slot panicked")
	}
	eqRow(t, w.one("SELECT status, attempts::text FROM fb_job"), "done", "1")
	if w.seen() != "1" {
		t.Errorf("seen %s", w.seen())
	}
}

// Rust: worker.rs::a_job_still_running_after_the_grace_period_is_released
func TestAJobStillRunningAfterTheGracePeriodIsReleased(t *testing.T) {
	w := setupLab(t, 200*time.Millisecond)
	// The job's own timeout is 3 s, longer than the grace period.
	w.enqueue(`{"sleep_ms": 20000}`)
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 1, "w", receiver) }()
	w.waitHandlerSleeping("w/0")
	stop <- struct{}{}
	started := time.Now()
	if !<-result {
		t.Error("a slot panicked")
	}
	if time.Since(started) > 5*time.Second {
		t.Error("the worker took too long to stop")
	}
	eqRow(t, w.one("SELECT status, attempts::text, (locked_by IS NULL)::text, (locked_until IS NULL)::text FROM fb_job"),
		"queued", "0", "true", "true")
	if w.seen() != "0" {
		t.Error("the aborted attempt was not rolled back")
	}
}

// Rust: worker.rs::a_second_signal_exits_at_once_and_the_lease_covers_the_job
func TestASecondSignalExitsAtOnceAndTheLeaseCoversTheJob(t *testing.T) {
	w := setupLab(t, time.Minute)
	w.enqueue(`{"sleep_ms": 20000}`)
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 1, "w", receiver) }()
	w.waitHandlerSleeping("w/0")
	stop <- struct{}{}
	stop <- struct{}{}
	started := time.Now()
	if !<-result {
		t.Error("a slot panicked")
	}
	if time.Since(started) > 5*time.Second {
		t.Error("the worker took too long to exit")
	}
	// Nobody released it: it is still running, with a lease that will expire.
	eqRow(t, w.one("SELECT status FROM fb_job"), "running")
	if !strings.Contains(w.log.String(), "exiting now (second signal)") {
		t.Errorf("%s", w.log.String())
	}
}

// Rust: worker.rs::a_worker_enqueues_a_due_schedule_once_and_runs_it
func TestAWorkerEnqueuesADueScheduleOnceAndRunsIt(t *testing.T) {
	every := EveryMinutes(1)
	w := setupWith(t, 5*time.Second, &every)
	// Only the app's schedule is left, and it is due (set in the past by SQL, so the test does not wait for a
	// clock).
	w.d.Exec("DELETE FROM fb_schedule WHERE name <> 'lab.work'")
	w.d.Exec("UPDATE fb_schedule SET next_run_at = date_trunc('minute', now()) - interval '3 minutes'")
	due := w.one(`SELECT to_char(next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM fb_schedule`)[0]
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 2, "w", receiver) }()
	// The precondition is the job being done, not elapsed time.
	w.waitRow(fmt.Sprintf("SELECT 1::text FROM fb_job WHERE name = 'lab.work' AND status = 'done' "+
		"AND payload->>'scheduled_for' = '%s' AND enqueued_by = 'scheduler'", due))
	stop <- struct{}{}
	if !<-result {
		t.Error("a slot panicked")
	}
	// One slot ticked per poll interval, and the due time was enqueued once. (The next tick, a minute boundary
	// later, may or may not have come in the meantime: it has another due time.)
	eqRow(t, w.one(fmt.Sprintf("SELECT count(*)::text FROM fb_job WHERE payload->>'scheduled_for' = '%s'", due)), "1")
	if w.lab.runs.Load() < 1 {
		t.Error("the job did not run")
	}
	eqRow(t, w.one("SELECT (last_run_at IS NOT NULL)::text FROM fb_schedule"), "true")
	if !strings.Contains(w.log.String(), "worker: started w: 5 job type(s), concurrency 2") {
		t.Errorf("%s", w.log.String())
	}
}

// Not in Rust (a handler cannot be aborted in Go): a handler that ignores its context is abandoned after the
// grace period and the abort wait; the job is given back and the worker says the pool cannot be closed in peace.
func TestAHandlerThatIgnoresItsContextIsAbandonedAndTheJobIsReleased(t *testing.T) {
	w := setupLab(t, 200*time.Millisecond)
	w.enqueue(`{"sleep_ms": 6000, "stubborn": true}`)
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 1, "w", receiver) }()
	w.waitHandlerSleeping("w/0")
	stop <- struct{}{}
	started := time.Now()
	if !<-result {
		t.Error("a slot panicked")
	}
	if time.Since(started) > 3*time.Second {
		t.Errorf("the worker took %s to stop", time.Since(started))
	}
	if !w.worker.leftBehind() {
		t.Error("the abandoned handler was not noticed")
	}
	eqRow(t, w.one("SELECT status, attempts::text FROM fb_job"), "queued", "0")
}

// Not in Rust: a second signal does not wait for a handler that ignores its context.
func TestASecondSignalDoesNotWaitForAHandlerThatIgnoresItsContext(t *testing.T) {
	w := setupLab(t, time.Minute)
	w.worker.config.abortWait = time.Minute
	w.enqueue(`{"sleep_ms": 6000, "stubborn": true}`)
	stop, receiver := signals()
	result := make(chan bool, 1)
	go func() { result <- w.worker.runWorker(context.Background(), 1, "w", receiver) }()
	w.waitHandlerSleeping("w/0")
	stop <- struct{}{}
	stop <- struct{}{}
	started := time.Now()
	<-result
	if time.Since(started) > 2*time.Second {
		t.Errorf("the worker took %s to exit", time.Since(started))
	}
	if !w.worker.leftBehind() {
		t.Error("the hard stop was not noticed")
	}
	eqRow(t, w.one("SELECT status FROM fb_job"), "running")
}

// Not in Rust: a handler that panics because it was aborted is an aborted attempt (the timeout is recorded), not
// a panic failure.
func TestAPanicAfterTheAbortIsTheTimeoutNotAPanic(t *testing.T) {
	w := setupLab(t, 5*time.Second)
	w.worker.jobs["lab.work"].options = NewJobOptions().Timeout(300 * time.Millisecond)
	w.enqueue(`{"sleep_ms": 20000, "panic_on_cancel": true}`)
	if got := w.runOne("w"); got != outcomeRetried {
		t.Fatalf("outcome %v", got)
	}
	row := w.one("SELECT status, last_error FROM fb_job")
	if row[0] != "queued" || !strings.HasPrefix(row[1], "timed out after 0.3 s") {
		t.Errorf("%q", row)
	}
}
