-- forge:backward-compatible — two new nullable columns with no default; the previous release neither reads nor writes them, and its inserts and updates name their columns.

-- Migration: run_outcome
-- Created: 2026-11-17T22:56:40Z
--
-- A run's outcome, beside its lifecycle status (reliant#2753). A run whose own
-- failure path ran — a node failed and the run went on past it — ends
-- COMPLETED, and read as a success to the run list and to its trigger's
-- health. run_outcome says what happened:
--   succeeded        completed, and no node's last attempt failed
--   failure_handled  completed, but a node's last attempt failed and the run
--                    went on past it (a failure path, a tolerated failure)
--   failed           failed, or completed into a declared outcome: failure
-- NULL for a run that has not ended, was cancelled, or ended before this
-- column existed. It is written once, with the terminal status, by the
-- WorkflowStatus activity (handlers/run_outcome.go).

ALTER TABLE workflows ADD COLUMN run_outcome text;
COMMENT ON COLUMN workflows.run_outcome IS 'How the run ended for its health: succeeded, failure_handled or failed. NULL while it runs, when it was cancelled, and on a run that ended before the column existed.';

-- The same, on the firing that launched the run: trigger health reads a
-- firing's own run (trigger_events.run_status), never the chat's later turns.
ALTER TABLE trigger_events ADD COLUMN run_outcome text;
COMMENT ON COLUMN trigger_events.run_outcome IS 'The launched run''s workflows.run_outcome, recorded with run_status.';
