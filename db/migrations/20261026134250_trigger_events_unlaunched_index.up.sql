-- forge:backward-compatible — additive index; previous releases never read it

-- Migration: trigger_events_unlaunched_index
--
-- The run log's second half (store.ListUnlaunchedTriggerEvents): a caller's
-- trigger firings that started no run, newest first. Partial, so it holds only
-- the skipped and couldn't-start rows — a small fraction of trigger_events —
-- and the keyset (occurred_at, id) walks it in order.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_trigger_events_unlaunched
    ON trigger_events (user_id, occurred_at DESC, id DESC)
    WHERE outcome IN ('skipped', 'failed') AND chat_id IS NULL AND trigger_id IS NOT NULL;
