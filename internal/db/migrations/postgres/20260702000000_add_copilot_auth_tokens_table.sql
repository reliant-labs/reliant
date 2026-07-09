-- +goose Up

CREATE TABLE copilot_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    github_access_token TEXT NOT NULL,
    github_refresh_token TEXT,
    tier TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(user_id)
);

CREATE INDEX idx_copilot_auth_tokens_user ON copilot_auth_tokens(user_id);

-- +goose Down

DROP TABLE IF EXISTS copilot_auth_tokens;
