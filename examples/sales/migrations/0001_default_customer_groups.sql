-- Creates the customer groups `Retail` and `Wholesale` if they do not exist yet.
--
-- A data migration: it runs once per database, after the DocType schema sync (so the table
-- `doc_customer_group` exists), in its own transaction. The SQL must not contain BEGIN or COMMIT.
--
-- It writes the standard columns by hand, because it bypasses the framework: `name` is the
-- value of the naming field `title` (field naming), `owner` and `modified_by` say who made the
-- row, `version` has its default 1. The statement is idempotent (`ON CONFLICT DO NOTHING`), so a
-- group that someone already created, with the same name, is left alone. `gen_random_uuid()`
-- gives a v4 id while the framework writes v7; nothing depends on id ordering (lists sort by
-- `name`, child rows by `idx`).
--
-- Migrations are append-only: once applied, this file is never edited or removed (its checksum is
-- verified at every `migrate`). To change the groups later, add a migration 0002.
INSERT INTO doc_customer_group (id, name, owner, created_at, modified_at, modified_by, title)
SELECT gen_random_uuid(), g.title, 'system', now(), now(), 'system', g.title
FROM (VALUES ('Retail'), ('Wholesale')) AS g (title)
ON CONFLICT DO NOTHING;
