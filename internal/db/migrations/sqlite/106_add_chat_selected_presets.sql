-- +goose Up
-- Add selected_presets column to chats table
-- Stores JSON map of target -> preset name (e.g., {"": "fast", "Agent A": "researcher"})
ALTER TABLE chats ADD COLUMN selected_presets TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN directly in older versions,
-- but modern SQLite (3.35+) does support it
ALTER TABLE chats DROP COLUMN selected_presets;
