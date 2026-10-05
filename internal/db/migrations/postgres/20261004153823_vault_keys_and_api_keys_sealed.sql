-- +goose Up

-- Credential vault, phase 1a (research/CONNECTIONS_VAULT.md §4.1, §2.5).
-- One wrapped data-encryption key per tenant; the key-encryption key that wraps
-- it lives outside the database (RELIANT_VAULT_KEY).
CREATE TABLE IF NOT EXISTS vault_keys (
  id           text PRIMARY KEY,
  tenant_kind  text NOT NULL CHECK (tenant_kind IN ('user', 'org')),
  tenant_id    text NOT NULL,
  version      int  NOT NULL,
  kek_id       text NOT NULL,
  wrapped_dek  bytea NOT NULL,
  state        text NOT NULL CHECK (state IN ('primary', 'decrypt_only', 'destroyed')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_kind, tenant_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS vault_keys_one_primary
  ON vault_keys (tenant_kind, tenant_id) WHERE state = 'primary';

-- Expand step: sealed copy of api_keys.api_key. Writers dual-write and readers
-- prefer it; the plaintext column is blanked in a later release.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS api_key_sealed bytea;
