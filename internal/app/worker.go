package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The job worker: claims jobs, runs their handlers and records the outcome (ADR 0013; the Rust module is
// worker.rs). The queue storage and its SQL are in internal/db (jobs.go); this file runs the handlers.
//
// One attempt, in order:
//
//  1. Claim (db.ClaimJob): one statement, committed at once, so the lease is held while the handler runs.
//     Only job names this binary knows are claimed.
//  2. Run: a goroutine takes a connection, opens a transaction on it, runs the handler in it and, if the
//     handler returned nil, marks the job done in the same transaction with a guard (still running, still
//     locked by this worker, same attempt) before it commits. When the guard finds nothing, the lease was
//     lost to another worker; the transaction is rolled back, so the handler's effects and the completion
//     commit together or not at all.
//  3. Record a failure (an error, a timeout or a panic) in a new short statement with the same guard: a retry
//     after the backoff, or dead for a permanent error or when the attempts are used up.
//
// A handler that runs past its timeout is aborted: its context is cancelled, its transaction is never
// committed and its connection is closed, not returned to the pool, because a query may still be running on
// it. A handler that panics ends its attempt; the worker records "panicked" and goes on. (Go cannot stop a
// goroutine, so a handler that ignores its context keeps running until it returns; see JobHandler.)
//
// The lease is the job's timeout plus a margin (leaseMargin). A worker that dies leaves the row running; after
// the lease expires another worker claims it again (the died attempt counts as an attempt).

const (
	// pollInterval is how often an idle worker looks for jobs.
	pollInterval = time.Second
	// workerGrace is how long running jobs may finish after the first stop signal; a job still running
	// then is aborted and released (queued again, without counting the attempt).
	workerGrace = 30 * time.Second
	// leaseMargin is added to a job's timeout to make its lease, so that a job that ends exactly at its
	// timeout still holds the lease while its failure is recorded.
	leaseMargin = 30 * time.Second
	// abortWait is how long the worker waits for an aborted handler to return before it records the outcome
	// without it. The handler cannot commit after the abort whenever it returns.
	abortWait = 5 * time.Second
)

// workerConfig are the times a worker uses. The defaults are the constants above; a test sets shorter ones.
type workerConfig struct {
	poll   time.Duration
	grace  time.Duration
	margin time.Duration
	// abortWait is how long the worker waits for an aborted handler to return.
	abortWait time.Duration
}

func defaultWorkerConfig() workerConfig {
	return workerConfig{poll: pollInterval, grace: workerGrace, margin: leaseMargin, abortWait: abortWait}
}

// outcome is what became of one claimed job.
type outcome int

const (
	// outcomeNone: nothing was due.
	outcomeNone outcome = iota
	// outcomeDone: the handler succeeded and the job is done.
	outcomeDone
	// outcomeRetried: the attempt failed and the job is queued again after a delay.
	outcomeRetried
	// outcomeDead: the job is dead.
	outcomeDead
	// outcomeLeaseLost: the lease was lost during the attempt: nothing was changed by this worker.
	outcomeLeaseLost
	// outcomeReleased: the worker was stopping: the job is queued again, the attempt not counted.
	outcomeReleased
	// outcomeInterrupted: the worker was told to exit at once; nothing was recorded, the lease covers the job.
	outcomeInterrupted
)

// finish is what a started attempt ends with.
type finish struct {
	kind     finishKind
	err      *JobError
	panicked bool
}

type finishKind int

const (
	finishDone finishKind = iota + 1
	finishLeaseLost
	finishFailed
	// finishAborted: the attempt was aborted; it changed nothing.
	finishAborted
)

// worker is the jobs of the binary and what it needs to run them.
type worker struct {
	pool *pgxpool.Pool
	// registry is the registry of the site. Live reload of custom fields is P7: this is the seam, an
	// attempt takes one snapshot when it starts and keeps it to its end, like a request.
	registry  *core.Registry
	jobs      map[string]*jobDef
	schedules map[string]db.Spec
	config    workerConfig
	out       io.Writer
	logMu     sync.Mutex

	tickMu   sync.Mutex
	lastTick time.Time

	// abandoned is set when an aborted handler did not return within abortWait, and hardStopped by the second
	// signal: in both cases a connection may stay acquired by a goroutine nobody can stop, so the caller must
	// not wait for the pool to close (see leftBehind).
	abandoned   atomic.Bool
	hardStopped atomic.Bool
}

// leftBehind is true when an attempt may still hold a connection of the pool: pgxpool's Close waits for every
// acquired connection, so the caller closes the pool with a bound (or not at all) before it exits.
func (w *worker) leftBehind() bool { return w.abandoned.Load() || w.hardStopped.Load() }

func newWorker(pool *pgxpool.Pool, registry *core.Registry, c *Composed, config workerConfig, out io.Writer) *worker {
	return &worker{pool: pool, registry: registry, jobs: c.jobs, schedules: c.schedules, config: config, out: out}
}

// snapshot is the registry an attempt uses.
func (w *worker) snapshot() *core.Registry { return w.registry }

// log writes one plain line on standard error, like the other commands of the binary.
func (w *worker) log(message string) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	_, _ = io.WriteString(w.out, "worker: "+message+"\n")
}

// tickDue is true for the first caller in each poll interval (and records it), false for the others.
func (w *worker) tickDue() bool {
	w.tickMu.Lock()
	defer w.tickMu.Unlock()
	now := time.Now()
	if !w.lastTick.IsZero() && now.Sub(w.lastTick) < w.config.poll {
		return false
	}
	w.lastTick = now
	return true
}

// tick is one scheduler tick: it enqueues the jobs of the due schedules, once however many workers run
// (db.Tick). at replaces the database's now() (tests only).
func (w *worker) tick(ctx context.Context, at *time.Time) (int, error) {
	return db.Tick(ctx, w.pool, w.snapshot(), w.schedules, at)
}

// secs is a duration in seconds as Rust prints an f64: 30, 0.2, 1.5.
func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

// attempt is one running attempt of a job: a goroutine with the handler and its transaction.
type attempt struct {
	mu     sync.Mutex
	state  attemptState
	cancel context.CancelFunc
	done   chan finish
}

type attemptState int

const (
	attemptRunning attemptState = iota
	// attemptCommitting: the handler returned and the completion is being written; it can no longer be
	// aborted.
	attemptCommitting
	attemptAborted
)

// beginCommit moves the attempt to committing, unless it was aborted.
func (a *attempt) beginCommit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == attemptAborted {
		return false
	}
	a.state = attemptCommitting
	return true
}

// abort stops the attempt and waits (a bounded time) for its goroutine, which closes the connection. It
// returns the result when the attempt had finished for real before the abort took effect (the same
// instant-before-the-abort rule as Rust: believe its result), and nil when it was aborted.
func (a *attempt) abort(wait time.Duration, abandoned *atomic.Bool) *finish {
	a.mu.Lock()
	if a.state == attemptRunning {
		a.state = attemptAborted
		a.cancel()
	}
	a.mu.Unlock()
	select {
	case f := <-a.done:
		if f.kind == finishAborted {
			return nil
		}
		return &f
	case <-time.After(wait):
		// The handler ignores its context: its goroutine and connection stay with it.
		abandoned.Store(true)
		return nil
	}
}

// runOne claims one job and runs it to its outcome. outcomeNone when nothing is due. id is the worker id
// recorded in locked_by. stop is closed by the first stop signal (nil: never); ctx is cancelled by the
// second one, which aborts the attempt without recording anything.
func (w *worker) runOne(ctx context.Context, id string, stop <-chan struct{}) (outcome, error) {
	known := make([]db.KnownJob, 0, len(w.jobs))
	for _, def := range w.jobs {
		known = append(known, db.KnownJob{Name: def.name, Lease: def.options.timeout + w.config.margin})
	}
	claimed, err := db.ClaimJob(ctx, w.pool, known, id)
	if err != nil {
		return outcomeNone, err
	}
	if claimed == nil {
		return outcomeNone, nil
	}
	// ClaimJob only returns names of known.
	def, ok := w.jobs[claimed.Name]
	if !ok {
		return outcomeNone, nil
	}
	label := fmt.Sprintf("job %s %s", claimed.Name, claimed.ID)
	if int(claimed.Attempt) > def.options.maxAttempts {
		// A worker died or timed out on every attempt (or the limit was lowered since): do not run it again.
		message := "the attempts were used up (a worker stopped while running the job, or max_attempts was lowered)"
		return w.record(ctx, claimed, id, def, label, NewPermanentJobError(message))
	}

	a := w.start(ctx, def, claimed, id)
	timeout := time.NewTimer(def.options.timeout)
	defer timeout.Stop()
	var grace <-chan time.Time
	stopped := stop
	for {
		select {
		case f := <-a.done:
			return w.finished(ctx, claimed, id, def, label, f)
		case <-timeout.C:
			if f := a.abort(w.config.abortWait, &w.abandoned); f != nil {
				return w.finished(ctx, claimed, id, def, label, *f)
			}
			return w.record(ctx, claimed, id, def, label,
				NewJobError(fmt.Sprintf("timed out after %s s", secs(def.options.timeout))))
		case <-stopped:
			stopped = nil
			grace = time.After(w.config.grace)
		case <-grace:
			// The grace period is over: give the job back.
			f := a.abort(w.config.abortWait, &w.abandoned)
			if f != nil && f.kind == finishDone {
				w.log(fmt.Sprintf("%s: the job had already completed (attempt %d)", label, claimed.Attempt))
				return outcomeDone, nil
			}
			if f != nil && f.kind == finishFailed {
				// The attempt failed just before the abort: record it through the guarded path.
				return w.record(ctx, claimed, id, def, label, f.err)
			}
			released, err := db.ReleaseJob(ctx, w.pool, claimed.ID, id, claimed.Attempt)
			if err != nil {
				return outcomeNone, err
			}
			if released {
				w.log(fmt.Sprintf("%s released (the worker is stopping)", label))
				return outcomeReleased, nil
			}
			w.log(fmt.Sprintf("%s: the lease was lost or the job had already completed", label))
			return outcomeLeaseLost, nil
		case <-ctx.Done():
			// Told to exit at once: the attempt is aborted, nothing is recorded, the lease recovers the job.
			a.abort(w.config.abortWait, &w.abandoned)
			return outcomeInterrupted, nil
		}
	}
}

// finished is the outcome of an attempt that ended on its own.
func (w *worker) finished(ctx context.Context, claimed *db.ClaimedJob, id string, def *jobDef, label string, f finish) (outcome, error) {
	switch f.kind {
	case finishDone:
		w.log(fmt.Sprintf("%s done (attempt %d)", label, claimed.Attempt))
		return outcomeDone, nil
	case finishLeaseLost:
		w.log(fmt.Sprintf("%s: the lease was lost during attempt %d; its effects were rolled back", label, claimed.Attempt))
		return outcomeLeaseLost, nil
	case finishFailed:
		return w.record(ctx, claimed, id, def, label, f.err)
	}
	// Aborted by the worker's own stop: nothing to record.
	return outcomeInterrupted, nil
}

// record records a failed attempt: retry after the backoff, or dead.
func (w *worker) record(ctx context.Context, claimed *db.ClaimedJob, id string, def *jobDef, label string, failure *JobError) (outcome, error) {
	used := int(claimed.Attempt)
	dead := failure.Permanent || used >= def.options.maxAttempts
	var f db.Failure
	if dead {
		f = db.Failure{Dead: true}
	} else {
		f = db.Failure{RetryAfter: def.options.backoffAfter(used)}
	}
	recorded, err := db.FailJob(ctx, w.pool, claimed.ID, id, claimed.Attempt, failure.Message, f)
	if err != nil {
		return outcomeNone, err
	}
	if !recorded {
		w.log(fmt.Sprintf("%s: the lease was lost or the job had already completed; the failure was not recorded", label))
		return outcomeLeaseLost, nil
	}
	if dead {
		w.log(fmt.Sprintf("%s dead after attempt %d: %s", label, claimed.Attempt, failure.Message))
		return outcomeDead, nil
	}
	w.log(fmt.Sprintf("%s failed (attempt %d of %d), retry in %s s: %s",
		label, claimed.Attempt, def.options.maxAttempts, secs(f.RetryAfter), failure.Message))
	return outcomeRetried, nil
}

// start runs the attempt in a goroutine of its own: the handler and the completion in one transaction.
func (w *worker) start(ctx context.Context, def *jobDef, claimed *db.ClaimedJob, id string) *attempt {
	hctx, cancel := context.WithCancel(ctx)
	a := &attempt{cancel: cancel, done: make(chan finish, 1)}
	registry := w.snapshot()
	go func() {
		defer cancel()
		a.done <- w.attemptJob(hctx, a, registry, def, claimed, id)
	}()
	return a
}

// attemptJob is the body of one attempt.
func (w *worker) attemptJob(hctx context.Context, a *attempt, registry *core.Registry, def *jobDef, claimed *db.ClaimedJob, id string) (result finish) {
	// failed is a failure of this attempt; an attempt that was aborted has no failure of its own (the worker
	// records the timeout, or gives the job back), whatever error the abort caused.
	failed := func(err *JobError) finish {
		if hctx.Err() != nil || a.aborted() {
			return finish{kind: finishAborted}
		}
		return finish{kind: finishFailed, err: err}
	}
	conn, err := w.pool.Acquire(hctx)
	if err != nil {
		return failed(fromDB(asDBError(err)))
	}
	// The connection goes back to the pool only when its transaction ended normally. After a panic, an
	// abort or an error whose effect on the connection is unknown it is closed instead: a query may still be
	// running on it, and a pool must never hand that out again.
	healthy := false
	defer func() {
		if r := recover(); r != nil {
			result = failed(NewJobError(panicText(r)))
			result.panicked = true
			healthy = false
		}
		if healthy {
			conn.Release()
			return
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(hctx), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(closeCtx)
	}()
	// The end of the attempt (rollback, completion, commit) never depends on the handler's context.
	end := context.WithoutCancel(hctx)
	tx, err := conn.Begin(end)
	if err != nil {
		return failed(fromDB(asDBError(err)))
	}
	handlerErr := def.handler(hctx, newJobContext(tx, registry, claimed, w.log))
	if handlerErr != nil {
		rolledBack := tx.Rollback(end) == nil
		if hctx.Err() != nil || a.aborted() {
			return finish{kind: finishAborted}
		}
		// A rollback that fails leaves the connection to be closed.
		healthy = rolledBack
		return finish{kind: finishFailed, err: jobFailure(handlerErr)}
	}
	if hctx.Err() != nil || !a.beginCommit() {
		_ = tx.Rollback(end)
		return finish{kind: finishAborted}
	}
	completed, err := db.CompleteJob(end, tx, claimed.ID, id, claimed.Attempt)
	if err != nil {
		_ = tx.Rollback(end)
		return finish{kind: finishFailed, err: fromDB(asDBError(err))}
	}
	if !completed {
		healthy = tx.Rollback(end) == nil
		return finish{kind: finishLeaseLost}
	}
	if err := tx.Commit(end); err != nil {
		return finish{kind: finishFailed, err: fromDB(asDBError(err))}
	}
	healthy = true
	return finish{kind: finishDone}
}

func (a *attempt) aborted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == attemptAborted
}

func panicText(r any) string {
	switch v := r.(type) {
	case string:
		return "panicked: " + v
	case error:
		return "panicked: " + v.Error()
	}
	return fmt.Sprintf("panicked: %v", r)
}

// runSlot is one worker slot: it claims and runs jobs until stop is closed, and sleeps poll when idle.
func (w *worker) runSlot(ctx context.Context, id string, stop <-chan struct{}) {
	stopped := func() bool {
		select {
		case <-stop:
			return true
		default:
			return false
		}
	}
	for !stopped() {
		// The tick comes before the claim, so a job that is due now is claimed at once. Only one slot ticks
		// per poll interval, so ticks do not grow with the concurrency.
		if w.tickDue() {
			switch n, err := w.tick(ctx, nil); {
			case err != nil:
				w.log("error: " + describeError(err))
			case n > 0:
				w.log(fmt.Sprintf("scheduled %d job(s)", n))
			}
		}
		switch got, err := w.runOne(ctx, id, stop); {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			w.log("error: " + describeError(err))
		case got != outcomeNone:
			continue
		}
		select {
		case <-time.After(w.config.poll):
		case <-stop:
		case <-ctx.Done():
			return
		}
	}
}

// workerID is the id of this process as a worker: host, process id and a start time, so it is unique even if
// a process id is reused. A slot adds /N.
func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = os.Getenv("HOSTNAME")
	}
	if host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d:%x", host, os.Getpid(), time.Now().UnixNano())
}

// runWorker runs concurrency worker slots until the first value on signals, then lets running jobs finish for
// at most the grace period (the slots handle that, see runOne). A second value ends the wait at once; leases
// recover whatever was running. It returns false if a slot panicked (the caller exits non-zero).
func (w *worker) runWorker(ctx context.Context, concurrency int, id string, signals <-chan struct{}) bool {
	hard, hardStop := context.WithCancel(ctx)
	defer hardStop()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var panicked atomic.Bool
	for slot := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					w.log(fmt.Sprintf("a worker slot ended abnormally: %v", r))
					panicked.Store(true)
				}
			}()
			w.runSlot(hard, fmt.Sprintf("%s/%d", id, slot), stop)
		}()
	}
	w.log(fmt.Sprintf("started %s: %d job type(s), concurrency %d", id, len(w.jobs), concurrency))
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-signals:
		close(stop)
		w.log(fmt.Sprintf("stopping: running jobs get up to %s s (send the signal again to exit now)", secs(w.config.grace)))
		select {
		case <-finished:
		case <-signals:
			w.log("exiting now (second signal); running jobs are recovered by their lease")
			w.hardStopped.Store(true)
			// The slots end their attempts without recording anything; give them a moment to close their
			// connections, not the abort wait: a handler that ignores its context must not delay the exit.
			hardStop()
			select {
			case <-finished:
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	w.log("stopped")
	return !panicked.Load()
}

// JobRun is what RunJobs did.
type JobRun struct {
	// Done is the number of jobs that ended done.
	Done int
	// Retried is the number of attempts that failed and were queued again (after a delay).
	Retried int
	// Dead is the number of jobs that ended dead.
	Dead int
}

// runJobsLimit is the most jobs RunJobs runs: more means a job keeps enqueueing work for itself.
const runJobsLimit = 10_000

// RunJobs runs every job of the composed apps that is due, one at a time, until none is due, with the
// worker's own code (claim, handler in one transaction with the completion, retry or dead); it is the
// function behind forgebase/testing.RunJobs. Jobs that hooks and other jobs enqueue while it runs are run
// too. A job that fails and is retried is queued again after its backoff, so it is not run again by this
// call. Jobs of names the binary does not know are left alone. After runJobsLimit jobs it fails (a job that
// enqueues itself without end).
func RunJobs(ctx context.Context, c *Composed, pool *pgxpool.Pool) (JobRun, error) {
	w := newWorker(pool, c.registry, c, defaultWorkerConfig(), os.Stderr)
	var run JobRun
	for range runJobsLimit {
		got, err := w.runOne(ctx, "test", nil)
		if err != nil {
			return run, err
		}
		switch got {
		case outcomeNone:
			return run, nil
		case outcomeDone:
			run.Done++
		case outcomeRetried:
			run.Retried++
		case outcomeDead:
			run.Dead++
		}
	}
	return run, fmt.Errorf("RunJobs stopped after %d jobs: a job probably enqueues itself again and again", runJobsLimit)
}

// RunSchedulesAt enqueues the job of every schedule of the composed apps that is due at at (its next_run_at
// is not after at) and returns how many jobs it enqueued: the worker's own tick with at in place of the
// database's now(). It is the function behind forgebase/testing.RunSchedulesAt.
func RunSchedulesAt(ctx context.Context, c *Composed, pool *pgxpool.Pool, at time.Time) (int, error) {
	return newWorker(pool, c.registry, c, defaultWorkerConfig(), os.Stderr).tick(ctx, &at)
}
