-- +goose Up
-- Migration: Add chat state column, drop is_archived, and create user_updates table
-- This enables real-time chat state management without Temporal API calls

-- Add state column to chats table
-- States: active (workflow running), needs_attention (stopped/approval), idle (acknowledged), archived
ALTER TABLE chats ADD COLUMN state TEXT DEFAULT 'idle' 
    CHECK (state IN ('active', 'needs_attention', 'idle', 'archived'));

-- Migrate existing is_archived data to state column
UPDATE chats SET state = CASE 
    WHEN is_archived = TRUE THEN 'archived'
    ELSE 'idle'
END;

-- Drop is_archived column (SQLite requires table recreation)
-- Create new table without is_archived
CREATE TABLE chats_new (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    model TEXT DEFAULT 'claude-3-5-sonnet',
    temperature REAL DEFAULT 0.7,
    max_tokens INTEGER,
    auto_approve BOOLEAN DEFAULT FALSE,
    state TEXT DEFAULT 'idle' CHECK (state IN ('active', 'needs_attention', 'idle', 'archived')),
    branched_from_chat_id TEXT,
    branched_at_ordinal BIGINT,
    parent_context_sequence INTEGER,
    agent TEXT,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    workflow_name TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (branched_from_chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

-- Copy data from old table to new table
INSERT INTO chats_new (
    id, title, project_id, user_id, model, temperature, max_tokens, auto_approve,
    state, branched_from_chat_id, branched_at_ordinal, parent_context_sequence,
    agent, workflow_id, run_id, created_at, updated_at, last_active, worktree_id, workflow_name
)
SELECT 
    id, title, project_id, user_id, model, temperature, max_tokens, auto_approve,
    state, branched_from_chat_id, branched_at_ordinal, parent_context_sequence,
    agent, workflow_id, run_id, created_at, updated_at, last_active, worktree_id, workflow_name
FROM chats;

-- Drop old table and rename new table
DROP TABLE chats;
ALTER TABLE chats_new RENAME TO chats;

-- Recreate indexes
CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_state ON chats(state);
CREATE INDEX idx_chats_branched_from ON chats(branched_from_chat_id);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);

-- Create user_updates table for global workspace-level event stream
-- This is separate from chat_updates (high-volume per-chat streaming)
-- user_updates is for sidebar/workspace updates: state changes, new chats, etc.
CREATE TABLE user_updates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    user_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    
    -- Hierarchical scoping (nullable based on scope level)
    project_id TEXT,      -- NULL = user-level update
    worktree_id TEXT,     -- NULL = project-level or higher
    chat_id TEXT,         -- NULL = worktree-level or higher
    
    -- What changed
    update_type TEXT NOT NULL CHECK (update_type IN (
        -- Chat updates
        'chat_state_change',
        'chat_created',
        'chat_title_changed',
        'chat_config_changed',
        'chat_deleted',
        
        -- Project updates
        'project_created',
        'project_deleted',
        'project_settings_changed',
        
        -- Worktree updates
        'worktree_created',
        'worktree_deleted',
        'worktree_status_changed',
        
        -- Background process updates
        'process_started',
        'process_output',
        'process_completed',
        'process_failed',
        
        -- General notification
        'notification'
    )),
    
    -- What entity this update is about
    entity_type TEXT NOT NULL CHECK (entity_type IN (
        'chat',
        'project', 
        'worktree',
        'background_process',
        'system'
    )),
    entity_id TEXT NOT NULL,
    
    -- JSON payload with update-specific data
    data TEXT NOT NULL,
    
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- Foreign keys (nullable for flexibility)
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    
    -- Sequence is unique per user
    UNIQUE(user_id, sequence_number)
);

-- Primary index for polling: get updates since sequence for a user
CREATE INDEX idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);

-- Index for project-scoped queries
CREATE INDEX idx_user_updates_project ON user_updates(project_id, sequence_number DESC) 
    WHERE project_id IS NOT NULL;

-- Index for chat-scoped queries  
CREATE INDEX idx_user_updates_chat ON user_updates(chat_id) 
    WHERE chat_id IS NOT NULL;

-- Index for entity lookups
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);

-- +goose Down
DROP INDEX IF EXISTS idx_user_updates_entity;
DROP INDEX IF EXISTS idx_user_updates_chat;
DROP INDEX IF EXISTS idx_user_updates_project;
DROP INDEX IF EXISTS idx_user_updates_poll;
DROP TABLE IF EXISTS user_updates;
DROP INDEX IF EXISTS idx_chats_state;

-- SQLite doesn't support DROP COLUMN directly, need to recreate table
-- For down migration, we'll leave the column (it has a default value)
-- A proper down migration would require table recreation
