-- +goose Up
--
-- The agent mailbox (spec: async-spawn-and-agent-messaging.md, §5).
--
-- Today there is no way to pass a message into a sub-agent that is already
-- running: a bare INSERT into `messages`, the trick the user-facing send path
-- uses, is unsafe here because an agent is mid-turn most of the time. The
-- product has a hard invariant that an assistant message with tool_use blocks
-- must always reach the LLM with matching tool_result blocks, or the provider
-- deadlocks the conversation (see 20260801010000_add_tool_calls.sql) --
-- inserting a message between an assistant-with-tool_calls row and its
-- tool_results row produces exactly that deadlock.
--
-- So delivery is queued here and only drained into `messages` at a boundary
-- where history is known-consistent (the top of the next agent-loop
-- iteration, after tool results are already saved). This is the same
-- create -> wake -> resolve shape `questions` and `approvals` already use.
--
-- kind: 1=message (a peer/parent sending free-form text), 2=completion (a
-- child agent finished), 3=cancelled, 4=failed.
-- status: 1=queued, 2=delivered.
CREATE TABLE agent_messages (
    id             text PRIMARY KEY,
    chat_id        text NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    from_thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    to_thread_id   text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    kind           integer NOT NULL,
    body           text NOT NULL,
    tool_call_id   text,
    status         integer NOT NULL,
    created_at     timestamptz NOT NULL,
    delivered_at   timestamptz,
    delivered_message_id text REFERENCES messages(id) ON DELETE SET NULL,
    -- A row claiming to be DELIVERED (status 2) without a delivery time is a
    -- contradiction the schema should refuse to store -- same principle as
    -- tool_calls_completed_has_completed_at (20260801010000:45-49).
    CONSTRAINT agent_messages_delivered_has_time
        CHECK (status <> 2 OR delivered_at IS NOT NULL)
);

-- Partial index on the queued rows only: the drain query
-- (ListQueuedAgentMessagesForThread) always filters status = 1, and delivered
-- rows accumulate as history with nothing that needs to scan them by recipient.
CREATE INDEX idx_agent_messages_inbox
    ON agent_messages(to_thread_id, created_at) WHERE status = 1;

-- +goose Down

DROP TABLE IF EXISTS agent_messages;
