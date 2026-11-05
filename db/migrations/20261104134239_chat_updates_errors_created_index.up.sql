-- forge:backward-compatible — additive index; the previous release never reads through it.

-- Migration: chat_updates_errors_created_index
--
-- A chat's ERROR (6) updates by time (store.ErrorUpdatesInRangeSQL): the
-- errors a ListMessages scroll-back page brings for the stretch of history it
-- loads, so each renders at its place in the timeline. Without it the read
-- walks every update the chat ever logged (hundreds of thousands on a busy
-- chat) to find a handful of errors.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_errors_created ON public.chat_updates USING btree (chat_id, created_at) WHERE update_type = 6;

