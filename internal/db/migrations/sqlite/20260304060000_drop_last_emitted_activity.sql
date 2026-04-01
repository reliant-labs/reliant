-- +goose Up
-- Remove last_emitted_activity dedup cache from chats.
-- Activity event dedup is now handled on the frontend (activityStore equality check).
-- The backend always emits chat_activity_changed events, which is simpler and
-- eliminates a class of bugs where the cached value goes stale.

-- Drop the view first (uses c.* which includes the column)
DROP VIEW IF EXISTS chats_with_activity;

ALTER TABLE chats DROP COLUMN last_emitted_activity;

-- Recreate the view (identical to 20260304043700 but without the dropped column)
CREATE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM yields y
            WHERE y.chat_id = c.id AND y.status = 1  -- YIELD_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
        ) THEN 1  -- RUNNING
        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
ALTER TABLE chats ADD COLUMN last_emitted_activity INTEGER NOT NULL DEFAULT 0;

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
        ELSE 0  -- IDLE
    END as activity
FROM chats c;
