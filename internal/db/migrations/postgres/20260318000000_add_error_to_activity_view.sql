-- +goose Up
-- Add ERROR activity state for failed workflows.
-- Previously, failed workflows (status=4) fell through to IDLE (0),
-- making the chat UI show no indication of failure.
-- Now they return activity=3 (CHAT_ACTIVITY_ERROR).

CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        -- Awaiting input: pending approvals or yields
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM yields y
            WHERE y.chat_id = c.id AND y.status = 1  -- YIELD_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT

        -- Running: any workflow for this chat is running (including threads/forks)
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
        ) THEN 1  -- RUNNING

        -- Error: any workflow for this chat has failed
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 4  -- WorkflowStatusFailed
        ) THEN 3  -- ERROR

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM yields y
            WHERE y.chat_id = c.id AND y.status = 1
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2
        ) THEN 1  -- RUNNING
        ELSE 0  -- IDLE
    END as activity
FROM chats c;
