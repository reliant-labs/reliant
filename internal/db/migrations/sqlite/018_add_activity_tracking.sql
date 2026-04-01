-- +goose Up
-- Migration: Add activity tracking to active_threads
-- Purpose: Track current activity name and start time for real-time status updates

ALTER TABLE active_threads ADD COLUMN current_activity TEXT;
ALTER TABLE active_threads ADD COLUMN current_activity_started_at DATETIME;

CREATE INDEX idx_active_threads_activity ON active_threads(chat_id, current_activity) 
WHERE current_activity IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_active_threads_activity;
ALTER TABLE active_threads DROP COLUMN current_activity_started_at;
ALTER TABLE active_threads DROP COLUMN current_activity;
