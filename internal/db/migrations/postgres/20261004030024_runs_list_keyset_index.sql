-- +goose NO TRANSACTION
-- +goose Up
-- Serves the cross-cutting run list (RunService.ListRuns): one user's chats
-- newest-first, paged by keyset on (created_at, id). Without it every page
-- sorts all of the user's chats; with it a page is an index range scan that
-- stops at the limit.
--
-- Dropped then created CONCURRENTLY: a failed CONCURRENTLY build leaves an
-- INVALID index that IF NOT EXISTS would accept as finished.
DROP INDEX CONCURRENTLY IF EXISTS idx_chats_user_created_id;
CREATE INDEX CONCURRENTLY idx_chats_user_created_id ON chats (user_id, created_at DESC, id DESC);
