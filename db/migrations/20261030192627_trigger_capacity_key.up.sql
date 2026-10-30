-- forge:backward-compatible — one nullable column; the previous release never reads or writes it.
-- The rendered concurrency key of a queued event under `waiting: latest`.
-- NULL: FIFO, or a key that could not be rendered (that event is never
-- superseded). '' is the whole pool's one key.
ALTER TABLE trigger_events ADD COLUMN capacity_key text;
COMMENT ON COLUMN trigger_events.capacity_key IS 'forge:immutable';
-- A new waiting event finds the older waiting events of its key.
CREATE INDEX trigger_events_capacity_key_waiting ON trigger_events
    (user_id, capacity_project_id, capacity_workflow_ref, capacity_key, capacity_order)
    WHERE capacity_key IS NOT NULL AND outcome = 'pending';
