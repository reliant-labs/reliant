-- forge:backward-compatible — a new table no previous release reads or writes.

-- Migration: insights_drilldowns
-- Created: 2026-11-25T00:42:31Z
--
-- The audit of Insights member drill-downs (dev-docs/design/agent-analytics.md
-- §3.6, PR C1): one row each time an org owner or admin reads one member's
-- numbers (InsightsService.GetMemberInsights), written before the numbers are
-- read, so a drill-down that could not be recorded is never answered. Org
-- admins read the org's rows; a member reads the rows about themselves
-- (ListDrillDownAudit). Append-only.
--
-- org_id is the org whose rollups were read; actor_user_id who looked;
-- subject_user_id whose numbers they were; start_date..end_date the UTC days
-- read, inclusive. Ids and dates only: no numbers that were shown, no content.
-- No foreign keys: org and user ids are control-plane's, and the record of who
-- looked outlives both accounts, like share_events.
CREATE TABLE insights_drilldowns (
    id text PRIMARY KEY,
    org_id text NOT NULL CHECK (org_id <> ''),
    actor_user_id text NOT NULL CHECK (actor_user_id <> ''),
    subject_user_id text NOT NULL CHECK (subject_user_id <> ''),
    start_date date NOT NULL,
    end_date date NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT NOW(),
    CONSTRAINT insights_drilldowns_range_check CHECK (end_date >= start_date)
);
COMMENT ON TABLE insights_drilldowns IS
    'One row per Insights member drill-down (who looked, at whom, which days), written before the numbers are read. Append-only.';

-- An admin reads the org's audit newest first; a member reads the rows about
-- themselves newest first. The table is new and empty, so its indexes are
-- built here: the build blocks nothing.
CREATE INDEX insights_drilldowns_org_created ON insights_drilldowns (org_id, created_at DESC, id DESC);
CREATE INDEX insights_drilldowns_subject_created ON insights_drilldowns (org_id, subject_user_id, created_at DESC, id DESC);
