-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning, and the index covers only rows (visibility = 'org') that release never writes.

-- Migration: chats_shared_by_org_index
-- Created: 2026-11-12T16:22:01Z
--
-- ListSharedChats reads an org's shared chats, most recently active first
-- (control-plane docs/design/org-sharing.md §9). Shared chats are a small
-- slice of chats, so a partial index keeps it small and keeps every other
-- chat write from paying for it.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. IF NOT EXISTS makes a retry after a failed build a
-- no-op only when the index is valid; an INVALID leftover is dropped by hand
-- (DROP INDEX CONCURRENTLY chats_shared_by_org) before the retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS chats_shared_by_org ON public.chats (org_id, last_active DESC) WHERE visibility = 'org';
