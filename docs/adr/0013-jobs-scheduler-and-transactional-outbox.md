# 0013 Jobs, scheduler and transactional outbox

- Status: Accepted (the ten owner decisions of the M6 plan, 2026-10-30); implementation details of M6.1 are separated below and are not owner decisions. Extends [ADR 0009](0009-lifecycle-pipeline-and-hooks.md) and updates the public surface of [ADR 0012](0012-app-model-and-public-api-boundary.md).
- Date: 2026-10-30

## Context

Business applications need work that runs outside a request: sending a notification after a document is saved, a nightly clean-up, a retry of something that failed. [ADR 0002](0002-modular-monolith-and-workspace-layout.md) already decided that jobs are a module in `forgebase-db` plus a `worker` command of the same binary, and [ADR 0009](0009-lifecycle-pipeline-and-hooks.md) said that effects after commit belong to the transactional outbox, not to a callback that can fail after the data is committed. The [architecture](../blueprint/architecture.md#8-background-jobs-scheduler-realtime) listed a table queue on `FOR UPDATE SKIP LOCKED` as Proposed. The owner approved the M6 plan on 2026-10-30. M6 is split into M6.1 (the queue, the worker, the outbox; this ADR's implementation), M6.2 (the scheduler and the built-in clean-up jobs) and M6.3 (example apps and the guide).

## Decision

The decisions below are the owner's (M6 plan, 2026-10-30).

1. **Hooks may enqueue jobs.** The jobs are written in the same transaction as the document change: the outbox is the job table. This extends [ADR 0009](0009-lifecycle-pipeline-and-hooks.md): hooks still have no database access.
2. **A job handler runs in one transaction together with its completion.** What the handler writes and the mark "done" commit together or not at all.
3. **Jobs get a documents API** as the system context (new public surface, see [ADR 0012](0012-app-model-and-public-api-boundary.md#public-surface-update-m61)).
4. **The scheduler has no leader.** Each schedule row is claimed atomically (M6.2).
5. **Schedules are every N minutes, daily or weekly, UTC only, no cron** (M6.2).
6. **Delivery defaults:** at-least-once; 5 attempts; a backoff that starts at 30 seconds, doubles and is capped at 1 hour; a `dead` state; a 5 minute timeout.
7. **A worker claims only the job names it knows.**
8. **Built-in clean-up jobs:** finished jobs after 7 days, login failures, expired sessions (M6.2).
9. **Operations are through the CLI only** (`worker`, `job list|show|retry|enqueue`). There is no HTTP or desk interface for jobs.
10. **The work is split into M6.1, M6.2 and M6.3.** M6.3 touches no file under `crates/`.

## Implementation details (M6.1)

These choices were made while implementing M6.1 and are reviewed with that change; they are not owner decisions.

**Where things live.** `forgebase-db::jobs` is the queue storage and its SQL (enqueue, claim, complete, fail, release, list, show, retry); it never runs a handler. `forgebase-core` has the collector for hook enqueues (`EnqueuedJobs`, `HookContext::enqueue`) and the set of known job names in the `Registry` (names only). The `forgebase` crate has the handler API (`JobHandler`, `JobContext`, `Documents`, `JobError`, `JobOptions`), `App::job`, the worker loop, the commands and the `testing` helpers.

**The table (migration 0005).** `fb_job`: `id` (UUID v7), `name` (`<app>.<job>`), `payload` (jsonb), `status` (`queued`, `running`, `done`, `dead`), `attempts`, `run_at`, `locked_until`, `locked_by`, `last_error`, `enqueued_by`, `created_at`, `finished_at`. Two partial indexes: `(run_at, id) WHERE status = 'queued'` for claiming the oldest due job and `(locked_until) WHERE status = 'running'` for finding expired leases; both stay small while finished jobs pile up. No index for `job list` (an operator command with a limit that reads by primary key; finished jobs are removed after a week, M6.2, which adds an index on `finished_at` of done jobs for that purge). Every time is PostgreSQL `now()`, never a worker's clock. Payloads are bound as text and cast to `jsonb` in SQL, so the database driver needs no extra feature.

**The outbox.** One `EnqueuedJobs` collector (a `Mutex` inside, so futures stay `Send`) per document operation is given to every hook call through `HookContext::with_jobs`. After the hooks and before the commit, `forgebase-db` inserts the collected jobs in the operation's transaction, for all six operations. A refused or rolled-back operation leaves no job; an unknown job name (not in the registry) or an oversize payload fails the whole operation with the new `DbError::Job`, which the HTTP API reports as `500 internal` with the detail in the server log: it is a mistake of the app, not of the caller. `enqueued_by` is the caller of the operation. `HookContext` stays `Copy`; its equality compares the collector by identity. A context made by `HookContext::new` has no collector, and `enqueue` on it fails with a message that says so.

**Payload.** At most 64 KiB of serialized JSON and no NUL character (`jsonb` cannot store one), checked on every way of enqueueing. Payloads are stored in clear text: never put a secret in one. `forgebase::serde_json` is re-exported so apps and the framework use one version.

**The documents API.** `JobContext::documents()` runs as `Caller::System` with the actor `job:<app>.<job>`, with the same document functions as the CLI and HTTP API on the job's transaction. Each write (and `JobContext::enqueue`) is a savepoint, so a refused write leaves the job's transaction usable; a read (`get`, `list`) runs directly on the job's transaction, so a failed read aborts it, the attempt fails and is retried; the hooks of those calls run as usual and the jobs they enqueue are written in the same transaction. A database or connection error becomes a retryable `JobError`; a validation, permission, hook, not-found or unique error, a version conflict and every other refusal is permanent, because it would fail again. `Documents` has one lifetime parameter: the transaction type is hidden behind a private trait object, so the public type does not carry the transaction's lifetime.

**Claiming.** One statement (`UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)`), committed at once. A job is eligible if its name is known to the worker and it is queued and due, or running with an expired lease. Expired leases are looked for first, in a statement of their own: running jobs are few, and one statement with `OR` could not use the ordered index of the queue. The claim counts the attempt, records the worker and sets the lease to the job's timeout plus a margin of 30 seconds.

**Running.** The handler runs in a spawned task on its own connection and transaction under `tokio::time::timeout`. On `Ok`, the same transaction runs the guarded completion (`WHERE id AND status = 'running' AND locked_by = me AND attempts = this attempt`); if it changes no row the lease was lost, the transaction is rolled back and the handler's effects are gone with it. An error, a timeout or a panic (`JoinError`) rolls back and is recorded in a new short transaction with the same guard: a retry after `min(base * 2^(attempt - 1), cap)`, or `dead` for a permanent error or when the attempts are used up. A timed-out or aborted attempt **closes its connection** instead of returning it to the pool, because a query may still be running. A job that was claimed after a crashed worker had used its last attempt is marked `dead` without running again.

**Options live in the binary**, not on the job row (`JobOptions`: attempts, timeout, backoff). The worker uses its own definition when it runs and retries a job. Invalid options, an invalid or duplicate job name are startup errors. The option rules: 1 to 1000 attempts, a timeout of more than zero and at most 24 hours, a backoff base of more than zero and a cap between the base and 30 days (the lease, timeout plus margin, is computed with saturating arithmetic). A worker slot that panics is logged and makes the `worker` command exit non-zero.

**Shutdown.** The first SIGTERM or Ctrl-C stops claiming and lets running jobs finish for 30 seconds; a job still running then is aborted (rolled back) and **released** (`queued`, the attempt not counted, due now). A second signal exits at once; the lease recovers what was running. Exit code 0 on a clean stop. The pool has `concurrency + 2` connections: one per running job, and a spare for claims and for recording failures.

**Delivery guarantees.** At-least-once. Database effects of a handler are exactly-once in effect (they commit with the completion). Effects outside the database are not covered: a job that crashes after sending an email may send it again.

## Implementation details (M6.2)

These choices were made while implementing M6.2 and are reviewed with that change; they are not owner decisions (the scheduler without a leader, the schedule kinds and the built-in jobs are decisions 4, 5 and 8).

**Where things live.** `forgebase-db::schedule` has the schedule values (`Spec`), the pure function `next_after`, the sync of the table and the tick. The SQL of the clean-up jobs is next to the code whose rules it must follow: `jobs::purge_finished`, `auth::purge_login_failures`, `auth::purge_sessions`. The `forgebase` crate has `Schedule`, `App::schedule`, the built-in handlers and the call of the tick in the worker loop.

**The table (migration 0006).** `fb_schedule`: `name` (the full job name, primary key), `spec` (canonical text such as `every 15m`, `daily 02:00`, `weekly mon 02:00`, used to detect a changed schedule), `next_run_at`, `last_run_at`, `updated_at`. A partial index `fb_job (finished_at) WHERE status = 'done'` served the purge of finished jobs (replaced by `fb_job_finished_idx` in migration 0007, below).

**Schedules.** `Schedule::every_minutes(n)` (1 to 1440), `daily_at(hour, minute)`, `weekly_on(weekday, hour, minute)`; all UTC. They store raw values; invalid ones (`n` of 0 or above 1440, an hour above 23, a minute above 59) are startup errors, like `JobOptions`. `App::schedule(job, schedule)` names a job of the **same app**; a job that app did not register, a second schedule for a job and an invalid schedule are startup errors. **Epoch alignment:** every N minutes ticks at the times where the minutes since 1970-01-01 00:00 UTC are divisible by N (every 15 is :00, :15, :30, :45; every 1440 is midnight UTC; 7 does not repeat each hour). This makes the ticks independent of when a worker or a schedule started. `next_after(schedule, t)` returns the first time strictly after `t`; it is a pure function with unit tests for the boundaries of minutes, days, months, years and the leap day.

**The tick.** A worker runs at most one tick per poll interval (1 second), whatever its concurrency: the slots share a "last tick" instant, and the first slot to start an iteration in a new interval ticks, before it claims a job. The tick:

1. One statement reads the due schedules this binary knows: `SELECT name, next_run_at, now() FROM fb_schedule WHERE next_run_at <= now() AND name = ANY(known)`.
2. For each, in its own short transaction: `UPDATE fb_schedule SET next_run_at = <next>, last_run_at = <at> WHERE name = $1 AND next_run_at = <the value read> RETURNING ...`. The guard on the value read makes exactly one of any number of concurrent workers change the row; the others find the guard false (READ COMMITTED re-checks it after the winner commits) and get no row. The winner enqueues the job with `forgebase_db::jobs::enqueue` **in the same transaction** and commits, so a schedule moves forward if and only if its job exists. `enqueued_by` is `scheduler`.
3. `<next>` is the first schedule time after `<at>`, so missed ticks are not caught up: after an outage one job is enqueued, then the next future time.

**Database time.** `<at>` is the `now()` of the reading statement (option B of the spec: it is read once, in the same statement as the due rows, and used as a parameter). The guard is on `next_run_at`, not on `<at>`, so the value used for the next time cannot let two workers both win. A worker's clock is never used; `testing::run_schedules_at` replaces `now()` by a time given by the test and runs the same code.

**`scheduled_for`.** The payload of a scheduled job is `{"scheduled_for": "<RFC 3339 UTC>"}`: the `next_run_at` that was due (the tick), not the time the worker ran. A handler can use it as an idempotency key; delivery is still at-least-once. `scheduled_for` has second precision (fractional seconds are not kept); production schedule times are whole minutes.

**Migrate sync.** `forgebase migrate` writes the schedules the binary declares (the apps' and the built-in ones) after the app migrations, in the same run under the same lock (`forgebase_db::migrate_all`; `migrate_apps` leaves the table alone): a new schedule is inserted due at its first time after the database's `now()`; a changed `spec` is updated and its `next_run_at` recomputed; an unchanged row keeps its `next_run_at`; **a row the binary does not declare is deleted**. The binary that migrates is the source of truth, so a removed schedule stops, and the schedules of an app that was removed from the binary are deleted by the next `migrate` of a binary without it. The consequence is that all binaries that share a database must declare the same schedules (they are normally one build). `--dry-run` lists the planned changes (`add`, `change`, `remove`) when no framework migration is pending. A worker never writes schedule definitions: a schedule added to a binary starts after `migrate`.

**The reserved app name `forgebase`.** An app with this name is a startup error. The framework registers its own jobs under it in every binary, with the default job options, so `forgebase worker` works on a binary without app jobs (the M6.1 "no jobs" exit is gone).

**Built-in jobs.**

| Job | Deletes | Schedule |
|---|---|---|
| `forgebase.purge_jobs` | `done` and `dead` jobs finished more than 7 days ago | daily at 03:00 UTC |
| `forgebase.purge_login_failures` | `fb_login_failure` rows that no longer count or lock | every 60 minutes |
| `forgebase.purge_sessions` | sessions `validate_session` would refuse as expired (`now() >= expires_at` or `now() >= last_seen_at + 24 h`) | every 60 minutes |

**Finished jobs.** "Finished jobs" (decision 8) means `done` and `dead` jobs: both are deleted 7 days after `finished_at` (owner decision 2026-10-30, which settles the interpretation of decision 8; until then only `done` jobs were purged and dead jobs were kept for the operator). `jobs::purge_finished` deletes in bounded batches with `FOR UPDATE SKIP LOCKED`. Queued and running jobs are never touched. A dead job that an operator retried (`forgebase job retry`) is `queued` again with `finished_at` cleared, so it is not deleted. An operator who wants to look at a dead job has 7 days. Migration 0007 replaces the partial index of migration 0006 (`fb_job_done_idx`, done jobs only) with `fb_job_finished_idx` on `finished_at` where the status is `done` or `dead`.

**What a login failure row is worth.** A row matters while the username is locked (`locked_until > now()`) or its last failure is within the failure window (`last_failure_at >= now() - window`; the next failure would be counted with it). Every other row (including one with no failure time) starts again at one failure exactly as if it had no row, so deleting it changes nothing a login can see. The condition is next to the login code and uses the same constants. Each purge deletes in batches of 1000 with `FOR UPDATE SKIP LOCKED` (so it never waits for a login that holds a row), at most 100 batches per run; a larger backlog continues at the next run. A purge batch holds its row locks until the job's transaction commits, so a login or session validation that touches a row being purged may wait for that commit; the wait is bounded by the job's runtime and only stale rows are affected.

## Implementation details (M6.3)

The example apps use jobs, the outbox and a schedule with no framework change: `sales` enqueues a log job from `before_submit` and `before_cancel` and runs a daily summary on `Schedule::daily_at(2, 0)`, and `sales_extension` enqueues a job of its own on a `sales` DocType. The M6.3 diff touches no file under `crates/`. See [the first-app guide](../guide/first-app.md#7-background-jobs-and-schedules).

## Alternatives considered

- **A leader-elected scheduler** (the architecture note of earlier drafts). A leader needs a dedicated connection, failover and a heartbeat; claiming each schedule row atomically needs none of them and fails safe (M6.2).
- **A separate queue (Redis, a message broker).** Rejected: a second system to run, no transactional outbox with the document change, against the modular-monolith principle.
- **Completion in a separate transaction from the handler.** Simpler, but an effect and its "done" could diverge; the guarded completion in the handler's transaction removes the window for database effects.
- **LISTEN/NOTIFY to wake workers.** Not needed: polling once per second is simple and enough; it can be added later without changing the table.
- **Named queues, priorities, a job UI, jobs running as a user, exactly-once external effects.** Not needed by an approved requirement.
- **A cron expression parser** (M6.2): rejected by decision 5 to avoid a dependency and a large surface.

## Consequences

- A job may run more than once; handlers must be safe to repeat for effects outside the database.
- Jobs and their hooks are part of the public API of apps: [ADR 0012](0012-app-model-and-public-api-boundary.md#public-surface-update-m61) lists the new surface.
- The `fb_job` payload and `last_error` are readable by anyone with database access and by the CLI; see the [security model](../blueprint/security-model.md).
- The scheduler and the clean-up jobs (decisions 4, 5 and 8) are implemented in M6.2; their details are above. A schedule needs a worker to be running at the time, and `forgebase migrate` after a change of schedules.
- *Amendment (M9.2):* the framework has a fourth built-in job, `forgebase.deliver_webhook`, which has no schedule: the document operations of `forgebase-db` enqueue it through this outbox for the webhooks of the site ([ADR 0020](0020-outgoing-webhooks.md)). Its payload holds a document snapshot, so the remark above about the payload in clear text applies to it as well.
