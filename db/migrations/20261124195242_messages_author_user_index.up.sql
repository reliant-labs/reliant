-- forge:backward-compatible — a new partial index; no release depends on it, and it holds only rows the previous release never writes.

-- Migration: messages_author_user_index
--
-- The messages a person wrote that the send path stamped with an author
-- (control-plane docs/design/chat-collaboration.md §5.3, §6.1): a chat
-- member's (author_user_id set) and, once collaboration is on, the owner's
-- own (author_name set, author_user_id NULL), newest first per chat. Model
-- output, tool results, agent and workflow messages and every row written
-- before collaboration carry neither, and are not in it. Two reads walk it:
--
--   - every LLM call of a chat in an org asks who wrote the chat's latest
--     stamped message (store.LatestHumanTurnAuthor): a member's means the
--     call draws on org-funded accounts only. One index probe;
--   - an account purge clears the name a purged member wrote under.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_chat_author
    ON public.messages (chat_id, created_at DESC)
    WHERE author_user_id IS NOT NULL OR author_name IS NOT NULL;
