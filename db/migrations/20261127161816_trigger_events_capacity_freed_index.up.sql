-- forge:backward-compatible — additive index; previous releases never read it

-- Migration: trigger_events_capacity_freed_index
--
-- The redriver re-fires a waiting capacity scope's head only when the scope
-- freed a slot since the head was last redriven (store.ListStalePendingTriggerEvents):
-- a run released its slot, or parked on a provider. This index answers "did
-- this scope free a slot after T" with one range probe. Partial, so it holds
-- only the capacity rows that ever freed a slot.
CREATE INDEX CONCURRENTLY IF NOT EXISTS trigger_events_capacity_freed
    ON trigger_events (user_id, capacity_project_id, capacity_workflow_ref, (GREATEST(capacity_released_at, capacity_parked_at)))
    WHERE capacity_order IS NOT NULL AND (capacity_released_at IS NOT NULL OR capacity_parked_at IS NOT NULL);
