-- forge:backward-compatible — widens a CHECK: every existing row ('user' or 'chat') satisfies the new one, the previous release only ever writes those two kinds, and the new constraint is added NOT VALID so the swap holds its lock for the catalog change only.

-- Migration: update_stream_counters_org_kind
-- Created: 2026-11-12T18:03:35Z
--
-- The org feed (org_updates) sequences per org from update_stream_counters,
-- under stream_kind 'org'. update_stream_counters_kind_check allowed 'user'
-- and 'chat' only; it is replaced by one that also allows 'org'. Dropping
-- and adding happen in this one transaction, so no write sees the table
-- without a check. The new one is NOT VALID here and validated by
-- 20261112195258_update_stream_counters_org_kind_validate under SHARE UPDATE
-- EXCLUSIVE. lock_timeout bounds the wait behind a long transaction.
--
-- Rollback: none needed. The previous release writes only 'user' and 'chat',
-- which the wider check admits; an 'org' row it never reads.
SET LOCAL lock_timeout = '5s';
ALTER TABLE update_stream_counters
    DROP CONSTRAINT update_stream_counters_kind_check,
    ADD CONSTRAINT update_stream_counters_kind_check
        CHECK (stream_kind = ANY (ARRAY['user'::text, 'chat'::text, 'org'::text])) NOT VALID;
