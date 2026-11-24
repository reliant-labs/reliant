-- forge:backward-compatible — adds a new table, its indexes and a write-admission trigger; the previous release never reads or writes the table.

-- Migration: tool_call_usage
-- Created: 2026-11-24T20:24:55Z, re-versioned 2026-11-24T22:26:27Z
--
-- Re-versioned: #3483 merged this as 20261124202455, below #3482's
-- 20261124204326 (merged first), so a database that had run 20261124204326
-- never applied it and reliant's migrator refused to start. No release carried
-- 20261124202455; a dev database that already ran it runs this copy as a no-op,
-- which is why every statement is re-runnable (IF NOT EXISTS, OR REPLACE).
--
-- tool_call_usage: the count-only service record of every tool call a run
-- makes, a model's (execute_tools) or a workflow node's (invoke_tool). Written
-- by those activities on the worker, beside the call's execute_tool span; the
-- user's daemon collects nothing. dev-docs/design/agent-analytics.md §3.2,
-- §3.3 ("Tool and daemon error rates"), §3.5, PR A2.
--
-- COUNTS AND METADATA ONLY, the rule llm_call_usage keeps. There is no tool
-- input, output, command line, file name or error text here, and there must
-- never be one: the privacy page lists these columns exactly, and
-- TestLLMUsageTablesHoldNoContent fails when a column appears that it does not
-- name. error_class is a closed set (CHECK below), so a tool's error message
-- cannot reach it. tool_name holds only the name of a tool Reliant ships (a
-- built-in tool or an integration action): the name of an MCP tool or of a
-- workflow's response tool is the user's own, and a name outside the registry
-- is whatever a model wrote, so for those tool_kind says which and tool_name
-- is ''.
--
-- (id, attempt) is one execution. id names the call within its activity:
-- workflow id, Temporal run id, activity id and the call's tool_call_id.
-- attempt is the Temporal attempt: a retried attempt does not run the tool
-- again (it answers "interrupted"), and that answer is the only record of a
-- call whose worker died, so it is its own row. A call answered from the
-- result an earlier dispatch recorded is not executed again and writes no
-- row; only a repeated write of the same attempt is ignored.
--
-- The run columns are llm_call_usage's (20261124162017_llm_call_usage_run_scope),
-- so a tool call and a model call join on them: NULL is "not recorded". ai_run_id and ai_root_run_id are the run in AI monitoring's
-- sense (forge.ai.run.id, step_executions.run_id), which Temporal's run id in
-- id is not.
--
-- duration_ms is the call's execute_tool span's duration, the worker's view of
-- the call, the round trip to the daemon included (the daemon exports no
-- spans).
--
-- Rows are deleted after the retention period (90 days, llmusage.Retention).
-- There is no foreign key: account deletion (internal/accountpurge) and
-- project deletion (internal/store/project_delete.go) delete them by hand.
CREATE TABLE IF NOT EXISTS tool_call_usage (
    id             text NOT NULL,
    attempt        integer NOT NULL,
    user_id        text NOT NULL,
    org_id         text NOT NULL DEFAULT '',
    chat_id        text NOT NULL,
    project_id     text,
    workflow_id    text,
    thread_id      text,
    workflow_name  text,
    node_id        text,
    preset         text,
    ai_run_id      text,
    ai_root_run_id text,
    tool_name      text NOT NULL DEFAULT '',
    tool_kind      text NOT NULL,
    location       text NOT NULL,
    outcome        text NOT NULL,
    error_class    text NOT NULL DEFAULT '',
    duration_ms    bigint NOT NULL DEFAULT 0,
    created_at     timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (id, attempt),
    CONSTRAINT tool_call_usage_tool_kind_check CHECK (tool_kind IN ('builtin', 'integration', 'mcp', 'response', 'unknown')),
    -- Only a tool Reliant ships is named, and a name is a name, not a blob.
    CONSTRAINT tool_call_usage_tool_name_matches_kind CHECK ((tool_kind IN ('builtin', 'integration')) = (tool_name <> '')),
    CONSTRAINT tool_call_usage_tool_name_length CHECK (char_length(tool_name) <= 128),
    -- Where the call ran: on the user's machine through the gateway, or on the worker.
    CONSTRAINT tool_call_usage_location_check CHECK (location IN ('daemon', 'local')),
    CONSTRAINT tool_call_usage_outcome_check CHECK (outcome IN ('success', 'error', 'cancelled')),
    CONSTRAINT tool_call_usage_error_class_check CHECK (error_class IN (
        '', 'refused', 'invalid_input', 'no_workspace', 'daemon_offline', 'dispatch', 'timeout',
        'interrupted', 'tool_error', 'internal', 'unknown')),
    CONSTRAINT tool_call_usage_error_class_matches_outcome CHECK ((outcome = 'error') = (error_class <> '')),
    CONSTRAINT tool_call_usage_counts_nonnegative CHECK (duration_ms >= 0 AND attempt >= 1)
);

-- Retention deletes by created_at; a user's own usage reads by (user_id,
-- created_at); project deletion deletes by chat.
CREATE INDEX IF NOT EXISTS tool_call_usage_created_at_idx ON tool_call_usage (created_at);
CREATE INDEX IF NOT EXISTS tool_call_usage_user_created_at_idx ON tool_call_usage (user_id, created_at);
CREATE INDEX IF NOT EXISTS tool_call_usage_chat_id_idx ON tool_call_usage (chat_id);

-- A tool call in flight when its chat's project is deleted finishes afterwards:
-- the chat_write_admission protocol (20261114221537_chat_purge_write_admission)
-- refuses its row once the chat is purged, as it does every other table keyed by
-- chat with no foreign key. Account deletion fences it as it fences the model
-- calls' ledger (llm_usage_purged_users, store.RecordToolCallUsage).
CREATE OR REPLACE TRIGGER chat_write_admission BEFORE INSERT ON tool_call_usage
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
