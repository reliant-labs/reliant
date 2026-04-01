-- +goose Up
-- Migration: Add threads and context_windows tables for unified context management
--
-- This migration introduces:
-- 1. threads table: First-class entity for thread hierarchy and fork relationships
-- 2. context_windows table: Atomic unit for LLM context (what gets sent to the model)
--
-- The key insight is that threads handle structure (hierarchy, forks) while
-- context_windows handle what the LLM sees (messages + token counts).

-- =============================================================================
-- THREADS TABLE
-- =============================================================================
-- Threads represent execution contexts. They can be:
-- - Root thread (parent_thread_id IS NULL, no fork)
-- - Sub-agent thread (parent_thread_id in same conversation)
-- - Branched root (parent_thread_id in DIFFERENT conversation)
--
-- The type is DERIVED:
--   NULL parent = root
--   parent in same conversation = sub_agent
--   parent in different conversation = branch
CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,  -- Which conversation this thread belongs to

    -- Hierarchy and fork source (can point cross-conversation for branches)
    parent_thread_id TEXT,

    -- Fork point in parent (NULL if not forked, i.e., fresh root thread)
    fork_at_ordinal INTEGER,
    fork_at_context_window_id TEXT,

    -- Link to workflow for execution state (optional - threads can exist without workflow)
    workflow_id TEXT,

    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (conversation_id) REFERENCES chats(id) ON DELETE CASCADE,
    -- Note: parent_thread_id is NOT a FK because it can point cross-conversation
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_threads_conversation ON threads(conversation_id);
CREATE INDEX IF NOT EXISTS idx_threads_parent ON threads(parent_thread_id) WHERE parent_thread_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_threads_workflow ON threads(workflow_id) WHERE workflow_id IS NOT NULL;

-- =============================================================================
-- CONTEXT_WINDOWS TABLE
-- =============================================================================
-- A context_window is the atomic unit for what gets sent to the LLM.
-- Each thread has one or more context_windows (sequence increments on compaction).
-- Token counts are cached here for O(1) lookup.
CREATE TABLE IF NOT EXISTS context_windows (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,

    -- Sequence number: 0 for initial, increments on compaction
    sequence INTEGER NOT NULL DEFAULT 0,

    -- Cached token counts (updated when messages are added/modified)
    total_input_tokens INTEGER NOT NULL DEFAULT 0,
    total_output_tokens INTEGER NOT NULL DEFAULT 0,
    total_cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    total_cache_read_tokens INTEGER NOT NULL DEFAULT 0,

    -- If this context_window was created by compaction, link to summary message
    compaction_summary_message_id TEXT,

    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
    UNIQUE(thread_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_context_windows_thread ON context_windows(thread_id, sequence DESC);

-- =============================================================================
-- MESSAGES TABLE CHANGES
-- =============================================================================
-- Add context_window_id to messages (nullable initially for migration)
-- This will eventually replace thread + context_sequence columns
ALTER TABLE messages ADD COLUMN context_window_id TEXT REFERENCES context_windows(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_messages_context_window ON messages(context_window_id, ordinal)
    WHERE context_window_id IS NOT NULL;

-- =============================================================================
-- DATA MIGRATION
-- =============================================================================

-- Step 0: Create threads for ALL unique (chat_id, thread) pairs from messages
-- This ensures orphaned messages (without workflows) still get their threads created
INSERT INTO threads (id, conversation_id, parent_thread_id, fork_at_ordinal, workflow_id, created_at)
SELECT DISTINCT
    m.thread AS id,
    m.chat_id AS conversation_id,
    NULL AS parent_thread_id,  -- Will be updated later from workflow data if available
    NULL AS fork_at_ordinal,
    NULL AS workflow_id,
    MIN(m.created_at) AS created_at
FROM messages m
WHERE m.thread IS NOT NULL
GROUP BY m.chat_id, m.thread
ON CONFLICT(id) DO NOTHING;

-- Step 1: Update threads with workflow data (fork info, workflow_id)
-- For threads that have an associated workflow, update fork metadata
UPDATE threads SET
    parent_thread_id = (
        SELECT CASE 
            WHEN w.forked_from_chat_id IS NOT NULL THEN w.forked_from_thread
            WHEN w.forked_from_thread IS NOT NULL AND w.forked_from_thread != w.thread THEN w.forked_from_thread
            ELSE NULL 
        END
        FROM workflows w
        WHERE w.thread = threads.id
        LIMIT 1
    ),
    fork_at_ordinal = (
        SELECT w.forked_at_ordinal
        FROM workflows w
        WHERE w.thread = threads.id
        LIMIT 1
    ),
    workflow_id = (
        SELECT w.id
        FROM workflows w
        WHERE w.thread = threads.id
        LIMIT 1
    )
WHERE EXISTS (SELECT 1 FROM workflows w WHERE w.thread = threads.id);

-- Step 2: Create context_windows for each unique (chat_id, thread, context_sequence) in messages
INSERT INTO context_windows (id, thread_id, sequence, created_at)
SELECT DISTINCT
    -- Generate deterministic ID from chat_id + thread + sequence
    m.chat_id || ':' || m.thread || ':' || m.context_sequence AS id,
    m.thread AS thread_id,
    m.context_sequence AS sequence,
    MIN(m.created_at) AS created_at
FROM messages m
WHERE EXISTS (SELECT 1 FROM threads t WHERE t.id = m.thread)
GROUP BY m.chat_id, m.thread, m.context_sequence
ON CONFLICT DO NOTHING;

-- Step 3: Update messages to point to their context_window
UPDATE messages SET context_window_id = (
    SELECT cw.id 
    FROM context_windows cw
    JOIN threads t ON t.id = cw.thread_id
    WHERE cw.thread_id = messages.thread 
      AND cw.sequence = messages.context_sequence
      AND t.conversation_id = messages.chat_id
)
WHERE EXISTS (
    SELECT 1 FROM context_windows cw
    JOIN threads t ON t.id = cw.thread_id
    WHERE cw.thread_id = messages.thread 
      AND cw.sequence = messages.context_sequence
      AND t.conversation_id = messages.chat_id
);

-- Step 4: Populate fork_at_context_window_id for forked threads
-- Now that context_windows exist, we can look up the context_window_id at the fork point
UPDATE threads SET fork_at_context_window_id = (
    SELECT m.context_window_id
    FROM messages m
    WHERE m.thread = threads.parent_thread_id
      AND m.ordinal = threads.fork_at_ordinal
    LIMIT 1
)
WHERE threads.parent_thread_id IS NOT NULL 
  AND threads.fork_at_ordinal IS NOT NULL;

-- Step 5: Update cached token counts on context_windows
UPDATE context_windows SET
    total_input_tokens = COALESCE((
        SELECT SUM(COALESCE(input_tokens, 0)) 
        FROM messages 
        WHERE context_window_id = context_windows.id
    ), 0),
    total_output_tokens = COALESCE((
        SELECT SUM(COALESCE(output_tokens, 0)) 
        FROM messages 
        WHERE context_window_id = context_windows.id
    ), 0),
    total_cache_creation_tokens = COALESCE((
        SELECT SUM(COALESCE(cache_creation_tokens, 0)) 
        FROM messages 
        WHERE context_window_id = context_windows.id
    ), 0),
    total_cache_read_tokens = COALESCE((
        SELECT SUM(COALESCE(cache_read_tokens, 0)) 
        FROM messages 
        WHERE context_window_id = context_windows.id
    ), 0);

-- +goose Down
-- Remove new columns and tables
DROP INDEX IF EXISTS idx_messages_context_window;
ALTER TABLE messages DROP COLUMN context_window_id;
DROP TABLE IF EXISTS context_windows;
DROP TABLE IF EXISTS threads;
