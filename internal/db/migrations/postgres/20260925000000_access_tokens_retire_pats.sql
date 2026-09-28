-- +goose Up
--
-- ONE machine credential: `rlat_` access tokens (forge/pkg/accesstoken).
--
-- In a hosted deployment reliant owns NO token rows: control-plane's
-- controlplane.access_tokens is the one table and reliant reaches it through
-- AccessTokenInternalService. This table is the SELF-HOSTED store — the same
-- token system with a local store, used only when no control-plane is
-- configured (internal/tokenauthority picks exactly one, never both).
--
-- It mirrors controlplane.access_tokens column for column and CHECK for CHECK,
-- so a token behaves identically in either mode. Two deliberate differences:
--   * org_id carries no FK — self-hosted reliant has no organizations table.
--     Personal tokens use the acting user's id as their org; org automation
--     tokens use the single 'self-hosted' org.
--   * acting_user_id / created_by_user_id carry no FK — reliant's user id is
--     the IdP subject and there is no users table to reference.
--
-- HARD CUT-OVER (no backwards compatibility; nothing has launched): the
-- retired families are dropped here.
--   * daemon_pats — `rlnt_pat_` daemon and API PATs. Daemons re-register;
--     CLI users re-mint.
--   * connector_grants.token_hash — `rlnt_conn_` connector credentials. A
--     grant keeps its POLICY (tools, path root, exec mode) and its
--     token_prefix (now the DISPLAY prefix of its credential); the credential
--     itself is an `rlat_` with mcp:connector bound to the grant.
--     Existing grants are revoked: their credentials no longer exist, and a
--     live grant with no credential would be unreachable yet look usable.

-- IF NOT EXISTS, on every statement here, because this file once ran under a
-- DIFFERENT version. It shipped as 20260923000000 in #293 — a version
-- 20260923000000_temporal_payload_blobs.sql already owned — and was renumbered
-- to 20260925000000 an hour later in #294. A database that ran main in that
-- window has these objects already, recorded under the wrong version, and
-- 20260925000000 still pending; a bare CREATE TABLE fails there and api-server
-- cannot start. Guarding lets such a database roll forward on plain startup.
-- Databases that recorded 20260925000000 never re-run this, and on a fresh
-- database the result is byte-identical.
CREATE TABLE IF NOT EXISTS access_tokens (
    id                  text PRIMARY KEY,
    org_id              text NOT NULL,
    name                text NOT NULL,
    token_hash          text NOT NULL UNIQUE,
    token_prefix        text NOT NULL,
    scopes              text[] NOT NULL,
    created_by_user_id  text,
    acting_user_id      text,
    resource_kind       text,
    resource_id         text,
    ephemeral           boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz,
    last_used_at        timestamptz,
    revoked_at          timestamptz,

    CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read', 'deploy:write',
            'token:read', 'token:write',
            'reliant:api', 'daemon:connect', 'llm:invoke',
            'proxy:port', 'mcp:connector',
            'secret:read', 'secret:write'
        ]::text[]
    ),
    CONSTRAINT access_tokens_name_present CHECK (length(trim(name)) > 0),
    CONSTRAINT access_tokens_expiry_after_creation CHECK (expires_at IS NULL OR expires_at > created_at),
    CONSTRAINT access_tokens_resource_pair CHECK ((resource_kind IS NULL) = (resource_id IS NULL)),
    CONSTRAINT access_tokens_resource_kind CHECK (resource_kind IS NULL OR resource_kind IN ('daemon', 'port', 'connector')),
    CONSTRAINT access_tokens_ephemeral_bound CHECK (NOT ephemeral OR resource_kind IS NOT NULL),
    CONSTRAINT access_tokens_ephemeral_expires CHECK (NOT ephemeral OR expires_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS access_tokens_live_hash ON access_tokens (token_hash) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS access_tokens_live_resource ON access_tokens (resource_kind, resource_id)
    WHERE revoked_at IS NULL AND resource_kind IS NOT NULL;
CREATE INDEX IF NOT EXISTS access_tokens_live_acting_user ON access_tokens (acting_user_id, name)
    WHERE revoked_at IS NULL AND acting_user_id IS NOT NULL;

DROP TABLE IF EXISTS daemon_pats;

UPDATE connector_grants SET revoked_at = now() WHERE revoked_at IS NULL;
DROP INDEX IF EXISTS idx_connector_grants_token_hash;
ALTER TABLE connector_grants DROP COLUMN IF EXISTS token_hash;

