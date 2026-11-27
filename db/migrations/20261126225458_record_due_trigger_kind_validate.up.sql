-- forge:backward-compatible — validates the widened kind CHECKs on existing rows; every existing row holds a kind the old checks allowed, which the new ones allow too, and nothing is written.

-- Migration: record_due_trigger_kind_validate
-- Created: 2026-11-26T22:54:58Z
--
-- Validates 20261126225455_record_due_trigger_kind's CHECKs in their own
-- transaction (SHARE UPDATE EXCLUSIVE: reads and writes continue).
--
-- Post-deploy verification (prod):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify record_due kind checks validated" \
--     "SELECT conname, convalidated FROM pg_constraint WHERE conname IN ('triggers_kind_check', 'trigger_events_kind_check')"
--   expects both validated (true).
SET LOCAL lock_timeout = '5s';
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_kind_check;
ALTER TABLE trigger_events VALIDATE CONSTRAINT trigger_events_kind_check;
