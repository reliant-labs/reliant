-- forge:backward-compatible — adds one NOT NULL column with a constant default, so adding it rewrites nothing; the previous release neither reads nor writes it, and its inserts take the default, which reads as "nothing reported".

-- Migration: daemon_attachment_resource_stats
--
-- The machine's and the daemon process's CPU, memory, file descriptors and
-- cgroup pressure, as the daemon's heartbeat last reported them
-- (DaemonHeartbeat.machine_stats, process_stats, cgroup_stats, sampled by
-- forge/pkg/resmetrics). The registry serves it as DaemonInfo.resources and
-- the machine settings page shows it.
--
-- It lives on the attachment lease, beside the memory telemetry and the
-- machine health, because it describes the live session: the row goes when
-- the stream does, and a re-attachment resets it (UpsertDaemonAttachment).
--
--   resource_stats   protojson of reliant.v1.DaemonResourceStats; '' while
--                    the session has reported none

ALTER TABLE daemon_attachment
    ADD COLUMN resource_stats text DEFAULT '' NOT NULL;
