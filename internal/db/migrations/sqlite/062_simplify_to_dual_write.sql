-- +goose Up
-- Simplify architecture: Remove triggers and content_block_chunks
-- Use direct dual-writes to chat_updates instead

-- Drop ALL chat_updates triggers - we'll use direct dual-writes instead
DROP TRIGGER IF EXISTS chat_updates_message_insert;
DROP TRIGGER IF EXISTS chat_updates_message_update;
DROP TRIGGER IF EXISTS chat_updates_content_block_enrich;
DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;
DROP TRIGGER IF EXISTS chat_updates_step_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_step_exec_update;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_tool_approval_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_approval_update;
DROP TRIGGER IF EXISTS chat_updates_workflow_event_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_insert;
DROP TRIGGER IF EXISTS chat_updates_workflow_exec_update;

-- Drop content_block_chunks table (no longer needed)
DROP INDEX IF EXISTS idx_content_block_chunks_message;
DROP TABLE IF EXISTS content_block_chunks;

-- Remove unused columns from messages table
ALTER TABLE messages DROP COLUMN is_streaming;

-- Remove unused columns from message_content_blocks table
ALTER TABLE message_content_blocks DROP COLUMN is_complete;

-- +goose Down
-- This migration is not reversible - restoring triggers would be complex
-- If needed, restore from migration 056
