-- +goose Up

CREATE TABLE claude_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    expires_at DATETIME,
    account_uuid TEXT,
    account_email TEXT,
    organization_uuid TEXT,
    organization_name TEXT,
    scope TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id)
);

CREATE INDEX idx_claude_auth_tokens_user ON claude_auth_tokens(user_id);

-- +goose Down

DROP TABLE IF EXISTS claude_auth_tokens;
