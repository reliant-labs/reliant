-- +goose Up
-- Migration: Add display_style column to messages
-- Purpose: Separate UI styling concerns from message role.
--          This allows info/warning messages to be saved as role="user" (so LLMs see them)
--          while the UI can still render them with special styling.
--
-- display_style values: NULL (default), "info", "warning", "success"
-- NULL means normal message styling for the given role.
--
-- NOTE: We do NOT recreate triggers here. Migration 062 moved to dual-writes,
-- and the existing code handles chat_updates via direct inserts in the repository layer.

-- Add display_style column (skip if already exists from partial migration)
-- SQLite doesn't have ADD COLUMN IF NOT EXISTS, so we just create the index
ALTER TABLE messages ADD COLUMN display_style TEXT;

-- Create index for filtering by display_style
CREATE INDEX IF NOT EXISTS idx_messages_display_style ON messages(display_style)
WHERE display_style IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_messages_display_style;

-- SQLite doesn't support DROP COLUMN directly, but for pre-launch we can just
-- recreate the table if needed. For now, we leave the column (it's harmless).
