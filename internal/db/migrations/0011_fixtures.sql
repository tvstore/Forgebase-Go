-- Fixtures (M9.4, ADR 0022): one row per fixture record an app has applied. `checksum` is the
-- SHA-256 (hex) of the record's canonical JSON, so the next `migrate` can tell whether the app
-- shipped a new version of the record. Rows are never deleted: a record that leaves the fixture
-- file keeps its row, and its document is not touched.
CREATE TABLE fb_fixture (
    doctype    text NOT NULL,
    name       text NOT NULL,
    app        text NOT NULL,
    checksum   text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (doctype, name)
);
