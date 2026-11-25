-- forge:backward-compatible — adds a boolean with a constant default and a nullable column the previous release never names (its inserts take false, "not folded yet", which this release's backfill folds, and NULL, "payer org unknown", which no org's spend counts), three functions, two aggregates and four new tables only this release reads and writes.

-- Migration: insights_rollups
-- Created: 2026-11-25T00:27:42Z
--
-- Agents › Insights reads its own rollups (dev-docs/design/agent-analytics.md
-- §3.1, PR A4): every raw source decays (llm_call_usage and tool_call_usage
-- rows are deleted at 90 days, spans at seven), so a six-month chart can only
-- come from totals written when the call happened. Two tables per source,
-- deliberately separate:
--
--   - the TEAM tables (insights_model_calls_daily, insights_tool_calls_daily)
--     are an org's totals per day, project, workflow, node, preset and the
--     call's labels. They have NO user_id column: the team page reads only
--     these, so a bug in its query cannot print a person (§3.6). Only calls
--     made in an org (org_id <> '') land here.
--   - the MEMBER tables (insights_model_calls_member_daily,
--     insights_tool_calls_member_daily) add user_id. They are what an org
--     admin's audited drill-down and a member's own "My analytics" read.
--
-- Each call is folded into both in the transaction that writes its ledger
-- row (store.RecordLLMCallUsage, store.RecordToolCallUsage), like
-- llm_call_usage_daily. A row the previous release wrote, and every row that
-- existed before this migration, has insights_folded = false; the retention
-- job folds those in batches (store.FoldLLMCallUsageIntoInsights,
-- FoldToolCallUsageIntoInsights) before it prunes, and pruning deletes only
-- folded rows. That is the backfill of the 90 days that exist, and it is
-- idempotent: a row is folded in the transaction that sets its flag, so it is
-- counted exactly once whichever path folds it, and a second run finds
-- nothing.
--
-- Counts are per attempt, as the ledgers count them: each Temporal attempt is
-- a real request. retried_calls is the attempts beyond the first, so
-- calls - retried_calls is the logical requests. A repeated write of one
-- attempt is ignored by the ledger and so never reaches a rollup.
--
-- Latency histograms: *_hist is 48 counts. Bucket 0 holds 0 ms; bucket k in
-- 1..46 holds 2^((k-1)/2) <= ms < 2^(k/2) (a half power of two, so a
-- percentile read from it is within ~19% of the true value); bucket 47 holds
-- everything from 2^23 ms (about 2.3 hours). insights_latency_bucket is the
-- one definition; store.InsightsLatencyBucket mirrors it and a test holds the
-- two equal.
--
-- Cost: the team model table counts a call's cost only when the org the row
-- belongs to is the org that paid for it: funding is org_key, org_seat or
-- org_wallet AND the ledger's funding_org_id (added below) is the row's
-- org_id. Who the work was for (org_id) and who paid (funding_org_id) are
-- separate: the org-less managed key bills the person's default org, which is
-- not necessarily the chat's, so it records funding org_wallet with no known
-- payer. A NULL payer (that key, a personal account, or any row written
-- before this column) is unknown and is never counted as an org's spend; the
-- call is still counted, without cost. A member's own subscription or key is
-- never in an org's spend (§3.6, D6). paid_by_org, a key column of both model
-- tables, says which rows the row's org paid for (computed when the call is
-- folded); a team row's cost is zero unless it is set, which the CHECKs below
-- enforce, and a reader of a member row filters on it for the org's spend,
-- without the table naming any other org. The
-- member table keeps the member's cost whatever paid, so their own view can
-- show it. Reported and estimated cost are separate columns and are never
-- summed together (§4); unpriced_calls are calls with no per-call price.
--
-- COUNTS, IDS AND LABELS ONLY, like the ledgers: TestLLMUsageTablesHoldNoContent
-- lists these columns exactly. There is no foreign key: account purge deletes
-- a member's rows (internal/accountpurge), project delete deletes the member
-- rows of the project (internal/store/project_delete.go), and the orphan
-- sweep removes member rows of a project that is gone. The team tables keep
-- an org's totals after a project or a member is deleted, but not the
-- deleted project's id: project delete (and the orphan sweep, for a project
-- an account purge deleted) merges the project's team rows into the
-- no-project bucket (project_id = '') of the same org, day and other keys,
-- adding their counts, and a call folded later whose project is gone lands
-- there too. project_id is part of the primary key, so "no project" is ''
-- rather than NULL, as every NULL run-scope column of the ledger is here.
--
-- Post-deploy verification (prod): the backfill drains within a few retention
-- passes (hourly); both counts reach 0:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify insights backfill" \
--     "SELECT (SELECT count(*) FROM llm_call_usage WHERE NOT insights_folded),
--             (SELECT count(*) FROM tool_call_usage WHERE NOT insights_folded)"
--
-- Rollback: none needed. The previous release never names the new columns,
-- functions or tables; its inserts leave insights_folded false, and rolling
-- forward again folds them. Its own pruning ignores the flag, so a row it
-- prunes before it is folded is lost to the rollup (only rows already past
-- the 90-day retention).
SET LOCAL lock_timeout = '5s';

-- A constant default is a catalog change, not a rewrite.
ALTER TABLE llm_call_usage ADD COLUMN insights_folded boolean NOT NULL DEFAULT false;
ALTER TABLE tool_call_usage ADD COLUMN insights_folded boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN llm_call_usage.insights_folded IS 'Whether the row is counted in the insights rollups. False for a row written before them or by a release that does not write them; the retention job folds it.';
-- The org that paid for the call (store.LLMCallUsageRecord.FundingOrgID):
-- the org that owns the serving account or that it is attributed to. NULL
-- when no org is known to have paid. No default, so this is a catalog change.
ALTER TABLE llm_call_usage ADD COLUMN funding_org_id text;
COMMENT ON COLUMN llm_call_usage.funding_org_id IS 'The org that paid for the call (owns the serving account, or it is attributed to). NULL when no org is known to have paid: a person''s own account, the org-less managed key, or a row written before the column. Never the same thing as org_id, the org the work was for.';
COMMENT ON COLUMN tool_call_usage.insights_folded IS 'Whether the row is counted in the insights rollups. False for a row written before them or by a release that does not write them; the retention job folds it.';

-- The histogram bucket of a duration in ms (see above). k >= 1 is
-- bit_length(ms * ms): ms * ms < 2^k exactly when ms < 2^(k/2). Exact integer
-- arithmetic, so a bucket boundary never depends on a float. ms is capped at
-- 2^24 first, which is already in the last bucket, so ms * ms cannot overflow.
CREATE FUNCTION insights_latency_bucket(ms bigint) RETURNS integer
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT CASE WHEN ms < 1 THEN 0
        ELSE least(47, 65 - position('1' IN ((least(ms, 16777216) * least(ms, 16777216))::bit(64))::text)) END
$$;

-- insights_hist(ms) is the 48-bucket histogram of a column; a NULL counts
-- nowhere. insights_hist_add adds two histograms, for ON CONFLICT.
CREATE FUNCTION insights_hist_step(state bigint[], ms bigint) RETURNS bigint[]
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    i integer;
BEGIN
    IF ms IS NOT NULL THEN
        i := insights_latency_bucket(ms) + 1;
        state[i] := state[i] + 1;
    END IF;
    RETURN state;
END
$$;

CREATE FUNCTION insights_hist_add(a bigint[], b bigint[]) RETURNS bigint[]
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT array_agg(x + y ORDER BY i) FROM unnest(a, b) WITH ORDINALITY AS u(x, y, i)
$$;

-- insights_hist_sum(hist) adds the histograms of a group, to merge rows.
CREATE AGGREGATE insights_hist_sum(bigint[]) (
    SFUNC = insights_hist_add,
    STYPE = bigint[],
    COMBINEFUNC = insights_hist_add,
    PARALLEL = SAFE,
    INITCOND = '{0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0}'
);

CREATE AGGREGATE insights_hist(bigint) (
    SFUNC = insights_hist_step,
    STYPE = bigint[],
    COMBINEFUNC = insights_hist_add,
    PARALLEL = SAFE,
    INITCOND = '{0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0}'
);

-- Model calls, per org. A NULL run-scope column of the ledger is '' here.
CREATE TABLE insights_model_calls_daily (
    org_id                   text NOT NULL,
    day                      date NOT NULL,
    project_id               text NOT NULL DEFAULT '',
    workflow_name            text NOT NULL DEFAULT '',
    node_id                  text NOT NULL DEFAULT '',
    preset                   text NOT NULL DEFAULT '',
    provider                 text NOT NULL,
    model                    text NOT NULL,
    auth_mode                text NOT NULL,
    funding                  text NOT NULL DEFAULT '',
    paid_by_org              boolean NOT NULL DEFAULT false,
    purpose                  text NOT NULL,
    outcome                  text NOT NULL,
    error_class              text NOT NULL DEFAULT '',
    calls                    bigint NOT NULL DEFAULT 0,
    retried_calls            bigint NOT NULL DEFAULT 0,
    input_tokens             bigint NOT NULL DEFAULT 0,
    output_tokens            bigint NOT NULL DEFAULT 0,
    cache_read_tokens        bigint NOT NULL DEFAULT 0,
    cache_write_tokens       bigint NOT NULL DEFAULT 0,
    latency_ms_total         bigint NOT NULL DEFAULT 0,
    latency_ms_hist          bigint[] NOT NULL,
    ttft_calls               bigint NOT NULL DEFAULT 0,
    ttft_ms_total            bigint NOT NULL DEFAULT 0,
    ttft_ms_hist             bigint[] NOT NULL,
    reported_calls           bigint NOT NULL DEFAULT 0,
    reported_cost_usd_nanos  bigint NOT NULL DEFAULT 0,
    estimated_calls          bigint NOT NULL DEFAULT 0,
    estimated_cost_usd_nanos bigint NOT NULL DEFAULT 0,
    unpriced_calls           bigint NOT NULL DEFAULT 0,
    updated_at               timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, day, project_id, workflow_name, node_id, preset, provider, model, auth_mode, funding, paid_by_org, purpose, outcome, error_class),
    CONSTRAINT insights_model_calls_daily_org_check CHECK (org_id <> ''),
    CONSTRAINT insights_model_calls_daily_hist_check CHECK (
        cardinality(latency_ms_hist) = 48 AND cardinality(ttft_ms_hist) = 48),
    CONSTRAINT insights_model_calls_daily_counts_nonnegative CHECK (
        calls >= 0 AND retried_calls >= 0 AND retried_calls <= calls
        AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
        AND latency_ms_total >= 0 AND ttft_calls >= 0 AND ttft_calls <= calls AND ttft_ms_total >= 0
        AND reported_calls >= 0 AND reported_cost_usd_nanos >= 0 AND estimated_calls >= 0
        AND estimated_cost_usd_nanos >= 0 AND unpriced_calls >= 0
        AND reported_calls + estimated_calls + unpriced_calls <= calls),
    -- An org's spend is what it paid for itself: never a member's own
    -- account, another org's payment, or a payer that is not known.
    CONSTRAINT insights_model_calls_daily_paid_by_org_is_org_funded CHECK (
        NOT paid_by_org OR funding IN ('org_key', 'org_seat', 'org_wallet')),
    CONSTRAINT insights_model_calls_daily_cost_is_paid_by_org CHECK (
        paid_by_org
        OR (reported_calls = 0 AND reported_cost_usd_nanos = 0 AND estimated_calls = 0 AND estimated_cost_usd_nanos = 0))
);
COMMENT ON TABLE insights_model_calls_daily IS 'forge:no-entity';

-- Model calls, per member: the team table's key and counts, plus user_id.
-- org_id is '' for a call made outside an org.
CREATE TABLE insights_model_calls_member_daily (
    user_id                  text NOT NULL,
    day                      date NOT NULL,
    org_id                   text NOT NULL DEFAULT '',
    project_id               text NOT NULL DEFAULT '',
    workflow_name            text NOT NULL DEFAULT '',
    node_id                  text NOT NULL DEFAULT '',
    preset                   text NOT NULL DEFAULT '',
    provider                 text NOT NULL,
    model                    text NOT NULL,
    auth_mode                text NOT NULL,
    funding                  text NOT NULL DEFAULT '',
    paid_by_org              boolean NOT NULL DEFAULT false,
    purpose                  text NOT NULL,
    outcome                  text NOT NULL,
    error_class              text NOT NULL DEFAULT '',
    calls                    bigint NOT NULL DEFAULT 0,
    retried_calls            bigint NOT NULL DEFAULT 0,
    input_tokens             bigint NOT NULL DEFAULT 0,
    output_tokens            bigint NOT NULL DEFAULT 0,
    cache_read_tokens        bigint NOT NULL DEFAULT 0,
    cache_write_tokens       bigint NOT NULL DEFAULT 0,
    latency_ms_total         bigint NOT NULL DEFAULT 0,
    latency_ms_hist          bigint[] NOT NULL,
    ttft_calls               bigint NOT NULL DEFAULT 0,
    ttft_ms_total            bigint NOT NULL DEFAULT 0,
    ttft_ms_hist             bigint[] NOT NULL,
    reported_calls           bigint NOT NULL DEFAULT 0,
    reported_cost_usd_nanos  bigint NOT NULL DEFAULT 0,
    estimated_calls          bigint NOT NULL DEFAULT 0,
    estimated_cost_usd_nanos bigint NOT NULL DEFAULT 0,
    unpriced_calls           bigint NOT NULL DEFAULT 0,
    updated_at               timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day, org_id, project_id, workflow_name, node_id, preset, provider, model, auth_mode, funding, paid_by_org, purpose, outcome, error_class),
    CONSTRAINT insights_model_calls_member_daily_paid_by_org_is_org_funded CHECK (
        NOT paid_by_org OR funding IN ('org_key', 'org_seat', 'org_wallet')),
    CONSTRAINT insights_model_calls_member_daily_hist_check CHECK (
        cardinality(latency_ms_hist) = 48 AND cardinality(ttft_ms_hist) = 48),
    CONSTRAINT insights_model_calls_member_daily_counts_nonnegative CHECK (
        calls >= 0 AND retried_calls >= 0 AND retried_calls <= calls
        AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
        AND latency_ms_total >= 0 AND ttft_calls >= 0 AND ttft_calls <= calls AND ttft_ms_total >= 0
        AND reported_calls >= 0 AND reported_cost_usd_nanos >= 0 AND estimated_calls >= 0
        AND estimated_cost_usd_nanos >= 0 AND unpriced_calls >= 0
        AND reported_calls + estimated_calls + unpriced_calls <= calls)
);
COMMENT ON TABLE insights_model_calls_member_daily IS 'forge:no-entity';

-- Tool calls, per org.
CREATE TABLE insights_tool_calls_daily (
    org_id            text NOT NULL,
    day               date NOT NULL,
    project_id        text NOT NULL DEFAULT '',
    workflow_name     text NOT NULL DEFAULT '',
    node_id           text NOT NULL DEFAULT '',
    preset            text NOT NULL DEFAULT '',
    tool_name         text NOT NULL DEFAULT '',
    tool_kind         text NOT NULL,
    location          text NOT NULL,
    outcome           text NOT NULL,
    error_class       text NOT NULL DEFAULT '',
    calls             bigint NOT NULL DEFAULT 0,
    retried_calls     bigint NOT NULL DEFAULT 0,
    duration_ms_total bigint NOT NULL DEFAULT 0,
    duration_ms_hist  bigint[] NOT NULL,
    updated_at        timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, day, project_id, workflow_name, node_id, preset, tool_name, tool_kind, location, outcome, error_class),
    CONSTRAINT insights_tool_calls_daily_org_check CHECK (org_id <> ''),
    CONSTRAINT insights_tool_calls_daily_hist_check CHECK (cardinality(duration_ms_hist) = 48),
    CONSTRAINT insights_tool_calls_daily_counts_nonnegative CHECK (
        calls >= 0 AND retried_calls >= 0 AND retried_calls <= calls AND duration_ms_total >= 0)
);
COMMENT ON TABLE insights_tool_calls_daily IS 'forge:no-entity';

-- Tool calls, per member.
CREATE TABLE insights_tool_calls_member_daily (
    user_id           text NOT NULL,
    day               date NOT NULL,
    org_id            text NOT NULL DEFAULT '',
    project_id        text NOT NULL DEFAULT '',
    workflow_name     text NOT NULL DEFAULT '',
    node_id           text NOT NULL DEFAULT '',
    preset            text NOT NULL DEFAULT '',
    tool_name         text NOT NULL DEFAULT '',
    tool_kind         text NOT NULL,
    location          text NOT NULL,
    outcome           text NOT NULL,
    error_class       text NOT NULL DEFAULT '',
    calls             bigint NOT NULL DEFAULT 0,
    retried_calls     bigint NOT NULL DEFAULT 0,
    duration_ms_total bigint NOT NULL DEFAULT 0,
    duration_ms_hist  bigint[] NOT NULL,
    updated_at        timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day, org_id, project_id, workflow_name, node_id, preset, tool_name, tool_kind, location, outcome, error_class),
    CONSTRAINT insights_tool_calls_member_daily_hist_check CHECK (cardinality(duration_ms_hist) = 48),
    CONSTRAINT insights_tool_calls_member_daily_counts_nonnegative CHECK (
        calls >= 0 AND retried_calls >= 0 AND retried_calls <= calls AND duration_ms_total >= 0)
);
COMMENT ON TABLE insights_tool_calls_member_daily IS 'forge:no-entity';

-- The tables are new and empty, so their indexes are built here: the build
-- blocks nothing. An admin's drill-down reads an org's members by day; project
-- delete and the orphan sweep delete or merge by project.
CREATE INDEX insights_model_calls_daily_project_idx ON insights_model_calls_daily (project_id) WHERE project_id <> '';
CREATE INDEX insights_tool_calls_daily_project_idx ON insights_tool_calls_daily (project_id) WHERE project_id <> '';
CREATE INDEX insights_model_calls_member_daily_org_day_idx ON insights_model_calls_member_daily (org_id, day) WHERE org_id <> '';
CREATE INDEX insights_model_calls_member_daily_project_idx ON insights_model_calls_member_daily (project_id) WHERE project_id <> '';
CREATE INDEX insights_tool_calls_member_daily_org_day_idx ON insights_tool_calls_member_daily (org_id, day) WHERE org_id <> '';
CREATE INDEX insights_tool_calls_member_daily_project_idx ON insights_tool_calls_member_daily (project_id) WHERE project_id <> '';
