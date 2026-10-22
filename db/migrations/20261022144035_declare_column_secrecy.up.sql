-- forge:backward-compatible — comments only; no schema change, the previous release never reads them
--
-- Declare which credential-shaped columns the read-only forge_ro role may see.
-- forge:secret withholds a column; forge:readable <reason> grants one whose name
-- only looks like a credential. None of these columns had a comment before, so
-- replacing the whole comment loses nothing.

-- Withheld: sealed provider tokens, hashes of credentials, ciphertext, and
-- columns that can hold user content (attachment bytes, Temporal payloads).
COMMENT ON COLUMN access_tokens.token_hash IS 'forge:secret';
COMMENT ON COLUMN antigravity_auth_tokens.access_token_sealed IS 'forge:secret';
COMMENT ON COLUMN antigravity_auth_tokens.id_token_sealed IS 'forge:secret';
COMMENT ON COLUMN antigravity_auth_tokens.refresh_token_sealed IS 'forge:secret';
COMMENT ON COLUMN api_keys.api_key IS 'forge:secret';
COMMENT ON COLUMN api_keys.api_key_sealed IS 'forge:secret';
COMMENT ON COLUMN attachments.content IS 'forge:secret';
COMMENT ON COLUMN claude_auth_tokens.access_token_sealed IS 'forge:secret';
COMMENT ON COLUMN claude_auth_tokens.refresh_token_sealed IS 'forge:secret';
COMMENT ON COLUMN codex_auth_tokens.access_token_sealed IS 'forge:secret';
COMMENT ON COLUMN codex_auth_tokens.id_token_sealed IS 'forge:secret';
COMMENT ON COLUMN codex_auth_tokens.refresh_token_sealed IS 'forge:secret';
COMMENT ON COLUMN connection_secrets.ciphertext IS 'forge:secret';
COMMENT ON COLUMN copilot_auth_tokens.github_access_token_sealed IS 'forge:secret';
COMMENT ON COLUMN copilot_auth_tokens.github_refresh_token_sealed IS 'forge:secret';
COMMENT ON COLUMN oauth_flows.pkce_verifier_sealed IS 'forge:secret';
COMMENT ON COLUMN oauth_flows.session_id_hash IS 'forge:secret';
COMMENT ON COLUMN oauth_flows.state_hash IS 'forge:secret';
COMMENT ON COLUMN provider_accounts.id_token_sealed IS 'forge:secret';
COMMENT ON COLUMN provider_accounts.refresh_sealed IS 'forge:secret';
COMMENT ON COLUMN provider_accounts.secret_sealed IS 'forge:secret';
COMMENT ON COLUMN temporal_payload_blobs.data IS 'forge:secret';
COMMENT ON COLUMN triggers.webhook_secret_sealed IS 'forge:secret';
COMMENT ON COLUMN triggers.webhook_token_hash IS 'forge:secret';
COMMENT ON COLUMN vault_keys.wrapped_dek IS 'forge:secret';

-- Granted: not credentials.
COMMENT ON COLUMN access_tokens.token_prefix IS 'forge:readable — non-secret display head of an rlat_ token (type prefix plus 8 characters), shown in token lists and the CLI';
COMMENT ON COLUMN connector_grants.token_prefix IS 'forge:readable — non-secret display head of the grant token, shown in the connector list; the token itself is held by the token authority';
COMMENT ON COLUMN attachments.file_hash IS 'forge:readable — hex SHA-256 of the file content, a dedupe key, not a credential';
