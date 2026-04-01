-- Migration: Add backgrounded_at column to tool_execution_requests table
-- This enables "Push to Background" functionality for running tool executions
-- When set, the tool execution should detach to a background process

-- +goose Up

-- Add backgrounded_at column to tool_execution_requests table
ALTER TABLE tool_execution_requests ADD COLUMN backgrounded_at DATETIME;

-- Create index for efficient background checks
CREATE INDEX idx_tool_exec_requests_backgrounded ON tool_execution_requests(tool_call_id, backgrounded_at);

-- +goose Down

-- Remove index first
DROP INDEX IF EXISTS idx_tool_exec_requests_backgrounded;

-- SQLite doesn't support DROP COLUMN easily, leave the column (it's nullable)
