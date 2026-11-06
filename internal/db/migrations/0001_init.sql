-- Forgebase framework tables. Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.

-- Also created by the runner before migrations run, hence IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS fb_migration (
    version    integer PRIMARY KEY,
    name       text NOT NULL,
    checksum   text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- DocTypes whose tables Forgebase manages.
CREATE TABLE fb_doctype (
    name       text PRIMARY KEY,
    table_name text NOT NULL UNIQUE,
    synced_at  timestamptz NOT NULL DEFAULT now()
);
