-- User permissions (M10.3, ADR 0025).
--
-- `fb_user_permission`: one row per document a user is restricted to. A user with at least one
-- row for a DocType sees, of that DocType, only the documents named by their rows, and only the
-- documents of other DocTypes whose Link fields to it are empty or point at those documents. A
-- user without rows for a DocType is not restricted on it. `doctype` and `value` are not foreign
-- keys: a DocType is metadata (not a table row) and the data is checked when a row is added. A
-- row for a DocType or a document that no longer exists is ignored. The rows of a user go with
-- the user.
CREATE TABLE fb_user_permission (
    user_id    uuid NOT NULL REFERENCES fb_user (id) ON DELETE CASCADE,
    doctype    text NOT NULL,
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, doctype, value)
);
