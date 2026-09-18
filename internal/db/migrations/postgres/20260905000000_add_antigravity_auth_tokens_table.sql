-- +goose Up

-- Antigravity authenticates via Google OAuth. Its token response carries
-- expires_in directly, so the expiry is STORED (the Claude shape) rather than
-- derived from a JWT exp claim the way codex_auth_tokens does it.
CREATE TABLE antigravity_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    expires_at TIMESTAMPTZ,
    id_token TEXT,
    scope TEXT,
    -- TIMESTAMPTZ, not TIMESTAMP: 20260728000000_timestamps_to_timestamptz.sql
    -- converted every existing column and runs BEFORE this migration, so a
    -- bare TIMESTAMP here would be the only naive column left in the schema.
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id)
);

CREATE INDEX idx_antigravity_auth_tokens_user ON antigravity_auth_tokens(user_id);

-- +goose Down

DROP TABLE IF EXISTS antigravity_auth_tokens;
