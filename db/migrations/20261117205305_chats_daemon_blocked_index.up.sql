-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: chats_daemon_blocked_index
-- Created: 2026-11-17T20:53:05Z
--
-- The Inbox's waiting-for-machine items (store.ListInboxPending, kind 3) are
-- the caller's chats whose daemon is unreachable mid-run: daemon_blocked_at
-- set, which is rare (10 of 42,773 chats in prod on 2026-11-17). No index
-- answered it: idx_chats_user_created_id selects nothing when one user owns
-- nearly every chat, so each Inbox read was a Seq Scan of chats (85ms in prod,
-- reliant#2736) that grew with the whole table. Partial, so it holds only the
-- blocked chats.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY
-- idx_chats_daemon_blocked and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chats_daemon_blocked ON public.chats (user_id) WHERE daemon_blocked_at IS NOT NULL;
