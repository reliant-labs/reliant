-- forge:breaks-rollback — the planned-restart drain (20261027131352) is removed; the previous release (v1.8.5 through v1.8.7) names daemons.restart_status in its explicit-column daemon reads and writes, so it fails against this schema until it is rolled forward again.

-- Migration: drop_daemon_restart_status
-- Created: 2026-10-28T19:25:05Z
--
-- The planned daemon restart drain had no caller and left a drained daemon
-- refusing work until its process was replaced by hand. Its code is gone;
-- this drops the one column it added. Nothing reads its contents: it held
-- only a pending drain's status, and nothing ever requested one.
-- forge:allow-destructive

ALTER TABLE daemons DROP COLUMN IF EXISTS restart_status;
