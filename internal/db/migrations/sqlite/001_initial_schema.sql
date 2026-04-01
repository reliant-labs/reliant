-- +goose Up
-- V2 Minimal Database Schema (Temporal-First Architecture)
-- Focus: Store queryable data, let Temporal handle execution

-- =============================================================================
-- CORE DATA (What users query)
-- =============================================================================

-- Chats: Top-level conversations
CREATE TABLE chats (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT,

    -- Settings
    model TEXT DEFAULT 'claude-3-5-sonnet',
    temperature REAL DEFAULT 0.7,
    max_tokens INTEGER,
    auto_approve BOOLEAN NOT NULL DEFAULT FALSE,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,

    -- Branching (simple: duplicate chat)
    branched_from_chat_id TEXT,
    branched_at_ordinal BIGINT,

    -- Link to Temporal workflow
    workflow_id TEXT, -- Main ChatWorkflow ID
    run_id TEXT,

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (branched_from_chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_is_archived ON chats(is_archived);
CREATE INDEX idx_chats_branched_from ON chats(branched_from_chat_id);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);

-- Messages: Conversation messages (simplified)
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL, -- Microsecond timestamp for deterministic ordering

    -- Execution context (no foreign keys - just strings)
    thread TEXT NOT NULL DEFAULT '0', -- '0' for main thread, tool_call_id for child agents
    context_sequence INTEGER NOT NULL DEFAULT 0, -- 0, 1, 2... (increments on compaction)

    -- Message content
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    model TEXT,
    agent TEXT,

    -- Streaming support
    streaming_state TEXT NOT NULL DEFAULT 'complete'
        CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_completed_at DATETIME,

    -- Token tracking
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_creation_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,

    -- Link to Temporal workflow (for traceability)
    workflow_id TEXT,
    run_id TEXT,

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, ordinal)
);

CREATE INDEX idx_messages_chat_ordinal ON messages(chat_id, ordinal);
CREATE INDEX idx_messages_thread ON messages(chat_id, thread, ordinal);
CREATE INDEX idx_messages_context ON messages(chat_id, thread, context_sequence, ordinal);
CREATE INDEX idx_messages_streaming ON messages(streaming_state, created_at)
    WHERE streaming_state = 'streaming';

-- Message Content Blocks: Granular content (replaces JSON parts)
CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL,

    block_type TEXT NOT NULL CHECK (block_type IN (
        'text',         -- Plain text
        'thinking',     -- Claude thinking blocks
        'tool_call',    -- Tool invocation
        'tool_result',  -- Tool execution result
        'image',        -- Image attachment
        'file'          -- File attachment
    )),

    -- Content
    content TEXT,

    -- Tool call specific
    tool_name TEXT,
    tool_input TEXT, -- JSON
    tool_call_id TEXT, -- SDK-provided ID

    -- Tool result specific
    is_error BOOLEAN DEFAULT FALSE,

    -- Streaming support
    streaming_state TEXT NOT NULL DEFAULT 'complete'
        CHECK (streaming_state IN ('streaming', 'complete', 'failed')),
    streaming_started_at DATETIME,
    streaming_completed_at DATETIME,
    version INTEGER DEFAULT 1, -- For optimistic locking during streaming

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
    UNIQUE(message_id, position)
);

CREATE INDEX idx_content_blocks_message ON message_content_blocks(message_id, position);
CREATE INDEX idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id)
    WHERE tool_call_id IS NOT NULL;
CREATE INDEX idx_content_blocks_streaming ON message_content_blocks(streaming_state, streaming_started_at)
    WHERE streaming_state = 'streaming';

-- =============================================================================
-- PROJECTS & WORKTREES
-- =============================================================================

-- Projects: Code repositories
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    description TEXT,
    is_git_repo BOOLEAN NOT NULL DEFAULT TRUE,
    default_branch TEXT DEFAULT 'main',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_projects_path ON projects(path);
CREATE INDEX idx_projects_last_active ON projects(last_active DESC);

-- Worktrees: Git worktrees
CREATE TABLE worktrees (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    project_id TEXT NOT NULL,
    chat_id TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',
        'completed',
        'abandoned'
    )),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE SET NULL,
    UNIQUE(project_id, name)
);

CREATE INDEX idx_worktrees_project ON worktrees(project_id);
CREATE INDEX idx_worktrees_chat ON worktrees(chat_id);
CREATE INDEX idx_worktrees_status ON worktrees(status);

-- =============================================================================
-- PLANS & TASKS
-- =============================================================================

-- Plans: High-level project plans
CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending',
        'in_progress',
        'completed',
        'cancelled'
    )),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE INDEX idx_plans_chat ON plans(chat_id);
CREATE INDEX idx_plans_status ON plans(status);

-- Tasks: Task items
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending',
        'in_progress',
        'completed',
        'failed',
        'blocked',
        'cancelled'
    )),
    position INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE INDEX idx_tasks_plan ON tasks(plan_id, position);
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id);
CREATE INDEX idx_tasks_status ON tasks(status);

-- =============================================================================
-- SETTINGS
-- =============================================================================

-- Settings: User and project settings (key-value pairs)
-- For complex settings, use multiple rows instead of JSON
CREATE TABLE settings (
    id TEXT PRIMARY KEY,
    project_id TEXT,  -- NULL for global settings
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    value_type TEXT NOT NULL CHECK (value_type IN ('string', 'int', 'float', 'bool')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(project_id, key)
);

CREATE INDEX idx_settings_project ON settings(project_id);
CREATE INDEX idx_settings_key ON settings(key);

-- =============================================================================
-- OPTIONAL: STATE CACHE FOR PERFORMANCE
-- =============================================================================

-- Chat State Cache: Derived from Temporal workflow state
-- Updated by workflow activities for faster queries
CREATE TABLE chat_state_cache (
    chat_id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    run_id TEXT,

    -- Current execution state (cached from Temporal)
    current_thread TEXT NOT NULL DEFAULT '0',
    current_context_sequence INTEGER NOT NULL DEFAULT 0,

    -- Status
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',
        'waiting',      -- Waiting for approval/input
        'completed',
        'failed'
    )),

    -- Timestamps
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_message_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE INDEX idx_chat_state_workflow ON chat_state_cache(workflow_id);
CREATE INDEX idx_chat_state_status ON chat_state_cache(status);

-- Active Threads: Cache of active sub-agent threads
-- Replaces JSON in chat_state_cache
CREATE TABLE active_threads (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    thread TEXT NOT NULL,

    -- Workflow tracking
    workflow_id TEXT NOT NULL,
    run_id TEXT,

    -- Thread info
    agent_name TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN (
        'active',
        'completed',
        'failed',
        'cancelled'
    )),

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, thread)
);

CREATE INDEX idx_active_threads_chat ON active_threads(chat_id, status);
CREATE INDEX idx_active_threads_workflow ON active_threads(workflow_id);

-- =============================================================================

-- +goose Down
DROP TABLE IF EXISTS active_threads;
DROP TABLE IF EXISTS chat_state_cache;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS plans;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS message_content_blocks;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS chats;
