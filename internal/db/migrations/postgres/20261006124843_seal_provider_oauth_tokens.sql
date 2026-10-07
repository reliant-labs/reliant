-- +goose Up

-- Provider sign-in tokens (Claude, Codex, GitHub Copilot, Antigravity) move
-- from plaintext TEXT columns to vault-sealed BYTEA: the same envelope
-- encryption api_keys.api_key_sealed and connection_secrets.ciphertext use, a
-- per-user data key wrapped by the RELIANT_VAULT_KEY key-encryption key
-- (internal/vault). Each value is sealed with its table, user and column as
-- associated data, so a ciphertext copied to another row or column does not
-- open.
--
-- Existing rows are DELETED, not converted. SQL cannot seal — the key lives in
-- the application, not the database — and carrying a plaintext fallback read
-- path is precisely what this migration exists to remove. Pre-launch that costs
-- a user one reconnect: with no row, every reader already reports the provider
-- as not connected and Settings offers Connect, exactly as for someone who
-- never signed in.
--
-- Roll-forward only. A release still reading the plaintext columns fails its
-- token queries against this schema for the length of the deploy; that is the
-- accepted cost of not keeping both shapes.
--
-- Every sealed column is NOT NULL: an absent refresh or id token is sealed as
-- the empty string, so a row always opens the same way.

TRUNCATE claude_auth_tokens, codex_auth_tokens, copilot_auth_tokens, antigravity_auth_tokens;

ALTER TABLE claude_auth_tokens
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    ADD COLUMN access_token_sealed BYTEA NOT NULL,
    ADD COLUMN refresh_token_sealed BYTEA NOT NULL;

ALTER TABLE codex_auth_tokens
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    DROP COLUMN id_token,
    ADD COLUMN access_token_sealed BYTEA NOT NULL,
    ADD COLUMN refresh_token_sealed BYTEA NOT NULL,
    ADD COLUMN id_token_sealed BYTEA NOT NULL;

ALTER TABLE copilot_auth_tokens
    DROP COLUMN github_access_token,
    DROP COLUMN github_refresh_token,
    ADD COLUMN github_access_token_sealed BYTEA NOT NULL,
    ADD COLUMN github_refresh_token_sealed BYTEA NOT NULL;

ALTER TABLE antigravity_auth_tokens
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    DROP COLUMN id_token,
    ADD COLUMN access_token_sealed BYTEA NOT NULL,
    ADD COLUMN refresh_token_sealed BYTEA NOT NULL,
    ADD COLUMN id_token_sealed BYTEA NOT NULL;
