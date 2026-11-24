-- forge:backward-compatible — one nullable column with no default the previous release never reads; its inserts omit it and leave NULL.

-- Migration: llm_call_usage_gateway_call_id
-- Created: 2026-11-24T18:30:39Z
--
-- The LLM gateway's own id for a model call (dev-docs/design/agent-analytics.md
-- §3.2): LiteLLM's x-litellm-call-id, which control-plane's wallet ledger stores
-- as the call's provider_request_id. It is not this table's provider_request_id,
-- which is the upstream provider's x-oai-request-id (the span's
-- gen_ai.response.id). With it a row joins exactly to what the gateway billed,
-- as the call's chat span already does through reliant.gateway.call_id.
--
-- An opaque id: TestLLMUsageTablesHoldNoContent and the privacy page list it.
-- NULL for a call that did not go through the gateway (a personal API key, a
-- subscription login, a local model), a request the driver failed and retried,
-- and every row written before this column.
--
-- ADD COLUMN with no default is a catalog change, not a rewrite.
--
-- Post-deploy check: gateway-routed calls written after the deploy carry it:
--   SELECT count(*) FILTER (WHERE gateway_call_id IS NOT NULL), count(*)
--   FROM llm_call_usage WHERE provider = 'reliant' AND attempt >= 1
--     AND created_at > now() - interval '1 hour';
-- Rollback: none needed; the previous release ignores the column. Dropping it
-- (ALTER TABLE llm_call_usage DROP COLUMN gateway_call_id) loses only the ids.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage ADD COLUMN gateway_call_id text;

COMMENT ON COLUMN llm_call_usage.gateway_call_id IS 'The LLM gateway''s id for the call (x-litellm-call-id; control-plane''s wallet ledger provider_request_id; the span''s reliant.gateway.call_id). NULL when the call did not go through the gateway, or not recorded.';
