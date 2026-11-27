-- forge:backward-compatible — widens a CHECK on a table nothing older reads or writes, and adds one nothing older can violate

-- Migration: queue_isolation_runc
--
-- The MVP gate runner (pipeline build plan task M9) has a second backend
-- besides sysbox: plain runc, labelled not isolated. It exists only so the
-- merge queue can run its shadow day before dedicated sysbox gate machines
-- exist (owner, 2026-11-26). A batch gated on it may pass, and its
-- would-land set is recorded, but it never lands: that is enforced here as
-- well as in the queue's Go code (GateResult.Landable).
--
-- Both constraints are added valid. The table holds no rows that use 'runc'
-- (the value did not exist), and the widening never invalidates a row.

ALTER TABLE queue_batches DROP CONSTRAINT queue_batches_isolation_backend;
ALTER TABLE queue_batches ADD CONSTRAINT queue_batches_isolation_backend
    CHECK (isolation_backend IN ('vm', 'sysbox', 'checks', 'runc'));
COMMENT ON CONSTRAINT queue_batches_isolation_backend ON queue_batches IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllQueueIsolationBackends';

-- A batch gated outside an isolation boundary never moves main.
ALTER TABLE queue_batches ADD CONSTRAINT queue_batches_runc_never_lands
    CHECK (NOT (isolation_backend = 'runc' AND state = 'landed'));
