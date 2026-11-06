-- Naming series counters (M2.4). Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.

-- One row per series prefix (the rendered text before the number, for example 'INV-2026-').
-- `current` is the last number handed out. The row is incremented in the same transaction as
-- the insert of the document that takes the number, so numbering has no gaps.
CREATE TABLE fb_series (
    prefix  text PRIMARY KEY,
    current bigint NOT NULL
);
