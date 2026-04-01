-- +goose Up
-- Fix: the original migration 20260210000000 was edited after being applied
-- to add global_memory_md and project_memory_md columns. The follow-up
-- migration 20260216140500 was a no-op assuming they already existed.
-- Databases that applied the original version are missing these columns.

ALTER TABLE project_configs ADD COLUMN global_memory_md TEXT;
ALTER TABLE project_configs ADD COLUMN project_memory_md TEXT;

-- +goose Down
-- SQLite does not support DROP COLUMN before 3.35.0, so this is best-effort.
-- These columns are safe to leave in place.
SELECT 1;
