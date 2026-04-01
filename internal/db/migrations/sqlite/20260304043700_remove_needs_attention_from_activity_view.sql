-- +goose Up
-- Remove stale NEEDS_ATTENTION branch from chats_with_activity view.
-- CHAT_STATE_NEEDS_ATTENTION (state=1) was removed from the proto enum and
-- replaced by the boolean `unread` column (migration 20260227000000).
-- However, the view was re-created with the old branch in 20260302220000.
-- Any chats still stuck at state=1 are migrated to state=2 (IDLE).

-- Fix any rows stuck at the removed state value
UPDATE chats SET state = 2 WHERE state = 1;

-- Recreate view without the state=1 branch
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

        -- Running: any workflow for this chat is running (including threads/forks)
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
        ) THEN 1  -- RUNNING

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
DROP VIEW IF EXISTS chats_with_activity;
CREATE VIEW chats_with_activity AS
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
        WHEN c.state = 1 THEN 3  -- NEEDS_ATTENTION
        ELSE 0  -- IDLE
    END as activity
FROM chats c;
