-- +goose Up
-- Create the chats_with_status view used by all chat queries.
-- This view was previously only in schema.sql but never in a migration.

CREATE OR REPLACE VIEW chats_with_status AS
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

-- +goose Down
DROP VIEW IF EXISTS chats_with_status;
