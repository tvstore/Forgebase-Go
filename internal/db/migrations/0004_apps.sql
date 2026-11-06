-- Apps and their SQL data migrations (M5.1, ADR 0012).
-- Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.

-- One row per app that was present at a `migrate`: its name and the version it declares.
CREATE TABLE fb_app (
    name        text PRIMARY KEY,
    version     text,
    migrated_at timestamptz NOT NULL DEFAULT now()
);

-- The data migrations an app declared and that were applied, with the checksum of their SQL.
CREATE TABLE fb_app_migration (
    app        text NOT NULL,
    version    integer NOT NULL,
    name       text NOT NULL,
    checksum   text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (app, version)
);
