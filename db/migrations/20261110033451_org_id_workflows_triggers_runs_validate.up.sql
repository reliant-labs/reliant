-- forge:backward-compatible — validates the org_id CHECKs on existing rows; every existing row has org_id NULL, which satisfies them, and nothing is written.

-- Migration: org_id_workflows_triggers_runs_validate
-- Created: 2026-11-10T03:34:51Z
--
-- 20261110033439_org_id_workflows_triggers_runs added three CHECKs NOT VALID.
-- Validate them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE
-- UPDATE EXCLUSIVE, which does not block reads or writes while it scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed):
--
--   1. All three CHECKs are installed and validated (true, 3):
--        reliant forge db query prod --database reliant --confirm-env prod \
--          --reason "verify org_id checks validated" \
--          "SELECT bool_and(convalidated), count(*) FROM pg_constraint WHERE conname IN ('workflow_drafts_org_id_not_blank', 'triggers_org_id_not_blank', 'workflows_org_id_not_blank')"
--   2. Runs inherit their chat's org: for runs created after the deploy in a
--      chat that has an org, none lacks one:
--        "SELECT count(*) FROM workflows w JOIN chats c ON c.id = w.chat_id
--          WHERE w.created_at > now() - interval '1 hour' AND c.org_id IS NOT NULL AND w.org_id IS NULL"
--      → 0.
--
-- Rollback: none needed. The columns are nullable, the trigger only fills a
-- NULL from the chat, and the previous release never names any of them.
-- Dropping them is a later forward migration, once no release reads them.
SET LOCAL lock_timeout = '5s';
ALTER TABLE workflow_drafts VALIDATE CONSTRAINT workflow_drafts_org_id_not_blank;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_org_id_not_blank;
ALTER TABLE workflows VALIDATE CONSTRAINT workflows_org_id_not_blank;
