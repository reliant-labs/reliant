-- +goose Up
-- Add PAUSED activity state for paused workflows.
-- Previously, paused workflows (status=6) fell through to IDLE (0),
-- making the chat UI report IDLE while a workflow was actually paused
-- (the frontend then infers in-flight tool calls must have completed).
-- Now they return activity=4 (CHAT_ACTIVITY_PAUSED).
--
-- Branch order: AWAITING_INPUT (pending approval/question) stays first, since
-- an open gate is more actionable than a pause. RUNNING stays ahead of PAUSED
-- so a chat with one running and one paused workflow still reads RUNNING.
-- ERROR stays ahead of PAUSED so a chat with one failed and one paused
-- workflow still surfaces the failure.

CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        -- Awaiting input: pending approvals or questions
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM questions q
            WHERE q.chat_id = c.id AND q.status = 1  -- QUESTION_STATUS_PENDING
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

        -- Paused: any workflow for this chat is paused
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 6  -- WorkflowStatusPaused
        ) THEN 4  -- PAUSED

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        -- Awaiting input: pending approvals or questions
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM questions q
            WHERE q.chat_id = c.id AND q.status = 1  -- QUESTION_STATUS_PENDING
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
