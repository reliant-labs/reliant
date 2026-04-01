-- Migration: Drop paused_at column from workflows table
-- Purpose: Pause/resume is tracked via workflow status only, not a separate timestamp.

-- +goose Up
ALTER TABLE workflows DROP COLUMN paused_at;

-- +goose Down
ALTER TABLE workflows ADD COLUMN paused_at DATETIME;
