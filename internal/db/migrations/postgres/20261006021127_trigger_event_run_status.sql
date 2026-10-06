-- +goose Up

-- How the run a trigger FIRED ended: 'completed', 'failed' or 'cancelled',
-- recorded on its launch event when that run reaches a terminal status. NULL
-- while it is still going, and for every event that launched no unattended run.
--
-- A launched chat outlives its run. A person can reply in it, and each reply is
-- a new run of the same root workflow, so the chat's live state describes the
-- latest turn, not the one the trigger fired. Trigger health, the failure
-- streak and the overlap check are the trigger's policies, about the runs it
-- fired; they read this column, and fall back to the live state only while it
-- is unset. Without it a person's failed follow-up counted as the automation
-- failing, and their successful one ended a streak the automation was still in.
ALTER TABLE trigger_events ADD COLUMN IF NOT EXISTS run_status text;

ALTER TABLE trigger_events DROP CONSTRAINT IF EXISTS trigger_events_run_status_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_run_status_check
    CHECK (run_status IS NULL OR run_status IN ('completed', 'failed', 'cancelled'));
