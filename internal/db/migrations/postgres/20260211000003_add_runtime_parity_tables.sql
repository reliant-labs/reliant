-- +goose Up

-- SQLite parity tables required by runtime Postgres paths.

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    api_key TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_provider ON api_keys(user_id, provider);

CREATE TABLE IF NOT EXISTS tool_execution_requests (
    id TEXT PRIMARY KEY,

    -- Ownership & Context
    user_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    project_id TEXT,

    -- Request Data
    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    content_block_id TEXT NOT NULL,

    -- Execution Context
    context_json TEXT,
    working_dir TEXT,
    timeout_ms BIGINT,

    -- Execution State
    status TEXT NOT NULL CHECK (status IN (
        'pending',
        'executing',
        'completed',
        'failed',
        'cancelled',
        'timeout'
    )),

    daemon_id TEXT,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,

    -- Result Data
    success BOOLEAN,
    is_error BOOLEAN,
    content TEXT,
    metadata TEXT,
    error_message TEXT,
    error_code TEXT,

    -- Timestamps
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    backgrounded_at TIMESTAMP,

    -- Foreign Keys
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tool_exec_user_status ON tool_execution_requests(user_id, status);
CREATE INDEX IF NOT EXISTS idx_tool_exec_chat ON tool_execution_requests(chat_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_status_created ON tool_execution_requests(status, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_exec_tool_call ON tool_execution_requests(tool_call_id);
CREATE INDEX IF NOT EXISTS idx_tool_exec_daemon ON tool_execution_requests(daemon_id, status)
    WHERE daemon_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tool_exec_cleanup ON tool_execution_requests(status, completed_at)
    WHERE status IN ('completed', 'failed', 'cancelled', 'timeout');
CREATE INDEX IF NOT EXISTS idx_tool_exec_requests_backgrounded ON tool_execution_requests(tool_call_id, backgrounded_at);

CREATE TABLE IF NOT EXISTS background_processes (
    id TEXT PRIMARY KEY,

    -- Process identification
    pid BIGINT,
    command TEXT NOT NULL,
    working_dir TEXT NOT NULL,

    -- Association
    worktree_id TEXT,
    project_id TEXT,
    chat_id TEXT,
    user_id TEXT NOT NULL,

    -- Execution state
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'running',
        'completed',
        'failed',
        'killed',
        'killed_externally',
        'stale'
    )),
    exit_code BIGINT,

    -- Timing
    started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at TIMESTAMP,

    -- Process signature for validation
    signature TEXT,

    -- Source info
    source_type TEXT NOT NULL DEFAULT 'llm' CHECK (source_type IN (
        'llm',
        'package_command',
        'manual'
    )),
    package_type TEXT,
    command_name TEXT,

    -- Metadata
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Foreign keys
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE SET NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_bg_processes_worktree ON background_processes(worktree_id, status);
CREATE INDEX IF NOT EXISTS idx_bg_processes_user ON background_processes(user_id, status);
CREATE INDEX IF NOT EXISTS idx_bg_processes_chat ON background_processes(chat_id)
    WHERE chat_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bg_processes_running ON background_processes(status, pid)
    WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_bg_processes_cleanup ON background_processes(status, ended_at)
    WHERE status IN ('completed', 'failed', 'killed', 'killed_externally', 'stale');

CREATE TABLE IF NOT EXISTS user_updates (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    sequence_number BIGINT NOT NULL,

    -- Hierarchical scoping
    project_id TEXT,
    worktree_id TEXT,
    chat_id TEXT,

    -- What changed
    update_type TEXT NOT NULL CHECK (update_type IN (
        'chat_state_change',
        'chat_created',
        'chat_title_changed',
        'chat_config_changed',
        'chat_deleted',
        'chat_activity_changed',
        'project_created',
        'project_deleted',
        'project_settings_changed',
        'worktree_created',
        'worktree_deleted',
        'worktree_status_changed',
        'process_started',
        'process_output',
        'process_completed',
        'process_failed',
        'process_port_changed',
        'notification'
    )),

    entity_type TEXT NOT NULL CHECK (entity_type IN (
        'chat',
        'project',
        'worktree',
        'background_process',
        'system'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,

    UNIQUE(user_id, sequence_number)
);

CREATE INDEX IF NOT EXISTS idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);
CREATE INDEX IF NOT EXISTS idx_user_updates_project ON user_updates(project_id, sequence_number DESC)
    WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_updates_chat ON user_updates(chat_id)
    WHERE chat_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_updates_entity ON user_updates(entity_type, entity_id);

CREATE TABLE IF NOT EXISTS daemons (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    hostname TEXT,
    platform TEXT,
    status TEXT NOT NULL DEFAULT 'disconnected' CHECK (status IN ('active', 'idle', 'disconnected')),
    capabilities TEXT,
    project_paths TEXT,
    projects_json TEXT,
    connected_at TIMESTAMP,
    last_heartbeat TIMESTAMP,
    disconnected_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_daemons_user_id ON daemons(user_id);
CREATE INDEX IF NOT EXISTS idx_daemons_status ON daemons(status);

CREATE TABLE IF NOT EXISTS project_configs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    daemon_id TEXT NOT NULL,
    user_config_yaml TEXT,
    project_config_yaml TEXT,
    local_config_yaml TEXT,
    global_memory_md TEXT,
    project_memory_md TEXT,
    mcp_configs TEXT,
    pushed_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_project_configs_daemon_id ON project_configs(daemon_id);
CREATE INDEX IF NOT EXISTS idx_project_configs_pushed_at ON project_configs(pushed_at DESC);

-- +goose Down

DROP INDEX IF EXISTS idx_project_configs_pushed_at;
DROP INDEX IF EXISTS idx_project_configs_daemon_id;
DROP TABLE IF EXISTS project_configs;

DROP INDEX IF EXISTS idx_daemons_status;
DROP INDEX IF EXISTS idx_daemons_user_id;
DROP TABLE IF EXISTS daemons;

DROP INDEX IF EXISTS idx_user_updates_entity;
DROP INDEX IF EXISTS idx_user_updates_chat;
DROP INDEX IF EXISTS idx_user_updates_project;
DROP INDEX IF EXISTS idx_user_updates_poll;
DROP TABLE IF EXISTS user_updates;

DROP INDEX IF EXISTS idx_bg_processes_cleanup;
DROP INDEX IF EXISTS idx_bg_processes_running;
DROP INDEX IF EXISTS idx_bg_processes_chat;
DROP INDEX IF EXISTS idx_bg_processes_user;
DROP INDEX IF EXISTS idx_bg_processes_worktree;
DROP TABLE IF EXISTS background_processes;

DROP INDEX IF EXISTS idx_tool_exec_requests_backgrounded;
DROP INDEX IF EXISTS idx_tool_exec_cleanup;
DROP INDEX IF EXISTS idx_tool_exec_daemon;
DROP INDEX IF EXISTS idx_tool_exec_tool_call;
DROP INDEX IF EXISTS idx_tool_exec_status_created;
DROP INDEX IF EXISTS idx_tool_exec_chat;
DROP INDEX IF EXISTS idx_tool_exec_user_status;
DROP TABLE IF EXISTS tool_execution_requests;

DROP INDEX IF EXISTS idx_api_keys_provider;
DROP INDEX IF EXISTS idx_api_keys_user;
DROP TABLE IF EXISTS api_keys;
