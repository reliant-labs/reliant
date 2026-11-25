-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: llm_call_usage_chat_id_idx
-- Created: 2026-11-25T08:51:36Z
--
-- The per-chat Trace/Cost tab (ChatTraceService.GetChatTrace,
-- dev-docs/design/agent-analytics.md §2.2) lists one chat's model calls in
-- time order. llm_call_usage had only (created_at) and (user_id, created_at),
-- so that read would scan every call of the chat owner's last ninety days
-- (every call of an automation-heavy user) to find one chat's.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A failed build can leave an INVALID index and a
-- dirty migration record. Dropping the index or redeploying alone does not
-- clear that record; see dev-docs/archived-chat-index-recovery.md.
--
-- Rollback: none needed. An extra index changes no result; the previous
-- release ignores it, so it stays when the application is rolled back.
CREATE INDEX CONCURRENTLY IF NOT EXISTS llm_call_usage_chat_created_at_idx ON public.llm_call_usage (chat_id, created_at);
