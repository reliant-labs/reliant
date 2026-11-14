-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: llm_org_usage_daily
-- Created: 2026-11-13T01:52:21Z
--
-- llm_org_usage_daily: the counts of ORG-FUNDED model calls only (funding
-- org_key, org_seat or org_wallet), summed per UTC day, org, account, member,
-- provider, model, funding and purpose (org-provider-accounts.md §3.7, §5).
-- It is what an org admin with billing:read reads: an admin read never touches
-- llm_call_usage, so it cannot return a member's personal-account usage, which
-- stays private (UsageService's promise). The per-user llm_call_usage_daily is
-- left as it is: its primary key is the previous release's ON CONFLICT target.
--
-- COUNTS AND IDS ONLY, like llm_call_usage: TestLLMUsageTablesHoldNoContent
-- lists these columns exactly. A member's rows go with their account (account
-- purge). Nothing writes this table yet.
--
-- Post-deploy verification (prod): the table exists and is empty, since
-- nothing writes it in this release (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify llm_org_usage_daily" "SELECT count(*) FROM llm_org_usage_daily"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread. Its one index is built in this file because the table is
-- new and empty, so the build blocks nothing.
CREATE TABLE llm_org_usage_daily (
    day                date NOT NULL,
    org_id             text NOT NULL,
    account_id         text NOT NULL,
    user_id            text NOT NULL,
    provider           text NOT NULL,
    model              text NOT NULL,
    funding            text NOT NULL,
    purpose            text NOT NULL,
    calls              bigint NOT NULL DEFAULT 0,
    error_calls        bigint NOT NULL DEFAULT 0,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    latency_ms_total   bigint NOT NULL DEFAULT 0,
    updated_at         timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, day, account_id, user_id, provider, model, funding, purpose),
    CONSTRAINT llm_org_usage_daily_org_nonempty CHECK (org_id <> ''),
    CONSTRAINT llm_org_usage_daily_funding_check CHECK (funding IN ('org_key', 'org_seat', 'org_wallet')),
    CONSTRAINT llm_org_usage_daily_purpose_check CHECK (purpose IN ('chat', 'compaction', 'title', 'draft_automation')),
    CONSTRAINT llm_org_usage_daily_counts_nonnegative CHECK (
        calls >= 0 AND error_calls >= 0 AND error_calls <= calls
        AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
        AND latency_ms_total >= 0)
);
-- Account purge deletes a member's rows by user_id.
CREATE INDEX llm_org_usage_daily_user ON llm_org_usage_daily (user_id);
