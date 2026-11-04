-- forge:backward-compatible — additive index; the previous release's queries read through it or through the indexes it leaves in place.

-- Migration: workflows_chat_state_index
--
-- chats_with_activity asks of every chat it lists "does it have a running
-- workflow", "a stopped one", "its newest completed and failed ones", and
-- counts its running threads: each a lookup of one chat's workflows by
-- state. With only (chat_id) indexed, each lookup read all of the chat's
-- workflow rows from the heap to test state, or ANDed a bitmap of
-- idx_workflows_state (every running workflow of every user) into it.
-- (chat_id, state, stop_reason) answers each one from the index range of
-- exactly the rows it asks for.
--
-- It also serves every chat_id-only lookup idx_workflows_chat_id served, so
-- the next migration drops that one.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflows_chat_state ON public.workflows USING btree (chat_id, state, stop_reason);
