-- The transition log of documents of DocTypes with a workflow (M8.1, ADR 0015): one row for
-- every committed transition, written in the transition's own transaction. Rows are deleted
-- with their document (a name can be reused after a delete, and there is no audit trail yet).
CREATE TABLE fb_workflow_log (
    id bigserial PRIMARY KEY,
    doctype text NOT NULL,
    name text NOT NULL,
    action text NOT NULL,
    from_state text NOT NULL,
    to_state text NOT NULL,
    actor text NOT NULL,
    at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX fb_workflow_log_document ON fb_workflow_log (doctype, name, id);
