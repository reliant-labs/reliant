-- forge:backward-compatible — validates the widened share_events action CHECK on existing rows; every one satisfied the narrower CHECK it replaced, and nothing is written.

-- Migration: share_events_action_moved_validate
-- Created: 2026-11-12T23:49:41Z
--
-- 20261112234909_share_events_action_moved added share_events_action_check NOT
-- VALID. Validate it here, in its own transaction: VALIDATE CONSTRAINT holds
-- SHARE UPDATE EXCLUSIVE, which does not block reads or writes while it scans.
--
-- Post-deploy verification (prod, once the release carrying this file is
-- deployed), expecting true:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify share_events action check validated" \
--     "SELECT convalidated FROM pg_constraint WHERE conname = 'share_events_action_check'"
--
-- Rollback: none needed. The previous release writes only values the CHECK
-- admits and never reads 'moved'.
SET LOCAL lock_timeout = '5s';
ALTER TABLE share_events VALIDATE CONSTRAINT share_events_action_check;
