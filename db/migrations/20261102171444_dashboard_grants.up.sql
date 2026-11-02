-- forge:backward-compatible — grants only; no schema change, so the previous release reads and writes exactly as before.

-- Live dashboard panels (forge db/dashboards) query as forge_dash, the role the
-- migrator creates NOLOGIN before any migration runs; KCL's
-- ManagedDatabase.dashboards gives it a login. It reads exactly the columns
-- granted here and nothing else. Every grant is a column list, never a whole
-- table, so a column added later is not readable until a migration grants it.
--
-- No PII, free text or secrets: chats.title, provider_accounts.email / label /
-- key_hint / organization_name and the sealed credentials are never granted. A
-- panel that needs a person groups by an opaque id (user_id). The tables are in
-- public, which every role may already use, so no schema USAGE is granted.
--
-- Do not add SECURITY DEFINER functions over these tables: one runs as its owner
-- and would read columns forge_dash is denied. Remove access with a REVOKE in a
-- later migration; never edit this one.

-- Spend and token volume by model, provider account, role and chat.
GRANT SELECT (chat_id, cost, created_at, model, provider_account_id, role, token_count)
    ON messages TO forge_dash;

-- Activity by user and project; id joins messages.chat_id.
GRANT SELECT (id, user_id, project_id, state, created_at, last_active)
    ON chats TO forge_dash;

-- Spend by provider and credential kind; id joins messages.provider_account_id.
GRANT SELECT (id, provider, auth_kind, user_id)
    ON provider_accounts TO forge_dash;

-- The count-only record of every model call (no content by construction).
-- id and run_id name the workflow that made the call; a panel needs neither.
GRANT SELECT (user_id, org_id, chat_id, provider, model, auth_mode, purpose,
              input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
              outcome, error_class, latency_ms, attempt, created_at)
    ON llm_call_usage TO forge_dash;

-- Per-day totals of llm_call_usage, kept past its retention: every column is a
-- count or a dimension of one.
GRANT SELECT (user_id, day, org_id, provider, model, auth_mode, purpose,
              calls, error_calls, input_tokens, output_tokens,
              cache_read_tokens, cache_write_tokens, latency_ms_total, updated_at)
    ON llm_call_usage_daily TO forge_dash;
