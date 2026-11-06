-- The job queue and the transactional outbox (M6.1, ADR 0013).
-- Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.

-- One row per job. A hook of a document operation inserts its jobs in the operation's own
-- transaction (the outbox), so a job exists if and only if the change is committed. A worker
-- claims a row, runs the handler in one transaction together with the completion, and records a
-- failure in a separate one. Every time is PostgreSQL `now()`, never a worker's clock.
CREATE TABLE fb_job (
    id          uuid PRIMARY KEY,
    -- `<app>.<job>`
    name        text NOT NULL,
    payload     jsonb NOT NULL,
    status      text NOT NULL CHECK (status IN ('queued', 'running', 'done', 'dead')),
    attempts    integer NOT NULL DEFAULT 0,
    run_at      timestamptz NOT NULL DEFAULT now(),
    -- the lease of a running job; a worker that dies leaves it to expire
    locked_until timestamptz,
    locked_by   text,
    last_error  text,
    -- the actor that caused the job: the caller of the document operation, a job
    -- (`job:<app>.<job>`), or `cli`
    enqueued_by text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

-- Claiming the next due job: the oldest `run_at` first. Partial, so it holds only the waiting
-- jobs and stays small while finished jobs pile up.
CREATE INDEX fb_job_claim_idx ON fb_job (run_at, id) WHERE status = 'queued';

-- Finding jobs whose lease expired (a worker crashed): only the few running jobs are indexed.
CREATE INDEX fb_job_lease_idx ON fb_job (locked_until) WHERE status = 'running';

-- There is no index for `job list`: it is an operator command with a LIMIT that reads the
-- newest rows by primary key (UUID v7 is time ordered), and finished jobs are removed after a
-- week (M6.2), so the table stays small. Add one when a measurement shows a need.
