-- forge:backward-compatible — nullable admission metadata leaves existing trigger events unchanged.
-- Capacity lives on the delivery record; no separate queue or permit table.
CREATE SEQUENCE trigger_capacity_order_seq AS bigint;
ALTER TABLE trigger_events
    ADD COLUMN capacity_project_id text,
    ADD COLUMN capacity_workflow_ref text,
    ADD COLUMN capacity_order bigint,
    ADD COLUMN capacity_workflow_id text,
    ADD COLUMN capacity_first_run_id text,
    ADD COLUMN capacity_released boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT trigger_events_capacity_scope CHECK (
        (capacity_project_id IS NULL AND capacity_workflow_ref IS NULL AND capacity_order IS NULL)
        OR (capacity_project_id IS NOT NULL AND capacity_project_id <> ''
            AND capacity_workflow_ref IS NOT NULL AND capacity_workflow_ref <> ''
            AND capacity_order IS NOT NULL AND capacity_order > 0)
    );
CREATE UNIQUE INDEX trigger_events_capacity_order ON trigger_events (capacity_order)
    WHERE capacity_order IS NOT NULL;
CREATE INDEX trigger_events_capacity_waiting ON trigger_events
    (user_id, capacity_project_id, capacity_workflow_ref, capacity_order)
    WHERE capacity_order IS NOT NULL AND outcome = 'pending';
CREATE INDEX trigger_events_capacity_occupied ON trigger_events
    (user_id, capacity_project_id, capacity_workflow_ref)
    WHERE capacity_order IS NOT NULL AND outcome = 'launched' AND NOT capacity_released;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_capacity_binding CHECK (
    (capacity_workflow_id IS NULL AND capacity_first_run_id IS NULL)
    OR (capacity_order IS NOT NULL AND capacity_workflow_id IS NOT NULL AND capacity_workflow_id <> ''
        AND capacity_first_run_id IS NOT NULL AND capacity_first_run_id <> '')
);
