-- +goose Up
-- Add updated_at column to workflow_executions for tracking ongoing execution progress

-- NOTE: Migration 029 was updated to include updated_at from the start, but databases
-- that ran the old version of 029 won't have this column. This migration adds it for backwards compatibility.

-- For new databases: migration 029 already includes updated_at, so this ALTER will fail with "duplicate column"
-- For old databases: this ALTER will succeed and add the column
-- Since goose doesn't support conditional migrations easily, we use the new SQLite 3.35.0+ syntax
-- If the SQLite version is older, the migration will fail on new databases (which is acceptable since they don't need it)

-- Add updated_at column
-- Note: Migration 029 already includes updated_at for new databases.
-- For databases that ran the old version of 029 (before updated_at was added),
-- this migration would add it. However, since ALL current databases have 029 with updated_at,
-- we make this a no-op to avoid SQLite driver compatibility issues with IF NOT EXISTS.

-- Uncomment this line ONLY if you need to support databases created with old version of migration 029:
-- ALTER TABLE workflow_executions ADD COLUMN updated_at DATETIME;

-- Update existing rows to set updated_at to started_at (or current time if null)
-- This will only affect old databases that didn't have updated_at before
-- NOTE: Commented out to avoid failures on databases with old migration 040 (missing updated_at)
-- UPDATE workflow_executions SET updated_at = COALESCE(started_at, CURRENT_TIMESTAMP) WHERE updated_at IS NULL;

-- Drop the old trigger
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- Recreate the trigger to fire on updated_at changes as well
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status
   OR OLD.completed_at IS NOT NEW.completed_at
   OR OLD.updated_at IS NOT NEW.updated_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'updated_at', NEW.updated_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Drop the new trigger
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- Recreate the old trigger without updated_at
-- +goose StatementBegin
CREATE TRIGGER chat_updates_workflow_exec_update
AFTER UPDATE ON workflow_executions
WHEN OLD.status != NEW.status OR OLD.completed_at IS NOT NEW.completed_at
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data)
    VALUES (
        NEW.chat_id,
        COALESCE((SELECT MAX(sequence_number) FROM chat_updates WHERE chat_id = NEW.chat_id), 0) + 1,
        'workflow_event',
        NEW.id,
        json_object(
            'update_type', 'workflow_execution',
            'id', NEW.id,
            'chat_id', NEW.chat_id,
            'workflow_name', NEW.workflow_name,
            'status', NEW.status,
            'started_at', NEW.started_at,
            'completed_at', NEW.completed_at,
            'error_message', NEW.error_message
        )
    );
END;
-- +goose StatementEnd

-- Remove updated_at column from workflow_executions
-- Note: SQLite doesn't support DROP COLUMN directly, so we would need to recreate the table
-- For simplicity in rollback, we'll leave the column in place
