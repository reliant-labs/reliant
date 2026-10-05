-- +goose Up

-- Credential vault, phase 1b (research/CONNECTIONS_VAULT.md §4.1).
-- reliant has no users table, so user_id is a plain column; the account purge
-- deletes these rows explicitly.
CREATE TABLE IF NOT EXISTS connections (
  id                  text PRIMARY KEY,
  owner_kind          text NOT NULL DEFAULT 'user' CHECK (owner_kind IN ('user', 'org')),
  user_id             text NOT NULL,
  org_id              text NULL,
  integration_id      text NOT NULL,
  auth_kind           text NOT NULL CHECK (auth_kind IN ('oauth2', 'github_app_user', 'api_key', 'basic', 'none')),
  name                text NOT NULL,
  account_label       text NULL,
  external_account_id text NULL,
  scopes              text[] NOT NULL DEFAULT '{}',
  oauth_client        text NULL,
  -- api_key connections: which allow-listed header carries the key (NULL otherwise).
  auth_header         text NULL,
  status              text NOT NULL CHECK (status IN ('active', 'needs_reauth', 'revoked')),
  status_reason       text NULL,
  is_default          boolean NOT NULL DEFAULT false,
  access_expires_at   timestamptz NULL,
  last_used_at        timestamptz NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  deleted_at          timestamptz NULL,
  CHECK ((owner_kind = 'user' AND org_id IS NULL) OR (owner_kind = 'org' AND org_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS connections_one_default
  ON connections (user_id, integration_id)
  WHERE is_default AND deleted_at IS NULL AND owner_kind = 'user';
CREATE UNIQUE INDEX IF NOT EXISTS connections_name
  ON connections (user_id, integration_id, name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS connections_user ON connections (user_id) WHERE deleted_at IS NULL;

-- Ciphertext only, in its own table so a SELECT on connections cannot return it.
CREATE TABLE IF NOT EXISTS connection_secrets (
  connection_id text NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  field         text NOT NULL CHECK (field IN ('access_token', 'refresh_token', 'api_key', 'password', 'client_secret')),
  vault_key_id  text NOT NULL REFERENCES vault_keys(id),
  ciphertext    bytea NOT NULL,
  generation    bigint NOT NULL DEFAULT 1,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (connection_id, field)
);

-- Single-use, session-bound OAuth flow state.
CREATE TABLE IF NOT EXISTS oauth_flows (
  state_hash              bytea PRIMARY KEY,
  user_id                 text NOT NULL,
  session_id_hash         bytea NOT NULL,
  integration_id          text NOT NULL,
  pkce_verifier_sealed    bytea NOT NULL,
  redirect_after          text NULL,
  reconnect_connection_id text NULL,
  connection_name         text NULL,
  expires_at              timestamptz NOT NULL,
  consumed_at             timestamptz NULL
);
CREATE INDEX IF NOT EXISTS oauth_flows_expires ON oauth_flows (expires_at);

-- Append-only audit.
CREATE TABLE IF NOT EXISTS connection_events (
  id            bigserial PRIMARY KEY,
  connection_id text NOT NULL,
  user_id       text NOT NULL,
  kind          text NOT NULL,
  run_id        text NULL,
  node_id       text NULL,
  tool_call_id  text NULL,
  actor         text NOT NULL,
  at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS connection_events_conn ON connection_events (connection_id, id DESC);
