-- forge:backward-compatible — validates the share_events constraints 20261125052227 added NOT VALID; every existing row satisfies them, and nothing is written.

-- Migration: share_events_member_actions_validate
-- Created: 2026-11-25T05:22:26Z
--
-- 20261125052227_share_events_member_actions added share_events_action_check,
-- share_events_target_check and share_events_role_check NOT VALID. Validate
-- them here, in their own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE
-- EXCLUSIVE, which does not block reads or writes while it scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed), expecting three rows, all true:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify share_events member constraints validated" \
--     "SELECT conname, convalidated FROM pg_constraint WHERE conname IN ('share_events_action_check', 'share_events_target_check', 'share_events_role_check')"
--
-- Rollback: none needed. The previous release writes only rows the
-- constraints admit and never reads the new columns or actions.
SET LOCAL lock_timeout = '5s';
ALTER TABLE share_events VALIDATE CONSTRAINT share_events_action_check;
ALTER TABLE share_events VALIDATE CONSTRAINT share_events_target_check;
ALTER TABLE share_events VALIDATE CONSTRAINT share_events_role_check;
