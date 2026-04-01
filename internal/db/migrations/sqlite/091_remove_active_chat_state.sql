-- +goose Up
-- Migration: Remove 'active' from chat.state
-- Activity is now derived from workflow.status (root_workflow_status), not stored in chat.state
-- chat.state is now only for notification/lifecycle: needs_attention, idle, archived

-- Update any chats currently marked as 'active' to 'idle'
-- This is safe because activity is now derived from workflow.status
UPDATE chats SET state = 'idle' WHERE state = 'active';

-- +goose Down
-- No rollback needed - 'active' state will be set by workflow_status activity if needed
