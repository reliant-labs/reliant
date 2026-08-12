-- +goose Up
--
-- Give connector audit rows an explicit lifecycle.
--
-- Until now a row was written only AFTER a tool call returned. That loses
-- exactly the records worth having: if the process is OOM-killed, evicted, or
-- redeployed mid-command, the command may already have run on the daemon while
-- nothing records that it was attempted. An audit log that is complete except
-- around crashes is weakest at the moment someone actually reads it.
--
-- The write is now two-phase. An intent row is inserted before dispatch and
-- updated with the outcome, so a row stuck in 'started' is itself the signal
-- that a call was issued and never accounted for.
--
--   started   — dispatched, outcome unknown (a durable row, not a transient one)
--   completed — ran, successfully or with a daemon-reported error
--   denied    — refused by policy; never reached the daemon
--
-- Existing rows are backfilled from the `denied` flag they already carry.

ALTER TABLE connector_audit_log
    ADD COLUMN status text NOT NULL DEFAULT 'completed';

UPDATE connector_audit_log
SET status = CASE WHEN denied THEN 'denied' ELSE 'completed' END;

ALTER TABLE connector_audit_log
    ADD CONSTRAINT connector_audit_status_valid
    CHECK (status IN ('started', 'completed', 'denied'));

-- Finding calls that were dispatched and never resolved. Partial, because
-- 'started' should be a vanishingly small fraction of the table — if it is
-- not, that is the thing worth alerting on.
CREATE INDEX idx_connector_audit_unresolved ON connector_audit_log(created_at DESC)
    WHERE status = 'started';

-- +goose Down
DROP INDEX IF EXISTS idx_connector_audit_unresolved;
ALTER TABLE connector_audit_log DROP CONSTRAINT IF EXISTS connector_audit_status_valid;
ALTER TABLE connector_audit_log DROP COLUMN IF EXISTS status;
