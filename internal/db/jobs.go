package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The job queue: storage and SQL only (ADR 0013). This file never runs a handler; the worker in
// internal/app does. The Rust module is forgebase-db jobs.rs.
//
// A job is a row of fb_job (migration 0005). The functions here are the whole life of a row:
//
//   - Enqueue (outbox.go) inserts a job in the transaction it is given. A hook's jobs reach the table
//     through the same code, in the transaction of the document operation (the outbox).
//   - ClaimJob takes the next due job for a worker: a queued job that is due, or a running job whose
//     lease expired (its worker died). It sets the lease and counts the attempt.
//   - CompleteJob, FailJob and ReleaseJob end an attempt. All three are guarded: they change the row
//     only if it is still running, locked by this worker and on this attempt. A worker that lost its
//     lease (it was too slow, and the job was claimed again) therefore changes nothing, and the caller
//     learns it from the false it gets back.
//   - ListJobs, GetJob and RetryDeadJob serve the command line.
//
// Every time is the database's now(), never a clock of the worker, so workers with different clocks
// agree. Payloads are bound as text and cast to jsonb in SQL.

// Job is a job row as stored.
type Job struct {
	// ID is the id (UUID v7).
	ID uuid.UUID
	// Name is the full name, <app>.<job>.
	Name string
	// Payload is the payload as compact JSON.
	Payload json.RawMessage
	// Status is queued, running, done or dead.
	Status string
	// Attempts is how many times a worker has claimed the job.
	Attempts int32
	// RunAt is when the job is due (the next try, for a retry).
	RunAt time.Time
	// LockedUntil is the end of the lease of a running job.
	LockedUntil *time.Time
	// LockedBy is the worker that holds or held the job.
	LockedBy *string
	// LastError is the error of the last failed attempt.
	LastError *string
	// EnqueuedBy is the actor that caused the job.
	EnqueuedBy string
	// CreatedAt is when the job was enqueued.
	CreatedAt time.Time
	// FinishedAt is when the job was done or died.
	FinishedAt *time.Time
}

// ClaimedJob is a job a worker has claimed.
type ClaimedJob struct {
	ID      uuid.UUID
	Name    string
	Payload json.RawMessage
	// Attempt is this attempt, counted from 1 (the stored attempts after the claim).
	Attempt int32
}

// KnownJob is a job name a worker knows, with the length of the lease it takes on a job of that name.
type KnownJob struct {
	// Name is the full job name.
	Name string
	// Lease is the job's timeout plus a margin.
	Lease time.Duration
}

// Failure is what FailJob does with the job: Dead gives up (the job is dead), otherwise the job is
// tried again after RetryAfter.
type Failure struct {
	Dead       bool
	RetryAfter time.Duration
}

// RetryResult is what RetryDeadJob found.
type RetryResult int

const (
	// RetryQueued: the dead job is queued again.
	RetryQueued RetryResult = iota + 1
	// RetryNotFound: no job has this id.
	RetryNotFound
	// RetryNotDead: the job is not dead, so it is not retried; RetryDeadJob also returns its status.
	RetryNotDead
)

// MaxJobErrorChars is the most characters of an error message that are stored.
const MaxJobErrorChars = 2000

const jobColumns = "id, name, payload::text, status, attempts, run_at, locked_until, locked_by, last_error, enqueued_by, created_at, finished_at"

// ClaimJob claims the next job a worker can run, or nil. One statement, so it needs no transaction.
//
// A job is eligible if its name is in known and it is either queued and due (run_at <= now()), or
// running with an expired lease (the worker died). The job with the oldest run_at goes first, and
// FOR UPDATE SKIP LOCKED lets concurrent workers pass over a row that another worker is claiming. The
// claim sets status = 'running', counts the attempt, records worker and sets the lease to now() plus
// the job's lease from known.
//
// Expired leases are looked for first, in a statement of its own: running jobs are few and have an
// index, while one statement with OR could not use the ordered index of the queue and would sort every
// due job on every claim.
func ClaimJob(ctx context.Context, q Queryer, known []KnownJob, worker string) (*ClaimedJob, error) {
	if len(known) == 0 {
		return nil, nil
	}
	names := make([]string, len(known))
	leases := make([]float64, len(known))
	for i, k := range known {
		names[i] = k.Name
		leases[i] = k.Lease.Seconds()
	}
	for _, due := range []string{
		"status = 'running' AND locked_until < now()",
		"status = 'queued' AND run_at <= now()",
	} {
		sql := "UPDATE fb_job SET status = 'running', attempts = attempts + 1, locked_by = $3, " +
			"locked_until = now() + make_interval(secs => ( " +
			"SELECT t.lease FROM unnest($1::text[], $2::float8[]) AS t(name, lease) " +
			"WHERE t.name = fb_job.name)) " +
			"WHERE id = (SELECT id FROM fb_job WHERE name = ANY($1::text[]) AND " + due + " " +
			"ORDER BY run_at, id LIMIT 1 FOR UPDATE SKIP LOCKED) " +
			"RETURNING id, name, payload::text, attempts"
		var (
			id      [16]byte
			claimed ClaimedJob
			payload string
		)
		err := q.QueryRow(ctx, sql, names, leases, worker).Scan(&id, &claimed.Name, &payload, &claimed.Attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, wrap(err)
		}
		claimed.ID = uuid.UUID(id)
		if claimed.Payload, err = parsePayload(payload); err != nil {
			return nil, err
		}
		return &claimed, nil
	}
	return nil, nil
}

// CompleteJob marks the job done, if this worker still holds it on this attempt. Call it in the
// transaction of the handler: when it returns false the lease was lost and the caller must roll the
// transaction back, so the handler's effects commit together with the completion or not at all.
func CompleteJob(ctx context.Context, q Queryer, id uuid.UUID, worker string, attempt int32) (bool, error) {
	tag, err := q.Exec(ctx,
		"UPDATE fb_job SET status = 'done', finished_at = now(), locked_until = NULL "+
			"WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3",
		[16]byte(id), worker, attempt)
	if err != nil {
		return false, wrap(err)
	}
	return tag.RowsAffected() == 1, nil
}

// FailJob records a failed attempt, if this worker still holds the job on this attempt. With a retry
// the job is queued again, due after the delay; with Dead it is dead. message is stored, cut to
// MaxJobErrorChars characters. It returns false when the lease was lost (nothing changed).
func FailJob(ctx context.Context, q Queryer, id uuid.UUID, worker string, attempt int32, message string, failure Failure) (bool, error) {
	message = truncateChars(message, MaxJobErrorChars)
	const guard = "WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3"
	var (
		sql  string
		args []any
	)
	if failure.Dead {
		sql = "UPDATE fb_job SET status = 'dead', finished_at = now(), last_error = $4, locked_until = NULL " + guard
		args = []any{[16]byte(id), worker, attempt, message}
	} else {
		sql = "UPDATE fb_job SET status = 'queued', run_at = now() + make_interval(secs => $5), " +
			"last_error = $4, locked_by = NULL, locked_until = NULL " + guard
		args = []any{[16]byte(id), worker, attempt, message, failure.RetryAfter.Seconds()}
	}
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return false, wrap(err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseJob gives a claimed job back, for example when a worker stops: the job is queued again, due
// now, and the attempt is not counted. Guarded like CompleteJob; it returns false when the lease was
// lost.
func ReleaseJob(ctx context.Context, q Queryer, id uuid.UUID, worker string, attempt int32) (bool, error) {
	tag, err := q.Exec(ctx,
		"UPDATE fb_job SET status = 'queued', attempts = attempts - 1, run_at = now(), "+
			"locked_by = NULL, locked_until = NULL "+
			"WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3",
		[16]byte(id), worker, attempt)
	if err != nil {
		return false, wrap(err)
	}
	return tag.RowsAffected() == 1, nil
}

// DoneRetentionSecs is how long a finished (done or dead) job is kept before PurgeFinishedJobs
// deletes it, in seconds (7 days).
const DoneRetentionSecs = 7.0 * 24.0 * 3600.0

// PurgeFinishedJobs deletes up to limit jobs that are done or dead and finished more than
// DoneRetentionSecs ago, and returns how many it deleted. Queued and running jobs are never touched. A
// dead job that an operator retried (RetryDeadJob) is queued with finished_at cleared, so it is not
// deleted. The partial index fb_job_finished_idx (migration 0007) serves the query.
func PurgeFinishedJobs(ctx context.Context, q Queryer, limit int64) (uint64, error) {
	tag, err := q.Exec(ctx,
		"DELETE FROM fb_job WHERE id IN ( "+
			"SELECT id FROM fb_job "+
			"WHERE status IN ('done', 'dead') "+
			"AND finished_at < now() - make_interval(secs => $1::float8) "+
			"LIMIT $2 FOR UPDATE SKIP LOCKED)", DoneRetentionSecs, limit)
	if err != nil {
		return 0, wrap(err)
	}
	return uint64(tag.RowsAffected()), nil
}

// JobFilter is which jobs ListJobs returns; an empty field does not filter.
type JobFilter struct {
	// Status is queued, running, done or dead.
	Status string
	// Name is the full name.
	Name string
}

// ListJobs returns the newest jobs first (by id, which is time ordered), at most limit.
func ListJobs(ctx context.Context, q Queryer, filter JobFilter, limit int64) ([]Job, error) {
	var status, name *string
	if filter.Status != "" {
		status = &filter.Status
	}
	if filter.Name != "" {
		name = &filter.Name
	}
	rows, err := q.Query(ctx,
		"SELECT "+jobColumns+" FROM fb_job "+
			"WHERE ($1::text IS NULL OR status = $1) AND ($2::text IS NULL OR name = $2) "+
			"ORDER BY id DESC LIMIT $3", status, name, limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return jobs, nil
}

// GetJob returns the job with this id, or nil. An id that is not a UUID is ErrJob.
func GetJob(ctx context.Context, q Queryer, id string) (*Job, error) {
	parsed, err := parseJobID(id)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, "SELECT "+jobColumns+" FROM fb_job WHERE id = $1", [16]byte(parsed))
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, wrap(rows.Err())
	}
	return scanJob(rows)
}

// RetryDeadJob queues a dead job again: attempts back to 0, due now, error and finish time cleared.
// Any other status is refused (RetryNotDead, with the status). An id that is not a UUID is ErrJob.
func RetryDeadJob(ctx context.Context, q Queryer, id string) (RetryResult, string, error) {
	parsed, err := parseJobID(id)
	if err != nil {
		return 0, "", err
	}
	tag, err := q.Exec(ctx,
		"UPDATE fb_job SET status = 'queued', attempts = 0, run_at = now(), last_error = NULL, "+
			"finished_at = NULL, locked_by = NULL, locked_until = NULL "+
			"WHERE id = $1 AND status = 'dead'", [16]byte(parsed))
	if err != nil {
		return 0, "", wrap(err)
	}
	if tag.RowsAffected() == 1 {
		return RetryQueued, "", nil
	}
	var status string
	err = q.QueryRow(ctx, "SELECT status FROM fb_job WHERE id = $1", [16]byte(parsed)).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetryNotFound, "", nil
	}
	if err != nil {
		return 0, "", wrap(err)
	}
	return RetryNotDead, status, nil
}

func parseJobID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.UUID{}, &Error{Kind: ErrJob, Message: fmt.Sprintf("`%s` is not a job id (a UUID)", id)}
	}
	return parsed, nil
}

func parsePayload(text string) (json.RawMessage, error) {
	if !json.Valid([]byte(text)) {
		var v any
		detail := json.Unmarshal([]byte(text), &v)
		if detail == nil {
			detail = errors.New("invalid JSON")
		}
		return nil, &Error{Kind: ErrJob, Message: "a stored payload is not JSON: " + detail.Error()}
	}
	return json.RawMessage(text), nil
}

func scanJob(row pgx.Rows) (*Job, error) {
	var (
		job     Job
		id      [16]byte
		payload string
	)
	if err := row.Scan(&id, &job.Name, &payload, &job.Status, &job.Attempts, &job.RunAt, &job.LockedUntil,
		&job.LockedBy, &job.LastError, &job.EnqueuedBy, &job.CreatedAt, &job.FinishedAt); err != nil {
		return nil, wrap(err)
	}
	job.ID = uuid.UUID(id)
	var err error
	if job.Payload, err = parsePayload(payload); err != nil {
		return nil, err
	}
	return &job, nil
}

// truncateChars keeps the first n characters (Unicode scalar values) of s.
func truncateChars(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
