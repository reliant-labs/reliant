-- forge:backward-compatible — messages_thread_ordinal_key is a unique index on the same (thread_id, ordinal); every read the previous release makes through this one is served by it.

-- Migration: drop_messages_thread_ordinal_index
--
-- idx_messages_thread_ordinal (thread_id, ordinal) is column for column the
-- unique constraint messages_thread_ordinal_key, so it only costs writes and
-- cache: every message insert maintained both (147MB each). Prod: 1.1k scans
-- against the key's 1.77M.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_messages_thread_ordinal;
