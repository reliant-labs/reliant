-- forge:backward-compatible — a new table only the login broker reads and writes; older releases never touch it.

-- The managed identity's sign-in sessions (forge/pkg/session SchemaSQL, the
-- library's own columns, so the ORM leaves the table alone). The cookie holds
-- an opaque id; a row is keyed by a hash of it, and the identity's tokens are
-- sealed under a key derived from it, so a copy of the table signs no one in.
CREATE TABLE auth_sessions (
    id_hash         bytea       PRIMARY KEY,
    subject         text        NOT NULL,
    sealed          bytea       NOT NULL,
    user_agent      text        NOT NULL DEFAULT '',
    issuer_session  text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    proved_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    refreshed_at    timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);
CREATE INDEX auth_sessions_subject_idx ON auth_sessions (subject, created_at DESC);
CREATE INDEX auth_sessions_idle_expires_idx ON auth_sessions (idle_expires_at);
CREATE INDEX auth_sessions_expires_idx ON auth_sessions (expires_at);
COMMENT ON COLUMN auth_sessions.id_hash IS 'forge:secret';
COMMENT ON COLUMN auth_sessions.sealed IS 'forge:secret';
COMMENT ON TABLE auth_sessions IS 'forge:no-entity';
