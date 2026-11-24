-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: worktrees_branch_index
-- Created: 2026-11-24T20:43:26Z
--
-- When a pr_records row first appears, its head branch is matched against
-- worktree branches to infer which chat produced it (chat_pr_links, the
-- head_ref fallback; store.InferChatPRLinks). Nothing indexed worktrees by
-- branch, so every new pull request would scan the table. Partial on what the
-- match can use: a project's main worktree is never a pull request's head.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_worktrees_branch ON public.worktrees (branch) WHERE NOT is_main;
