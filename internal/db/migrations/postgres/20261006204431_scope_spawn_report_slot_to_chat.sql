-- +goose Up
--
-- One terminal report per spawn, where a spawn is (chat_id, tool_call_id)
-- rather than tool_call_id alone.
--
-- tool_call_id is chosen by the model provider, not by us. A provider can
-- hand two chats the same id — the Vertex Gemini driver used the function name
-- ("spawn") until #560, and a local OpenAI-compatible server may number calls
-- from call_0 in every conversation — and the chat-blind slot then held one
-- chat's report against every other chat's spawn of the same id. The second
-- report's upsert found the slot taken and returned "already reported" with no
-- error, so that chat's parent never received its sub-agent's result, and the
-- stranded-spawn sweep's placeholder for it was a silent no-op too.
--
-- Same name as the index it replaces, so every reference to it (the incident
-- doc, the 23505 a plain INSERT reports) still names the right constraint.
-- Built before the old one is dropped, in one transaction: there is no moment
-- with no slot at all. agent_messages holds one terminal row per spawn, so the
-- build is short.
--
-- A release older than this one cannot write reports against this schema: its
-- ON CONFLICT (tool_call_id) has no matching unique index any more. Roll
-- forward, not back (repo policy); see the PR for the rollout window.
CREATE UNIQUE INDEX idx_agent_messages_one_terminal_report_per_chat_spawn
    ON agent_messages (chat_id, tool_call_id)
    WHERE kind IN (2, 3, 4);
DROP INDEX idx_agent_messages_one_terminal_report_per_spawn;
ALTER INDEX idx_agent_messages_one_terminal_report_per_chat_spawn
    RENAME TO idx_agent_messages_one_terminal_report_per_spawn;
