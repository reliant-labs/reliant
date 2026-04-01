-- +goose Up
ALTER TABLE chats ADD COLUMN last_emitted_activity INTEGER NOT NULL DEFAULT 0;

-- Since SQLite doesn't support adding columns to views and we use c.*,
-- we need to recreate the view to pick up the new column.
DROP VIEW IF EXISTS chats_with_activity;
CREATE VIEW chats_with_activity AS
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

        -- Running: any non-thread/fork workflow is running
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
              AND w.workflow_name NOT LIKE 'thread:%'
              AND w.workflow_name NOT LIKE 'fork:%'
        ) THEN 1  -- RUNNING

        -- Needs attention
        WHEN c.state = 1 THEN 3  -- NEEDS_ATTENTION (ChatState_CHAT_STATE_NEEDS_ATTENTION)

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
DROP VIEW IF EXISTS chats_with_activity;

-- Recreate the view without the new column
CREATE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM yields y
            WHERE y.chat_id = c.id AND y.status = 1
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2
              AND w.workflow_name NOT LIKE 'thread:%'
              AND w.workflow_name NOT LIKE 'fork:%'
        ) THEN 1
        WHEN c.state = 1 THEN 3
        ELSE 0
    END as activity
FROM chats c;

ALTER TABLE chats DROP COLUMN last_emitted_activity;
