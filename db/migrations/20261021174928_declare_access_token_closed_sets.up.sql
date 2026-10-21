-- forge:backward-compatible — only catalog comments; no row or column changes

-- Migration: declare_access_token_closed_sets
-- Created: 2026-10-21T17:22:52Z
--
-- Declares the access_tokens CHECKs that mirror a forge closed set, so
-- `forge lint --closed-sets` fails the moment forge's set and the CHECK
-- diverge (a forge bump that adds a scope), hermetically, on the PR that
-- bumps the pin.
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS 'forge:closed-set accesstoken.AllScopes';
COMMENT ON CONSTRAINT access_tokens_resource_kind ON access_tokens IS 'forge:closed-set accesstoken.AllResourceKinds';
