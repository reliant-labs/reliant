-- +goose Up
CREATE TABLE daemon_pats (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    name TEXT NOT NULL,
    ephemeral INTEGER NOT NULL DEFAULT 0,
    expires_at DATETIME,
    last_used_at DATETIME,
    revoked_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_daemon_pats_token_hash ON daemon_pats(token_hash);
CREATE INDEX idx_daemon_pats_user_id ON daemon_pats(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_daemon_pats_user_id;
DROP INDEX IF EXISTS idx_daemon_pats_token_hash;
DROP TABLE IF EXISTS daemon_pats;
