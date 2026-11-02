-- forge:backward-compatible — one added column with a false default; the previous release names its columns on every daemons insert, so it writes the default and never reads the column (after a rollback the reservation is simply not enforced).

-- A daemon id reserved for daemon-bound credentials. Once true, only a
-- credential bound to this daemon id (or the gateway dialing a managed pod
-- the control plane names) may connect as it; an unbound login token
-- asserting the id is refused, so no other daemon of the same account can
-- take over the machine's identity or supersede its live connection.
--
-- It is set by the server, never by the daemon: when a daemon-bound
-- credential connects as the id, when the gateway dials the pod in, and when
-- a control-plane lifecycle observation (managed machines only) is applied.
-- It is never cleared: a machine that was ever the managed or bound kind
-- stays reserved. daemon_type is NOT the signal — a daemon reports that
-- itself at registration.
-- (IF NOT EXISTS keeps the file idempotent, so its test re-runs the backfill
-- over a migrated database.)
ALTER TABLE daemons
    ADD COLUMN IF NOT EXISTS requires_bound_token boolean DEFAULT false NOT NULL;

-- Backfill from the signals only the control plane writes:
--   * lifecycle_phase is set by ApplyDaemonLifecycle alone, from the control
--     plane's Workspace lifecycle events and registry snapshots, which carry a
--     phase only for managed machines;
--   * an outbound attachment is written only when the gateway dialed a pod
--     the control plane's workspace snapshot named.
-- A managed machine that has neither yet (never observed, never dialed) is
-- reserved by its first bound connect or lifecycle event.
UPDATE daemons
SET requires_bound_token = true
WHERE lifecycle_phase IS NOT NULL
   OR EXISTS (
        SELECT 1 FROM daemon_attachment a
        WHERE a.daemon_id = daemons.id AND a.source = 'outbound'
   );
