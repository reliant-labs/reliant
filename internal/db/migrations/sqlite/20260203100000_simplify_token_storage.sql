-- +goose Up
-- Migration: Simplify token storage for correct Gemini handling
--
-- Problem: The 4-field token storage (input, output, cache_creation, cache_read)
-- doesn't work well with Gemini which reports cumulative context size rather than
-- per-turn deltas like Anthropic/OpenAI.
--
-- Solution: Replace with a single token_count field that represents "context size"
-- - For Anthropic: input_tokens (context size for this request)
-- - For OpenAI: prompt_tokens (context size for this request)  
-- - For Gemini: PromptTokenCount (cumulative context size)
--
-- Also adds cost_micros field for future billing (unused for now).

-- Add new columns
ALTER TABLE messages ADD COLUMN token_count INTEGER;
ALTER TABLE messages ADD COLUMN cost_micros INTEGER;

-- Migrate existing data: use input_tokens as the context size
-- (This is correct for Anthropic/OpenAI where input_tokens represents context)
UPDATE messages SET token_count = input_tokens WHERE input_tokens IS NOT NULL;

-- Drop the old columns (SQLite requires table recreation for column drops)
-- Create new table without old columns
CREATE TABLE messages_new (
    id TEXT PRIMARY KEY NOT NULL,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    thread_id TEXT NOT NULL,
    context_window_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant', 'tool', 'system')),
    display_style TEXT CHECK(display_style IS NULL OR display_style IN ('hidden', 'info', 'warning', 'success')),
    model TEXT,
    agent TEXT,
    token_count INTEGER,
    cost_micros INTEGER,
    workflow_id TEXT,
    run_id TEXT,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    created_at DATETIME DEFAULT (datetime('now', 'utc')) NOT NULL,
    updated_at DATETIME DEFAULT (datetime('now', 'utc')) NOT NULL
);

-- Copy data
INSERT INTO messages_new (id, chat_id, ordinal, thread_id, context_window_id, role, display_style, model, agent, token_count, cost_micros, workflow_id, run_id, node_id, node_path, activity_id, created_at, updated_at)
SELECT id, chat_id, ordinal, thread_id, context_window_id, role, display_style, model, agent, token_count, cost_micros, workflow_id, run_id, node_id, node_path, activity_id, created_at, updated_at
FROM messages;

-- Drop old and rename new
DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;

-- Recreate indexes
CREATE INDEX idx_messages_chat_id ON messages(chat_id);
CREATE INDEX idx_messages_thread_id ON messages(thread_id);
CREATE INDEX idx_messages_context_window_id ON messages(context_window_id);
CREATE INDEX idx_messages_ordinal ON messages(ordinal);
CREATE UNIQUE INDEX idx_messages_chat_thread_ordinal ON messages(chat_id, thread_id, ordinal);

-- +goose Down
-- Note: This down migration won't restore the original data since we dropped the columns
ALTER TABLE messages ADD COLUMN input_tokens INTEGER;
ALTER TABLE messages ADD COLUMN output_tokens INTEGER;
ALTER TABLE messages ADD COLUMN cache_creation_tokens INTEGER;
ALTER TABLE messages ADD COLUMN cache_read_tokens INTEGER;
ALTER TABLE messages DROP COLUMN token_count;
ALTER TABLE messages DROP COLUMN cost_micros;