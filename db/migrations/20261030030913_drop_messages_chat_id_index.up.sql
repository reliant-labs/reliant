-- forge:backward-compatible — idx_messages_chat_thread (the previous migration) is a superset: every chat_id lookup the previous release makes is served by it.

-- Migration: drop_messages_chat_id_index
--
-- idx_messages_chat_id (chat_id) is a prefix of idx_messages_chat_thread
-- (chat_id, thread_id), so it only costs writes: every message insert
-- maintained both.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_messages_chat_id;
