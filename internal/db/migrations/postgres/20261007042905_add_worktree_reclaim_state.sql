-- +goose Up
-- What a daemon last reported about the disk holding its worktrees: the JSON of
-- {path, free_bytes, total_bytes, reported_at}. Like local_models it lives on
-- daemons, not daemon_attachment, so a sleeping laptop's last known disk state
-- survives its disconnect. Empty until the daemon first reports.
--
-- IF NOT EXISTS: migrations are replayed against databases built from
-- schema.sql (see 20261004201053_add_local_models_to_daemons.sql).
ALTER TABLE daemons ADD COLUMN IF NOT EXISTS storage_state TEXT NOT NULL DEFAULT '';
