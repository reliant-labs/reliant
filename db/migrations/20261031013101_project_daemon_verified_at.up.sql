-- forge:backward-compatible — one new nullable column; the previous release never reads it, and its writes (which leave it untouched) only make a checkout look unverified, which delays pool placement onto that machine until its next reconnect rather than placing anything wrongly.

-- Migration: project_daemon_verified_at
--
-- When this project's checkout was last SEEN on this machine's disk: written
-- when a clone lands and by the connect-time reconcile that finds the path on
-- the daemon. Machine-pool placement puts a run on a connected member only
-- when its checkout was seen since the member's current connection
-- (verified_at >= daemon_attachment.attached_at). A machine whose disk is the
-- pod's own (control-plane daemons.disk_mode = ephemeral) wakes with an empty
-- disk while install_state still says installed; this is what keeps a run off
-- it until the checkout is back (dev-docs/design/machine-pools.md, Stage 7).
ALTER TABLE project_daemons ADD COLUMN verified_at TIMESTAMPTZ;

-- Rows installed today are as verified as they ever were: a machine connected
-- now stays placeable, and every later connect re-verifies.
UPDATE project_daemons SET verified_at = NOW() WHERE install_state = 'installed';
