-- +goose Up
CREATE TABLE IF NOT EXISTS daemon_pats (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    name TEXT NOT NULL,
    ephemeral BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_daemon_pats_token_hash ON daemon_pats(token_hash);
CREATE INDEX IF NOT EXISTS idx_daemon_pats_user_id ON daemon_pats(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_daemon_pats_user_id;
DROP INDEX IF EXISTS idx_daemon_pats_token_hash;
DROP TABLE IF EXISTS daemon_pats;
