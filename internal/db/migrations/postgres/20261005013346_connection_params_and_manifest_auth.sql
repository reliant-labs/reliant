-- +goose Up

-- Manifest-declared auth (research/INTEGRATIONS_V1_BRIEF.md §3.1).
--
-- params: a connection's non-secret settings, declared by the integration's
-- connection_params (a Shopify shop, a Zendesk subdomain). base_url templates
-- read them. Never a secret: secrets stay in connection_secrets.
ALTER TABLE connections ADD COLUMN IF NOT EXISTS params jsonb NOT NULL DEFAULT '{}'::jsonb;
-- A new OAuth connection's params are entered before the redirect (the shop
-- decides the authorize URL), so the flow row carries them to the callback.
ALTER TABLE oauth_flows ADD COLUMN IF NOT EXISTS params jsonb NOT NULL DEFAULT '{}'::jsonb;

-- A GitHub App user-to-server token is an OAuth 2.0 token with refresh; the
-- integration's manifest, not the auth kind, now says it is GitHub. Fold the
-- special kind into oauth2 before narrowing the check.
UPDATE connections SET auth_kind = 'oauth2' WHERE auth_kind = 'github_app_user';
ALTER TABLE connections DROP CONSTRAINT IF EXISTS connections_auth_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_auth_kind_check
  CHECK (auth_kind IN ('oauth2', 'api_key', 'basic', 'none'));

-- auth_header was the closed allow-list choice for api_key connections. A
-- manifest that declares where its key goes no longer needs it; it survives
-- for integrations that leave placement to each connection (generic HTTP).
COMMENT ON COLUMN connections.auth_header IS
  'api_key connections of an integration that does not declare placement: the allow-listed header choice';
