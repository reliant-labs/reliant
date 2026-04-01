-- +goose Up
-- Replace chats_with_status with chats_with_activity view.
-- The new view computes a single `activity` integer based on:
--   0 = IDLE, 1 = RUNNING, 2 = AWAITING_INPUT, 3 = NEEDS_ATTENTION

DROP VIEW IF EXISTS chats_with_status;

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

-- Recreate the old chats_with_status view (from 20260224100000 migration)
CREATE VIEW IF NOT EXISTS chats_with_status AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w2
            WHERE w2.chat_id = c.id
              AND w2.status = 2
              AND w2.workflow_name NOT LIKE 'thread:%'
              AND w2.workflow_name NOT LIKE 'fork:%'
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = c.id
              AND w3.status = 6
              AND w3.workflow_name NOT LIKE 'thread:%'
              AND w3.workflow_name NOT LIKE 'fork:%'
        ) THEN 6
        ELSE (
            SELECT w4.status FROM workflows w4
            WHERE w4.chat_id = c.id AND w4.parent_id IS NULL
            ORDER BY w4.created_at DESC LIMIT 1
        )
    END as workflow_status
FROM chats c;
