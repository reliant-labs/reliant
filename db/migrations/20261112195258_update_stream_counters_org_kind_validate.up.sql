-- forge:backward-compatible — validates the widened kind CHECK on existing rows; every existing row is 'user' or 'chat', which satisfies it, and nothing is written.

-- Migration: update_stream_counters_org_kind_validate
-- Created: 2026-11-12T18:03:55Z
--
-- Validates 20261112195257_update_stream_counters_org_kind's CHECK in its own
-- transaction (SHARE UPDATE EXCLUSIVE: reads and writes continue).
--
-- Post-deploy verification (prod):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify update_stream_counters kind check validated" \
--     "SELECT convalidated, pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'update_stream_counters_kind_check'"
--   expects true and a definition naming 'org'.
SET LOCAL lock_timeout = '5s';
ALTER TABLE update_stream_counters VALIDATE CONSTRAINT update_stream_counters_kind_check;
