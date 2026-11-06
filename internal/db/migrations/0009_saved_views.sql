-- Saved list views (M8.3): a filter, sort and column set that a user names and reopens.
-- `owner_id` NULL is a shared view (every user who can read the DocType sees it; only an
-- Administrator creates or changes one). A private view belongs to one user and is deleted with
-- the user. The definition is checked by the application when it is written; it is not checked
-- on read, because a DocType may lose a field a view still names.
CREATE TABLE fb_saved_view (
    id          uuid PRIMARY KEY,
    doctype     text NOT NULL,
    label       text NOT NULL,
    owner_id    uuid REFERENCES fb_user (id) ON DELETE CASCADE,
    definition  jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    modified_at timestamptz NOT NULL DEFAULT now(),
    version     bigint NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX fb_saved_view_private_label ON fb_saved_view (doctype, owner_id, label) WHERE owner_id IS NOT NULL;
CREATE UNIQUE INDEX fb_saved_view_shared_label ON fb_saved_view (doctype, label) WHERE owner_id IS NULL;
