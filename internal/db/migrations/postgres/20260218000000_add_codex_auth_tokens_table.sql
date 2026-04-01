-- +goose Up

CREATE TABLE codex_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    id_token TEXT,
    account_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(user_id)
);

CREATE INDEX idx_codex_auth_tokens_user ON codex_auth_tokens(user_id);

-- +goose Down

DROP TABLE IF EXISTS codex_auth_tokens;
