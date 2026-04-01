-- +goose Up
-- Add thought_signature column to support Gemini 3.x
-- This is required for Gemini 3 models which use thought signatures for maintaining
-- reasoning context across tool calls

-- Add to message_content_blocks (permanent storage)
-- Note: content_block_chunks was removed in migration 20251124005745_simplify_to_dual_write.sql
ALTER TABLE message_content_blocks ADD COLUMN thought_signature TEXT;

-- +goose Down
ALTER TABLE message_content_blocks DROP COLUMN thought_signature;
