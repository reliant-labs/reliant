-- forge:backward-compatible — no release queries message_content_blocks by activity_id or workflow_run_id; they are written with the block and only ever read with it, by message_id.

-- Migration: drop_content_blocks_activity_index
--
-- idx_content_blocks_activity (activity_id, workflow_run_id) has no reader:
-- content blocks are read by message_id (idx_content_blocks_message_position)
-- or tool_call_id (idx_content_blocks_tool_call_id), and no foreign key
-- references these columns. Prod counted 0 scans of it in 69 days, against
-- 20MB and an index write on every content block.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_content_blocks_activity;
