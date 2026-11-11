-- forge:backward-compatible — nullable columns: an older server never sets them, and NULL means "not parked" and "no stamped cap", which is exactly what it assumes.
-- A run waiting on a provider holds no workflow-capacity slot (parked); it
-- takes one back, ahead of the queued firings, before it runs another step.
ALTER TABLE trigger_events
    -- The run is parked on a provider and holds no slot.
    ADD COLUMN capacity_parked_at timestamptz,
    -- A parked run asked for its slot back and the scope was full: it counts
    -- against queued admission (it is ahead of them), not against resumers.
    ADD COLUMN capacity_resume_requested_at timestamptz,
    -- The cap the event was admitted under; a parked run resumes against it.
    ADD COLUMN capacity_limit integer;
COMMENT ON COLUMN trigger_events.capacity_parked_at IS 'forge:immutable';
COMMENT ON COLUMN trigger_events.capacity_resume_requested_at IS 'forge:immutable';
COMMENT ON COLUMN trigger_events.capacity_limit IS 'forge:immutable';
