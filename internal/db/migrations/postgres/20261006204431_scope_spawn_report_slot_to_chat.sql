-- +goose Up
--
-- EXPAND: one terminal report per spawn, where a spawn is (chat_id,
-- tool_call_id) rather than tool_call_id alone.
--
-- tool_call_id is chosen by the model provider, not by us. A provider can
-- hand two chats the same id — the Vertex Gemini driver used the function name
-- ("spawn") until #560, and a local OpenAI-compatible server may number calls
-- from call_0 in every conversation — and the chat-blind slot then held one
-- chat's report against every other chat's spawn of the same id. The second
-- report's upsert found the slot taken and returned "already reported" with no
-- error, so that chat's parent never received its sub-agent's result.
--
-- This step only ADDS the chat-scoped index; the current code arbitrates on
-- it. The old chat-blind idx_agent_messages_one_terminal_report_per_spawn
-- stays, because the previous release's report writers name it in
-- ON CONFLICT (tool_call_id): dropping it here would break that release
-- against this schema. While both exist, a second chat's report under an id
-- another chat already reported trips the old index, and the writer reports
-- core.ErrSpawnReportSlotTaken — loud, never "already reported". The contract
-- step drops the old index once no deployment runs the previous release, and
-- then such a report is delivered.
--
-- agent_messages holds one terminal row per spawn, so the build is short.
-- IF NOT EXISTS so a replay is a no-op (TestRenumberWindowDatabaseMigratesOnPlainStartup
-- re-runs every migration after its window against a migrated schema).
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_messages_one_terminal_report_per_chat_spawn
    ON agent_messages (chat_id, tool_call_id)
    WHERE kind IN (2, 3, 4);
