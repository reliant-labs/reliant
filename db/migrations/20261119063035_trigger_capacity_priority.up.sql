-- forge:backward-compatible — two nullable columns; the previous release never reads or writes them.
-- Admission metadata of a queued event, fixed when it enrolls, like capacity_key.
-- capacity_running_key: the rendered concurrency key under max_running_per_key,
-- the group whose running runs are capped. NULL: no per-key cap, or a key that
-- could not be rendered (the event counts only against the workflow-wide cap).
-- It is separate from capacity_key, which stays the waiting: latest supersede
-- group: a FIFO workflow may cap runs per key without superseding anything.
-- capacity_priority: the evaluated concurrency.priority. NULL is 0.
ALTER TABLE trigger_events
    ADD COLUMN capacity_running_key text,
    ADD COLUMN capacity_priority bigint;
COMMENT ON COLUMN trigger_events.capacity_running_key IS 'forge:immutable';
COMMENT ON COLUMN trigger_events.capacity_priority IS 'forge:immutable';
