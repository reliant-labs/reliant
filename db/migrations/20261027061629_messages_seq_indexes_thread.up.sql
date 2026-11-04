-- forge:backward-compatible — additive index; the previous release never names it and its queries only get faster.

-- LatestMessageInThread / LatestTurnInThread order a thread's messages by seq DESC LIMIT 1, but the only
-- thread index is (thread_id, ordinal). Ascending btree: Postgres walks it backward for ORDER BY seq DESC.
-- CONCURRENTLY cannot run in a transaction block, so this is the only statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_thread_seq ON public.messages USING btree (thread_id, seq);
