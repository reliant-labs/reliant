-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning, and the index covers only rows (app_name set) that release never writes.

-- Migration: chats_by_app_index
-- Created: 2026-11-19T17:47:25Z
--
-- An app's chat list (ListChats with app_name): a person's chats tagged with
-- one app, most recently active first, then narrowed to one org. Tagged chats
-- are a slice of chats, so a partial index keeps it small and keeps every
-- untagged chat write from paying for it. org_id is not a key column: the
-- org filter also admits a person's chats with no org (core.OrgScope), an OR
-- the index could not seek on, and one person's chats of one app are few.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. IF NOT EXISTS makes a retry after a failed build a
-- no-op only when the index is valid; an INVALID leftover is dropped by hand
-- (DROP INDEX CONCURRENTLY chats_by_app) before the retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS chats_by_app ON public.chats (user_id, app_name, last_active DESC) WHERE app_name IS NOT NULL;
