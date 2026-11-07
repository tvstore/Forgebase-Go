package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Background jobs for apps: JobHandler, JobContext, JobError and JobOptions (ADR 0013; the Rust module is
// jobs.rs). The root package re-exports these as type aliases, so this file is where the godoc of the
// public API lives.

// JobHandler is what a background job does. Register it with [App.Job] or [App.JobWith]. It returns nil to
// complete the job; an error is retried or, for a [JobError] made with [NewPermanentJobError], ends it.
//
// Rust has a trait here; in Go the simplest thing that holds the same rules is a function, and a handler
// that needs state is a closure over it:
//
//	func Welcome(crm *Mailer) forgebase.JobHandler {
//		return func(ctx context.Context, job *forgebase.JobContext) error {
//			var p struct{ Customer string `json:"customer"` }
//			if err := job.DecodePayload(&p); err != nil {
//				return forgebase.NewPermanentJobError(err.Error())
//			}
//			found, err := job.Documents().Get(ctx, "customer", p.Customer)
//			if err != nil {
//				return err // retried if the database is down, permanent for a refusal
//			}
//			if found == nil {
//				return forgebase.NewPermanentJobError("the customer is gone")
//			}
//			return nil
//		}
//	}
//
// The handler runs inside a database transaction; if it returns an error, everything it did through the
// [JobContext] is rolled back. The ctx it gets is cancelled when the attempt is aborted (it ran longer than
// its timeout, or the worker stops and the grace period is over): pass it on to every call and return
// when it is done. A handler that ignores it cannot be stopped (Go cannot abort a goroutine): its attempt
// is still failed or given back, its transaction is never committed, but the goroutine and its connection
// stay in use until it returns.
//
// # Delivery
//
//   - At least once. A job runs in one database transaction together with its completion: what the handler
//     writes through [JobContext.Documents] or [JobContext.Enqueue] commits with the completion, or is
//     rolled back and the job is tried again. Effects outside the database (an email, an HTTP call) are not
//     covered: a job that crashes after sending may send again, so make such effects safe to repeat.
//   - Retries. A handler error is retried, up to [JobOptions] max attempts (5 by default), after a delay that
//     starts at 30 seconds and doubles up to 1 hour; then the job is dead until an operator runs `forgebase
//     job retry`. A permanent [JobError] skips the retries. A handler that runs longer than its timeout (5
//     minutes by default) fails the same way, and a handler that panics is a retryable failure (the worker
//     survives).
//   - Options live in the binary. They are not stored on the job row: the worker uses the definition in its
//     own binary when it runs and retries a job, so a change takes effect for jobs that are already queued.
//   - A worker claims only the jobs it knows, by name. A job of an app that this binary does not have stays
//     queued for a binary that has it.
type JobHandler func(ctx context.Context, job *JobContext) error

// JobError is why a job failed. [NewJobError] is retried (until the attempts are used up);
// [NewPermanentJobError] goes straight to dead. A handler may return any error: one that is not a JobError
// is classified by what it wraps. A [DocError] or a database error is retryable when it is a database or
// connection error (the same call may well work later) and permanent for every refusal (validation,
// permission, a hook that rejected, not found, a version conflict, a unique violation, ...), because it
// would fail again; any other error is retryable.
type JobError struct {
	// Message is stored as the job's last_error (cut to 2000 characters).
	Message string
	// Permanent is true when the job is not retried.
	Permanent bool
}

// NewJobError is a failure that is worth retrying.
func NewJobError(message string) *JobError { return &JobError{Message: message} }

// NewPermanentJobError is a failure that retrying cannot fix: the job is dead after this attempt.
func NewPermanentJobError(message string) *JobError {
	return &JobError{Message: message, Permanent: true}
}

func (e *JobError) Error() string { return e.Message }

// IsPermanent reports whether the job is not retried.
func (e *JobError) IsPermanent() bool { return e.Permanent }

// jobFailure is how a handler's error is recorded: its text and whether it is permanent.
func jobFailure(err error) *JobError {
	var je *JobError
	if errors.As(err, &je) {
		return je
	}
	var de *DocError
	if errors.As(err, &de) {
		return fromDB(de.Err)
	}
	var dbe *db.Error
	if errors.As(err, &dbe) {
		return fromDB(dbe)
	}
	return NewJobError(err.Error())
}

// fromDB is the JobError of a database error: a database or connection error is retryable, every refusal
// is permanent. The text is the one the command line prints.
func fromDB(err *db.Error) *JobError {
	message := describeDB(err)
	switch err.Kind {
	case db.ErrPostgres, db.ErrLockTimeout, db.ErrPool, db.ErrCreatePool, db.ErrChangedWhileReading:
		return NewJobError(message)
	}
	return NewPermanentJobError(message)
}

// asDBError is err as a *db.Error; any other error (a cancelled context, say) is a database error of the
// driver kind.
func asDBError(err error) *db.Error {
	var dbe *db.Error
	if errors.As(err, &dbe) {
		return dbe
	}
	return &db.Error{Kind: db.ErrPostgres, Message: err.Error(), Err: err}
}

// JobOptions is how a job is run and retried. The defaults are 5 attempts, a 30 second delay that doubles up
// to 1 hour, and a 5 minute timeout. Start from [NewJobOptions] and chain the methods below, then give it to
// [App.JobWith]; invalid values (and the zero JobOptions) are a startup error.
//
// Limits: 1 to 1000 attempts, a timeout of more than zero and at most 24 hours, a backoff base of more than
// zero, a cap of at least the base and at most 30 days.
//
// The options are part of the binary, not of the job row (see Delivery on [JobHandler]).
type JobOptions struct {
	maxAttempts int
	timeout     time.Duration
	backoffBase time.Duration
	backoffCap  time.Duration
}

const (
	// maxJobAttempts is the most attempts a job may be given.
	maxJobAttempts = 1000
	// maxJobTimeout is the longest timeout a job may have.
	maxJobTimeout = 24 * time.Hour
	// maxBackoffCap is the longest backoff cap a job may have.
	maxBackoffCap = 30 * 24 * time.Hour
)

// NewJobOptions is the defaults.
func NewJobOptions() JobOptions {
	return JobOptions{maxAttempts: 5, timeout: 5 * time.Minute, backoffBase: 30 * time.Second, backoffCap: time.Hour}
}

// MaxAttempts sets the most attempts a job gets, the first one included. From 1 to 1000.
func (o JobOptions) MaxAttempts(attempts int) JobOptions {
	o.maxAttempts = attempts
	return o
}

// Timeout sets how long a handler may run before the attempt is failed. More than zero, at most 24 hours.
func (o JobOptions) Timeout(timeout time.Duration) JobOptions {
	o.timeout = timeout
	return o
}

// Backoff sets the delay before the second attempt to base; each further one doubles it, up to cap. base is
// more than zero, cap is at least base and at most 30 days.
func (o JobOptions) Backoff(base, cap time.Duration) JobOptions {
	o.backoffBase, o.backoffCap = base, cap
	return o
}

// problems is what is wrong with the options, one message per problem.
func (o JobOptions) problems() []string {
	var problems []string
	if o.maxAttempts < 1 {
		problems = append(problems, "max_attempts must be at least 1")
	}
	if o.maxAttempts > maxJobAttempts {
		problems = append(problems, fmt.Sprintf("max_attempts must be at most %d", maxJobAttempts))
	}
	if o.timeout <= 0 {
		problems = append(problems, "timeout must be more than zero")
	}
	if o.timeout > maxJobTimeout {
		problems = append(problems, "timeout must be at most 24 hours")
	}
	if o.backoffBase <= 0 {
		problems = append(problems, "the backoff base must be more than zero")
	}
	if o.backoffCap < o.backoffBase {
		problems = append(problems, "the backoff cap must be at least the backoff base")
	}
	if o.backoffCap > maxBackoffCap {
		problems = append(problems, "the backoff cap must be at most 30 days")
	}
	return problems
}

// backoffAfter is the delay before the attempt after attempt (counted from 1): min(base * 2^(attempt - 1),
// cap).
func (o JobOptions) backoffAfter(attempt int) time.Duration {
	doublings := min(max(attempt-1, 0), 31)
	// base << doublings overflows when base > cap >> doublings (cap is at least base for valid options).
	if o.backoffBase > o.backoffCap>>doublings {
		return o.backoffCap
	}
	return o.backoffBase << doublings
}

// jobDef is a job of an app: its full name, handler and options.
type jobDef struct {
	name    string
	handler JobHandler
	options JobOptions
}

// JobContext is what a running job knows, and what it may do. The worker builds it; a handler gets it as a
// pointer and must not keep it after it returns.
//
// It owns the transaction the job runs in. [JobContext.Documents] and [JobContext.Enqueue] work inside it, so
// their effects commit together with the job's completion, or not at all.
type JobContext struct {
	tx       pgx.Tx
	registry *core.Registry
	caller   core.Caller
	name     string
	id       string
	attempt  int
	payload  json.RawMessage
	log      func(string)
}

func newJobContext(tx pgx.Tx, registry *core.Registry, claimed *db.ClaimedJob, log func(string)) *JobContext {
	return &JobContext{
		tx:       tx,
		registry: registry,
		caller:   core.SystemCaller("job:" + claimed.Name),
		name:     claimed.Name,
		id:       claimed.ID.String(),
		attempt:  int(claimed.Attempt),
		payload:  claimed.Payload,
		log:      log,
	}
}

// Name is the full name of the job, <app>.<job>.
func (c *JobContext) Name() string { return c.name }

// ID is the id of the job (a UUID, as text). It is the same for every attempt.
func (c *JobContext) ID() string { return c.id }

// Attempt is the number of this attempt, counted from 1.
func (c *JobContext) Attempt() int { return c.attempt }

// Payload is the payload the job was enqueued with, as JSON.
func (c *JobContext) Payload() json.RawMessage { return c.payload }

// DecodePayload decodes the payload into v, as json.Unmarshal does.
func (c *JobContext) DecodePayload(v any) error { return json.Unmarshal(c.payload, v) }

// Enqueue enqueues another job (<app>.<job>) in this job's transaction: it exists if and only if this job
// completes. payload is anything encoding/json can marshal. An unknown name or a payload over 64 KiB of JSON
// is a permanent error (it would fail again). Never put secrets in a payload: it is stored in clear text.
func (c *JobContext) Enqueue(ctx context.Context, name string, payload any) error {
	// A savepoint: a refused enqueue leaves the job's transaction usable.
	sp, err := c.tx.Begin(ctx)
	if err != nil {
		return fromDB(asDBError(err))
	}
	if _, err := db.Enqueue(ctx, sp, c.registry, name, payload, c.caller.Name()); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return fromDB(asDBError(err))
	}
	if err := sp.Commit(ctx); err != nil {
		return fromDB(asDBError(err))
	}
	return nil
}

// Documents reads and writes documents as the trusted system context (no permission rules), with the actor
// job:<app>.<job>. See [Documents].
func (c *JobContext) Documents() *Documents {
	return &Documents{tx: c.tx, registry: c.registry, caller: c.caller}
}
