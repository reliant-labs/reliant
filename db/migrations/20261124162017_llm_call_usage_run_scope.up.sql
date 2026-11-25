-- forge:backward-compatible — nullable columns with no default and NOT VALID checks the previous release never reads; its inserts omit them and leave NULL, which every check admits.

-- Migration: llm_call_usage_run_scope
-- Created: 2026-11-24T16:20:17Z
--
-- What a model call was part of, and what it cost (dev-docs/design/agent-analytics.md
-- §3.2, §4, PR A1). The recorder fills them from the run scope the activity
-- already opens its spans from (telemetry.RunScope), so a call joins to its
-- run, node and workflow without the span store, whose traces live seven days.
--
-- run_id stays Temporal's run id: it is part of the row's identity (id) and
-- of every row written so far. The run in AI monitoring's sense, the one
-- step_executions.run_id and the spans' forge.ai.run.id name, is ai_run_id;
-- ai_root_run_id is the run at the top of a sub-agent's spawn chain, which is
-- what step_executions.run_id holds for every node of that chain.
--
-- Opaque ids, closed-set labels and counts only: TestLLMUsageTablesHoldNoContent
-- and the privacy page list them. NULL is "not recorded" (a row written before
-- these columns, or a call outside a run: an automation draft has no chat or
-- project). In particular a NULL cost is unknown, never zero.
--
-- ADD COLUMN with no default is a catalog change, not a rewrite. The CHECKs
-- are added NOT VALID, so the lock is held only for the catalog change;
-- 20261124162018_llm_call_usage_run_scope_validate validates the existing rows
-- (all NULL) in its own transaction.
--
-- Post-deploy verification and rollback: see the _validate file that follows.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage
    ADD COLUMN workflow_id         text,
    ADD COLUMN thread_id           text,
    ADD COLUMN workflow_name       text,
    ADD COLUMN node_id             text,
    ADD COLUMN preset              text,
    ADD COLUMN project_id          text,
    ADD COLUMN ai_run_id           text,
    ADD COLUMN ai_root_run_id      text,
    ADD COLUMN provider_request_id text,
    ADD COLUMN ttft_ms             bigint,
    ADD COLUMN cost_usd_nanos      bigint,
    ADD COLUMN cost_basis          text;

-- cost_basis is a closed set (§4): reported (the gateway priced the call),
-- estimated (a list price at the call's date) or unpriced (a subscription or
-- local model: tokens only, a seat is not a per-call price). A cost is present
-- exactly when the basis prices the call, so an unpriced or unwritten call can
-- never be summed as zero.
ALTER TABLE llm_call_usage ADD CONSTRAINT llm_call_usage_cost_basis_check
    CHECK (cost_basis IN ('reported', 'estimated', 'unpriced')) NOT VALID;
ALTER TABLE llm_call_usage ADD CONSTRAINT llm_call_usage_cost_matches_basis
    CHECK ((cost_usd_nanos IS NOT NULL) = (cost_basis IS NOT NULL AND cost_basis <> 'unpriced')) NOT VALID;
ALTER TABLE llm_call_usage ADD CONSTRAINT llm_call_usage_run_scope_nonnegative
    CHECK (cost_usd_nanos >= 0 AND ttft_ms >= 0) NOT VALID;

COMMENT ON COLUMN llm_call_usage.workflow_id IS 'The Temporal workflow id of the run the call was made in (workflows.id, step_executions.workflow_id). NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.thread_id IS 'The thread (agent) that made the call. NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.workflow_name IS 'The workflow the run executes (forge.ai.workflow.name). NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.node_id IS 'The workflow node the call ran in (forge.ai.node.id). NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.preset IS 'The agent preset the call ran as (gen_ai.agent.name when it has one). NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.project_id IS 'The project of the chat the call belonged to. NULL for a call with no chat, or not recorded.';
COMMENT ON COLUMN llm_call_usage.ai_run_id IS 'The run in AI monitoring''s sense (forge.ai.run.id), unlike run_id, which is Temporal''s run id. NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.ai_root_run_id IS 'The root run of ai_run_id''s spawn chain (forge.ai.root_run.id; step_executions.run_id). NULL when not recorded.';
COMMENT ON COLUMN llm_call_usage.provider_request_id IS 'The request id the provider answered with (the span''s gen_ai.response.id). NULL when the provider sent none.';
COMMENT ON COLUMN llm_call_usage.ttft_ms IS 'Time from the request to its first output token. NULL when no token arrived.';
COMMENT ON COLUMN llm_call_usage.cost_usd_nanos IS 'The call''s cost in billionths of a US dollar, on cost_basis. NULL is unknown, never zero.';
COMMENT ON COLUMN llm_call_usage.cost_basis IS 'What cost_usd_nanos rests on: reported, estimated or unpriced. NULL when the call is not priced yet.';
