-- forge:backward-compatible — adds three new tables; the previous release never reads or writes them.

-- llm_call_usage: the count-only service record of every model call, whichever
-- credential made it (Reliant AI, the user's own API key, a Claude / Codex /
-- Copilot subscription, a local model). Written by the CallLLM activity on the
-- worker; the user's daemon collects nothing.
--
-- COUNTS AND METADATA ONLY. There is no prompt, reply, tool input, file name or
-- error text here, and there must never be one: the privacy page lists these
-- columns exactly, and TestLLMUsageTablesHoldNoContent fails when a column
-- appears that it does not name. error_class is a closed set (CHECK below), so
-- a raw provider error string cannot reach it.
--
-- (id, attempt) is one provider request. id names the logical request: workflow
-- id, run id, activity id, what it was for (purpose) and its ordinal within the
-- activity (a title or a compaction may make several); attempt is the Temporal
-- attempt that made it. Temporal re-runs an
-- activity after a failure and every attempt is a real request to the provider
-- that may have consumed tokens, so each attempt is its own row and is counted;
-- only a repeated write of the same attempt is ignored. purpose says what the
-- call was for: a chat turn, a compaction summary, a chat title or an
-- automation draft.
-- Rows are deleted after the retention period (90 days); llm_call_usage_daily keeps
-- the totals.
CREATE TABLE llm_call_usage (
    id                 text NOT NULL,
    user_id            text NOT NULL,
    org_id             text NOT NULL DEFAULT '',
    chat_id            text NOT NULL,
    run_id             text NOT NULL DEFAULT '',
    provider           text NOT NULL,
    model              text NOT NULL,
    auth_mode          text NOT NULL,
    purpose            text NOT NULL,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    outcome            text NOT NULL,
    error_class        text NOT NULL DEFAULT '',
    latency_ms         bigint NOT NULL DEFAULT 0,
    attempt            integer NOT NULL,
    created_at         timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (id, attempt),
    CONSTRAINT llm_call_usage_auth_mode_check CHECK (auth_mode IN ('managed', 'byok', 'subscription', 'local')),
    CONSTRAINT llm_call_usage_purpose_check CHECK (purpose IN ('chat', 'compaction', 'title', 'draft_automation')),
    CONSTRAINT llm_call_usage_outcome_check CHECK (outcome IN ('success', 'error', 'cancelled')),
    CONSTRAINT llm_call_usage_error_class_check CHECK (error_class IN (
        '', 'rate_limit', 'usage_limit', 'context_length', 'auth', 'timeout', 'stalled', 'network', 'unknown')),
    CONSTRAINT llm_call_usage_error_class_matches_outcome CHECK ((outcome = 'error') = (error_class <> '')),
    CONSTRAINT llm_call_usage_counts_nonnegative CHECK (
        input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
        AND latency_ms >= 0 AND attempt >= 1)
);

-- Retention deletes by created_at; a user's own usage reads by (user_id, created_at).
CREATE INDEX llm_call_usage_created_at_idx ON llm_call_usage (created_at);
CREATE INDEX llm_call_usage_user_created_at_idx ON llm_call_usage (user_id, created_at);

-- llm_call_usage_daily: the same counts summed per UTC day, user, org, provider, model,
-- auth mode and purpose. Kept after the per-call rows are deleted. Maintained in the
-- transaction that inserts the per-call row, so it is never behind and a repeated
-- write of one attempt never counts twice. purpose is part of the key so the split
-- of chat, compaction, title and draft usage survives the 90-day retention.
CREATE TABLE llm_call_usage_daily (
    user_id            text NOT NULL,
    day                date NOT NULL,
    org_id             text NOT NULL DEFAULT '',
    provider           text NOT NULL,
    model              text NOT NULL,
    auth_mode          text NOT NULL,
    purpose            text NOT NULL,
    calls              bigint NOT NULL DEFAULT 0,
    error_calls        bigint NOT NULL DEFAULT 0,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    latency_ms_total   bigint NOT NULL DEFAULT 0,
    updated_at         timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day, org_id, provider, model, auth_mode, purpose),
    CONSTRAINT llm_call_usage_daily_auth_mode_check CHECK (auth_mode IN ('managed', 'byok', 'subscription', 'local')),
    CONSTRAINT llm_call_usage_daily_purpose_check CHECK (purpose IN ('chat', 'compaction', 'title', 'draft_automation')),
    CONSTRAINT llm_call_usage_daily_counts_nonnegative CHECK (
        calls >= 0 AND error_calls >= 0 AND error_calls <= calls
        AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
        AND latency_ms_total >= 0)
);

-- llm_usage_purged_users: a fence for account deletion, holding only an opaque
-- user id and when that account was purged. A model call that was already in
-- flight when its account was deleted finishes afterwards and would otherwise
-- write usage under a user that no longer exists, which would outlive the
-- deletion (the rollup is kept indefinitely). The purge takes a per-user
-- advisory lock and writes the row in its transaction; every usage write takes
-- the same lock and refuses a user listed here. Rows are removed after
-- 7 days, longer than any activity or its retries run.
CREATE TABLE llm_usage_purged_users (
    user_id   text PRIMARY KEY,
    purged_at timestamp with time zone NOT NULL DEFAULT now()
);
COMMENT ON TABLE llm_usage_purged_users IS 'forge:no-entity';
