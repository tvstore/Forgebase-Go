-- Site custom fields and the metadata version (M10.1, ADR 0023).
--
-- `fb_custom_field`: one row per field a site added to a DocType, enabled or disabled. The
-- `definition` is the field as JSON, with the same keys as a TOML `[[fields]]` entry. A row is
-- never deleted: disabling a field hides it and keeps its column and data.
--
-- `fb_meta_version`: one row that every change to `fb_custom_field` increments in the same
-- transaction. A running server compares it with the version it last built its registry from,
-- and rebuilds the registry when it differs.
CREATE TABLE fb_custom_field (
    doctype     text NOT NULL,
    name        text NOT NULL,
    definition  jsonb NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    modified_at timestamptz NOT NULL DEFAULT now(),
    modified_by text NOT NULL,
    PRIMARY KEY (doctype, name)
);

CREATE TABLE fb_meta_version (
    id      int PRIMARY KEY CHECK (id = 1),
    version bigint NOT NULL
);

INSERT INTO fb_meta_version (id, version) VALUES (1, 0);
