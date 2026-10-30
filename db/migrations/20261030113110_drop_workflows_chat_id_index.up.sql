-- forge:backward-compatible — idx_workflows_chat_state (the previous migration) is a superset: every chat_id lookup the previous release makes is served by it.

-- Migration: drop_workflows_chat_id_index
--
-- idx_workflows_chat_id (chat_id) is a prefix of idx_workflows_chat_state
-- (chat_id, state, stop_reason), so it only costs writes: every workflow
-- insert and state change maintained both.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_workflows_chat_id;
