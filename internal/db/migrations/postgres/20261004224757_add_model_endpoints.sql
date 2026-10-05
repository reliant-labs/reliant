-- +goose Up
-- Custom model endpoints: a user's OpenAI-compatible servers (a GPU cluster,
-- vLLM, LM Studio, a hosted inference API) that Reliant runs models on. See
-- proto/reliant/v1/model_endpoint.proto for the semantics of every column.
--
-- NO SECRETS LIVE HERE. An endpoint's API key and header values are the
-- user's credentials for that server, and credentials have exactly one home:
-- the sealed connection store (connections + connection_secrets, vault-sealed
-- under the user's DEK). credential_connection_id points at that connection;
-- NULL means the endpoint needs no credential. Keeping a second secret column
-- here would mean a second seal path, a second purge path, and a second place
-- a plaintext key could leak from.
--
-- route: 'direct' (Reliant's servers call base_url; private/loopback refused
-- in hosted mode) or 'via_daemon' (daemon_id's machine relays, for localhost,
-- LAN and VPN-only servers).
--
-- models_json: the user's per-model settings (protojson of repeated
-- ModelEndpointModel). probe_json: the last probe result (protojson
-- LocalModelEndpoint). Both are owned by the server and replaced wholesale.
CREATE TABLE IF NOT EXISTS model_endpoints (
  id                       text PRIMARY KEY,
  user_id                  text NOT NULL,
  name                     text NOT NULL,
  base_url                 text NOT NULL,
  route                    text NOT NULL CHECK (route IN ('direct', 'via_daemon')),
  daemon_id                text,
  credential_connection_id text,
  header_names             text[] NOT NULL DEFAULT '{}',
  models_json              text NOT NULL DEFAULT '[]',
  probe_json               text NOT NULL DEFAULT '',
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now(),
  CHECK ((route = 'via_daemon') = (daemon_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_model_endpoints_user ON model_endpoints (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_model_endpoints_user_name ON model_endpoints (user_id, name);
