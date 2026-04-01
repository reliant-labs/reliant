-- +goose Up
-- Ensure updated_at column exists on workflow_executions
-- This is a safety migration for databases where migration 032 may have failed

-- Note: Migration 029 already includes updated_at column in workflow_executions table.
-- Migration 032 was a backwards-compatibility migration for old databases, but it's now a no-op.
-- This migration (042) is also a no-op since the column is guaranteed to exist from migration 029.

-- Uncomment this line ONLY if you need to support databases with missing updated_at column:
-- ALTER TABLE workflow_executions ADD COLUMN updated_at DATETIME;

-- Update any NULL values (safe to run even if column already exists)
-- NOTE: This is commented out because it will fail if the column doesn't exist
-- If you have an existing database created with old migration 040 (missing updated_at),
-- you should delete the database and recreate it with the fixed migrations.
-- UPDATE workflow_executions
-- SET updated_at = COALESCE(started_at, CURRENT_TIMESTAMP)
-- WHERE updated_at IS NULL;

-- +goose Down
-- No-op - we don't want to remove the column if it exists
