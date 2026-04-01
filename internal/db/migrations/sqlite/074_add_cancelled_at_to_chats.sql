-- Migration: Add cancelled_at column to chats table
-- This enables application-level cancellation signals for responsive cancellation
-- When set, activities should check this flag and stop streaming immediately

-- +goose Up

-- Add cancelled_at column to chats table
ALTER TABLE chats ADD COLUMN cancelled_at DATETIME;

-- Create index for efficient cancellation checks
CREATE INDEX idx_chats_cancelled_at ON chats(id, cancelled_at);

-- +goose Down

-- Remove index first
DROP INDEX IF EXISTS idx_chats_cancelled_at;

-- SQLite doesn't support DROP COLUMN, so we need to recreate
-- For down migration, we'll just leave the column (it's nullable)
-- In production, we wouldn't typically run down migrations anyway
