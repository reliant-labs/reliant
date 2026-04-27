-- +goose Up
-- Create questions table (replaces yields)
CREATE TABLE IF NOT EXISTS questions (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL,
    temporal_workflow_id TEXT NOT NULL DEFAULT '',
    thread_id TEXT NOT NULL DEFAULT '',
    step_id TEXT NOT NULL DEFAULT '',
    loop_node_id TEXT,
    loop_iteration INTEGER,
    status INTEGER NOT NULL DEFAULT 1,
    metadata TEXT,
    response_data TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMP,
    tool_call_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_questions_chat_id ON questions(chat_id);
CREATE INDEX IF NOT EXISTS idx_questions_workflow_id ON questions(workflow_id);
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);

-- Drop yields table
DROP TABLE IF EXISTS yields;

-- Recreate chats_with_activity view replacing yields with questions
DROP VIEW IF EXISTS chats_with_activity;
CREATE VIEW chats_with_activity AS
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

-- +goose Down
DROP INDEX IF EXISTS idx_questions_status;
DROP INDEX IF EXISTS idx_questions_workflow_id;
DROP INDEX IF EXISTS idx_questions_chat_id;
DROP TABLE IF EXISTS questions;

-- Recreate chats_with_activity view with yields reference (restore previous state)
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
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 4
        ) THEN 3  -- ERROR
        ELSE 0  -- IDLE
    END as activity
FROM chats c;
