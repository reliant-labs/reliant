-- forge:backward-compatible — additive index; the previous release never names it and its queries only get faster.

-- Window reads filter context_window_id and order/range on seq (newest-N, seq < cursor, seq >= from),
-- but the only context-window index is (context_window_id, ordinal), so each read fetched and sorted
-- every row of the window. Ascending btree: Postgres walks it backward for ORDER BY seq DESC.
-- CONCURRENTLY cannot run in a transaction block, so this is the only statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_context_window_seq ON public.messages USING btree (context_window_id, seq);
