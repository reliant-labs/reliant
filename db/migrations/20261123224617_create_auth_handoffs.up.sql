-- forge:backward-compatible — a new table only the login broker's sign-in handoff reads and writes; older releases never touch it.

-- Pending sign-in handoffs (forge/pkg/session HandoffSchemaSQL, the library's
-- own columns): a desktop app's request and its one-time code, each keyed by
-- a hash of the value the client holds and sealed under a key derived from
-- it, so a copy of the table redeems nothing. Rows live minutes.
CREATE TABLE auth_handoffs (
    key_hash   bytea       PRIMARY KEY,
    sealed     bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_handoffs_expires_idx ON auth_handoffs (expires_at);
COMMENT ON COLUMN auth_handoffs.key_hash IS 'forge:secret';
COMMENT ON COLUMN auth_handoffs.sealed IS 'forge:secret';
COMMENT ON TABLE auth_handoffs IS 'forge:no-entity';
