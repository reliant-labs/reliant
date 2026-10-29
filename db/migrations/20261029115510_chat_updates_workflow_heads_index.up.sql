-- forge:backward-compatible — additive index; the previous release never reads through it.

-- Migration: chat_updates_workflow_heads_index
--
-- The newest NODE_EXECUTION (9) update of each node of one workflow
-- (store.WorkflowNodeHeadsSQL): what ChatService.GetWorkflowNodeExecutions
-- returns when a run-status view opens. A node's entity_id is
-- "<workflow id>:<node id>", so one workflow's nodes are a key range.
--
-- entity_id is indexed under COLLATE "C" so that range is contiguous,
-- [id || ':', id || ';'), whatever the database's default collation: a
-- linguistic collation may ignore ':' at the first level and interleave
-- another workflow's nodes with this one's.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_workflow_heads ON public.chat_updates USING btree (chat_id, (entity_id COLLATE "C"), sequence_number DESC) WHERE update_type = 9;
