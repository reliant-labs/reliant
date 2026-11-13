-- forge:backward-compatible — adds four columns, each NOT NULL with a constant default or nullable, so adding them rewrites nothing; the previous release neither reads nor writes them, and its inserts take the defaults, which read as "health not reported".

-- Migration: daemon_attachment_health
--
-- A machine's health as its daemon last reported it in a heartbeat
-- (reliantv1.MachineHealth; reliant#1919). On 2026-10-30 a machine's process
-- table filled: every fork failed for 24 minutes while the daemon stayed
-- attached and answered, so the registry, the UI and wait_for_machine all
-- called it connected. The daemon's fork watchdog (internal/forkwatch) now
-- reports that as degraded, and the gateway keeps the report here.
--
-- It lives on the attachment lease, beside the memory telemetry, because it
-- describes the live session: the row goes when the stream does, and a
-- re-attachment resets it (UpsertDaemonAttachment).
--
--   health_state   '' (not reported), 'ok' or 'degraded'
--   health_code    machine-readable cause while degraded ("process_table_full")
--   health_reason  the cause in words, for a person or an agent
--   health_since   when the machine entered its current state, by the
--                  gateway's clock; NULL while nothing was reported

ALTER TABLE daemon_attachment
    ADD COLUMN health_state text DEFAULT '' NOT NULL,
    ADD COLUMN health_code text DEFAULT '' NOT NULL,
    ADD COLUMN health_reason text DEFAULT '' NOT NULL,
    ADD COLUMN health_since timestamp with time zone;
