-- The purge of finished jobs now also deletes dead jobs (owner decision 2026-10-30, ADR 0013).
-- Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); this file replaces an index of 0006.

-- Migration 0006 indexed only done jobs (`WHERE status = 'done'`), which the purge query
-- `status IN ('done', 'dead') AND finished_at < ...` cannot use. Replace it with an index on
-- both finished states. Queued and running jobs have no finish time and stay out of the index.
DROP INDEX fb_job_done_idx;
CREATE INDEX fb_job_finished_idx ON fb_job (finished_at) WHERE status IN ('done', 'dead');
