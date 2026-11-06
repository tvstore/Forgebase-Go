-- The scheduler (M6.2, ADR 0013).
-- Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.

-- One row per schedule of the binary that migrated: the full name of the job it enqueues, and
-- when it is due next. `forgebase migrate` keeps the rows equal to what the binary declares.
-- A worker claims a due row by moving `next_run_at` forward in the same transaction that
-- enqueues the job, so a tick is enqueued once however many workers run (no leader). Every time
-- is PostgreSQL `now()`, never a worker's clock.
CREATE TABLE fb_schedule (
    -- the full job name, `<app>.<job>`
    name        text PRIMARY KEY,
    -- the canonical text of the schedule (`every 15m`, `daily 02:00`, `weekly mon 02:00`), to
    -- detect a changed schedule at migrate
    spec        text NOT NULL,
    next_run_at timestamptz NOT NULL,
    last_run_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Purging finished jobs (the built-in `forgebase.purge_jobs`): only the done jobs are indexed.
CREATE INDEX fb_job_done_idx ON fb_job (finished_at) WHERE status = 'done';
