-- +goose Up
-- Add active_daemon_id to chats for session-level daemon targeting.
-- When set, this daemon is used as the default for tool execution in this chat.
-- IF NOT EXISTS because version 20260321000000 was claimed by two files. It
-- belonged to add_metadata_to_yields until #82 renumbered that to
-- 20260321000001, so a database migrated between #76 and #77 recorded this
-- version having run the YIELDS SQL. See
-- 20260928193015_restore_temporal_payload_blobs.sql for the same defect in
-- September, and TestMigrationVersionsAreUnique for the guard against a third.
ALTER TABLE chats ADD COLUMN IF NOT EXISTS active_daemon_id TEXT;

