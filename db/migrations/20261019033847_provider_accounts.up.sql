-- forge:backward-compatible — additive: three new tables and one nullable column (no default, so no table rewrite) that the previous release never reads or writes; the legacy token tables are untouched.

-- Multi-account LLM providers. One row per connected credential (OAuth session,
-- API key, device token, managed key). Replaces the four *_auth_tokens tables
-- and the LLM rows of api_keys in a later contract release; until then the boot
-- copy (internal/db/legacy_credentials.go) mirrors them here.

CREATE TABLE provider_accounts (
    id                   text PRIMARY KEY,
    user_id              text NOT NULL,
    provider             text NOT NULL,
    auth_kind            text NOT NULL CHECK (auth_kind IN ('oauth', 'api_key', 'device', 'managed')),
    identity_key         text NOT NULL,
    label                text NOT NULL DEFAULT '',
    email                text,
    external_account_id  text,
    organization_id      text,
    organization_name    text,
    plan                 text,
    plan_raw             text,
    key_hint             text,
    priority             integer NOT NULL,
    paused_at            timestamptz,
    needs_reauth_at      timestamptz,
    needs_reauth_reason  text,
    secret_sealed        bytea NOT NULL,
    refresh_sealed       bytea,
    id_token_sealed      bytea,
    expires_at           timestamptz,
    scope                text,
    legacy_source        text,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_accounts_identity_key UNIQUE (user_id, provider, identity_key),
    CONSTRAINT provider_accounts_priority_key UNIQUE (user_id, provider, priority) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT provider_accounts_one_managed CHECK (auth_kind <> 'managed' OR identity_key = 'managed')
);
CREATE INDEX provider_accounts_user ON provider_accounts (user_id, provider, priority);

-- Latest limit state per account. No secrets, so hot writes stay off the sealed table.
CREATE TABLE provider_account_limits (
    account_id            text PRIMARY KEY REFERENCES provider_accounts(id) ON DELETE CASCADE,
    user_id               text NOT NULL,
    status                text NOT NULL DEFAULT 'unknown',
    windows               jsonb NOT NULL DEFAULT '[]',
    representative_window text,
    source                text NOT NULL DEFAULT 'headers',
    observed_at           timestamptz NOT NULL,
    limited_until         timestamptz,
    limited_window        text,
    limited_probes        integer NOT NULL DEFAULT 0,
    usage_api_next_at     timestamptz,
    usage_api_failures    integer NOT NULL DEFAULT 0,
    usage_api_error       text,
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX provider_account_limits_user ON provider_account_limits (user_id);
CREATE INDEX provider_account_limits_poll ON provider_account_limits (usage_api_next_at) WHERE usage_api_next_at IS NOT NULL;

-- Sticky account per (thread, provider).
CREATE TABLE thread_provider_accounts (
    thread_id    text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    provider     text NOT NULL,
    account_id   text NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    assigned_at  timestamptz NOT NULL,
    reason       text NOT NULL,
    PRIMARY KEY (thread_id, provider)
);
CREATE INDEX thread_provider_accounts_recent ON thread_provider_accounts (account_id, assigned_at);

-- Which account served an assistant message. No foreign key: history outlives a removed account.
ALTER TABLE messages ADD COLUMN provider_account_id text;
