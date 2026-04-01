-- Add user_id to chats and projects tables for multi-user support
-- No backwards compatibility - all existing records get a default user_id

-- +goose Up
ALTER TABLE chats ADD COLUMN user_id TEXT NOT NULL DEFAULT '4630f1da-5058-4707-804b-aa30362f3e40';
ALTER TABLE projects ADD COLUMN user_id TEXT NOT NULL DEFAULT '4630f1da-5058-4707-804b-aa30362f3e40';

-- Add indexes for user_id lookups
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_user_updated ON chats(user_id, updated_at DESC);
CREATE INDEX idx_projects_user_id ON projects(user_id);

-- Add unique constraint on projects to ensure one path per user
-- First drop the existing unique constraint on path
DROP INDEX IF EXISTS idx_projects_path;
CREATE UNIQUE INDEX idx_projects_user_path ON projects(user_id, path);

-- +goose Down
-- Remove indexes
DROP INDEX IF EXISTS idx_chats_user_id;
DROP INDEX IF EXISTS idx_chats_user_updated;
DROP INDEX IF EXISTS idx_projects_user_id;
DROP INDEX IF EXISTS idx_projects_user_path;

-- Recreate original path index
CREATE INDEX idx_projects_path ON projects(path);

-- SQLite doesn't support DROP COLUMN directly, need to recreate tables
-- For simplicity, we'll just remove the columns in a new migration if needed
-- This is a destructive operation and should be done carefully in production
