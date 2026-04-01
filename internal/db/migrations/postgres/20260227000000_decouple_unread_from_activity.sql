-- +goose Up
-- Decouple unread badge from activity status.

DROP VIEW IF EXISTS chats_with_activity;

ALTER TABLE chats ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;

UPDATE chats SET unread = 1, state = 2 WHERE state = 1;

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
              AND w.workflow_name NOT LIKE 'thread:%'
              AND w.workflow_name NOT LIKE 'fork:%'
        ) THEN 1  -- RUNNING
        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
DROP VIEW IF EXISTS chats_with_activity;

UPDATE chats SET state = 1 WHERE unread = 1;

ALTER TABLE chats DROP COLUMN unread;

CREATE OR REPLACE VIEW chats_with_activity AS
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
