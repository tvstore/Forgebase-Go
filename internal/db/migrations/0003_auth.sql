-- Users, roles, sessions, API tokens and login failure accounting (M3.1, ADR 0010).
-- Runs inside the migration transaction: no BEGIN/COMMIT here.
-- Never edit an applied migration (its checksum is verified); add a new numbered file instead.
-- No plaintext secret is ever stored: passwords are Argon2id PHC strings, session tokens, CSRF
-- tokens and API secrets are stored as SHA-256 digests (computed by PostgreSQL).

CREATE TABLE fb_user (
    id            uuid PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- A role is a plain name; what a role may do comes with permissions (M3.2).
CREATE TABLE fb_user_role (
    user_id uuid NOT NULL REFERENCES fb_user (id) ON DELETE CASCADE,
    role    text NOT NULL,
    PRIMARY KEY (user_id, role)
);

-- Server-side sessions. `token_hash` is the SHA-256 of the session token, `csrf_hash` that of
-- the CSRF token. `expires_at` is the absolute expiry; `last_seen_at` drives the idle expiry.
CREATE TABLE fb_session (
    token_hash   bytea PRIMARY KEY,
    csrf_hash    bytea NOT NULL,
    user_id      uuid NOT NULL REFERENCES fb_user (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX fb_session_user_id_idx ON fb_session (user_id);

-- API tokens: `key` is the public identifier, `secret_hash` the SHA-256 of the secret.
CREATE TABLE fb_api_token (
    key         text PRIMARY KEY,
    secret_hash bytea NOT NULL,
    user_id     uuid NOT NULL REFERENCES fb_user (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);
CREATE INDEX fb_api_token_user_id_idx ON fb_api_token (user_id);

-- Failed login accounting, keyed by the submitted username (also for unknown users, so the
-- lockout does not reveal which usernames exist). A failure only counts with the previous ones if
-- `last_failure_at` is at most 15 minutes ago; otherwise the count restarts at 1.
CREATE TABLE fb_login_failure (
    username        text PRIMARY KEY,
    failures        integer NOT NULL,
    locked_until    timestamptz,
    last_failure_at timestamptz
);
