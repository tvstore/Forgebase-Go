-- Outgoing webhooks (M9.2, ADR 0020): one row per webhook of the site. A document operation
-- that matches a row enqueues a `forgebase.deliver_webhook` job in its own transaction (the
-- outbox, ADR 0013).
-- `secret` is stored in plain text because the signature (HMAC-SHA256) needs it: anyone with
-- read access to the database can read it. It is never printed by the CLI after `add`.
CREATE TABLE fb_webhook (
    name          text PRIMARY KEY,
    doctype       text NOT NULL,
    events        text[] NOT NULL,
    url           text NOT NULL,
    secret        text NOT NULL,
    enabled       boolean NOT NULL DEFAULT true,
    allow_private boolean NOT NULL DEFAULT false,
    insecure_http boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    modified_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT fb_webhook_events CHECK (
        cardinality(events) > 0
        AND events <@ ARRAY['insert', 'update', 'delete', 'submit', 'cancel', 'transition']::text[]
    )
);

CREATE INDEX fb_webhook_doctype ON fb_webhook (doctype) WHERE enabled;
