-- forge:backward-compatible — only COMMENT ON CONSTRAINT: no table, column, constraint or index changes, so every statement of the previous release means what it did.

-- Migration: seed_ref_declarations
-- Created: 2026-11-21T10:23:23Z
--
-- Where a row reaches one parent by two foreign-key paths, say which path is
-- the truth. `forge db seed apply` refuses to seed until it knows (it will not
-- write rows whose two paths name different parents), and the sentence is a
-- fact about reliant's domain, so it lives on the constraint where psql \d+ and
-- every other reader see it too. Only the `forge:ref ...` clause is read.
--
-- Pairs not declared here are ones forge resolves itself: a NOT NULL direct
-- column whose other path leaves through a NULLABLE column is `authoritative`
-- (tool_calls.chat_id, triggers.project_id, threads.chat_id, ...).

-- The chat scopes an agent message: to_thread_id, the recipient, is a thread
-- of it. from_thread_id is too, except for a run message (send_to_run,
-- internal/db/core/agent_message.go), whose sender is another chat's main
-- thread. One declaration covers both paths, so the seeder keeps both in the
-- chat: rows that are all valid, if never a cross-chat run message.
COMMENT ON CONSTRAINT agent_messages_chat_id_fkey ON agent_messages IS
    'forge:ref authoritative — the chat scopes the message; to_thread_id is a thread of it, and so is from_thread_id except for a run message from another chat';

-- messages.chat_id is denormalized from the message's thread
-- (internal/db/core/message.go): the thread decides the chat.
COMMENT ON CONSTRAINT messages_chat_id_fkey ON messages IS
    'forge:ref derived-from=thread_id — chat_id is denormalized from the thread the message belongs to';

-- A tool call's thread is the truth; its (optional) message is the assistant
-- message in that thread that carries the call.
COMMENT ON CONSTRAINT tool_calls_thread_id_fkey ON tool_calls IS
    'forge:ref authoritative — message_id, when set, is the assistant message of this thread that carries the call';

-- Two different messages: the result's message is the tool-role message that
-- carries the result (internal/db/core/tool_call.go), the call's is the
-- assistant message that made the call.
COMMENT ON CONSTRAINT tool_call_results_message_id_fkey ON tool_call_results IS
    'forge:ref independent — the tool-role result message, not the assistant message that carries the call';

-- A background process and a user update name their project directly; the
-- chat and worktree they may also name belong to it.
COMMENT ON CONSTRAINT background_processes_project_id_fkey ON background_processes IS
    'forge:ref authoritative — the project scopes the row; chat_id and worktree_id, when set, belong to it';

COMMENT ON CONSTRAINT user_updates_project_id_fkey ON user_updates IS
    'forge:ref authoritative — the project scopes the row; chat_id and worktree_id, when set, belong to it';
