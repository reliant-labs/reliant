CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE sqlite_sequence(name,seq);
CREATE TABLE worktrees (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    project_id TEXT NOT NULL,
    chat_id TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME, is_main BOOLEAN NOT NULL DEFAULT FALSE, cleanup_metadata TEXT,

    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE SET NULL,
    UNIQUE(project_id, name)
);
CREATE INDEX idx_worktrees_project ON worktrees(project_id);
CREATE INDEX idx_worktrees_chat ON worktrees(chat_id);
CREATE INDEX idx_worktrees_status ON worktrees(status);
CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    complexity INTEGER,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE
);
CREATE INDEX idx_plans_thread ON plans(thread_id);
CREATE INDEX idx_plans_status ON plans(status);
CREATE INDEX idx_plans_project ON plans(project_id) WHERE project_id IS NOT NULL;
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    position INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    metadata TEXT,
    assignee TEXT,

    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_tasks_plan ON tasks(plan_id, position);
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE TABLE task_dependencies (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type)
);
CREATE INDEX idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX idx_task_deps_to ON task_dependencies(to_task_id);
CREATE INDEX idx_task_deps_type ON task_dependencies(dependency_type);
CREATE TABLE IF NOT EXISTS "settings" (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL DEFAULT '4630f1da-5058-4707-804b-aa30362f3e40',
    project_id TEXT,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    value_type TEXT NOT NULL CHECK (value_type IN ('string', 'int', 'float', 'bool')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    UNIQUE(user_id, project_id, key)
);
CREATE INDEX idx_settings_user_id ON settings(user_id);
CREATE INDEX idx_settings_project ON settings(project_id);
CREATE INDEX idx_settings_key ON settings(key);
CREATE TABLE IF NOT EXISTS "projects" (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    user_id TEXT NOT NULL,
    description TEXT,
    is_git_repo BOOLEAN NOT NULL DEFAULT TRUE,
    default_branch TEXT DEFAULT 'main',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, path)
);
CREATE INDEX idx_projects_user_id ON projects(user_id);
CREATE INDEX idx_projects_last_active ON projects(last_active DESC);
CREATE INDEX idx_worktrees_is_main ON worktrees(project_id, is_main) WHERE is_main = TRUE;
CREATE UNIQUE INDEX idx_one_main_per_project ON worktrees(project_id) WHERE is_main = TRUE;
CREATE TABLE IF NOT EXISTS "approvals" (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,

    approval_type INTEGER NOT NULL,

    entity_id TEXT NOT NULL UNIQUE,

    status INTEGER NOT NULL,
    denial_reason TEXT,

    -- Display fields
    title TEXT NOT NULL DEFAULT '',

    -- Optional fields (JSON for flexibility)
    metadata TEXT, -- JSON object with type-specific metadata

    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME, action_taken TEXT,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
);
CREATE INDEX idx_approvals_chat_status ON approvals(chat_id, status);
CREATE INDEX idx_approvals_entity ON approvals(entity_id);
CREATE INDEX idx_approvals_type ON approvals(approval_type, status);
CREATE INDEX idx_approvals_created ON approvals(created_at DESC);
CREATE TABLE background_processes (
    id TEXT PRIMARY KEY,
    
    -- Process identification
    pid INTEGER,                    -- OS process ID (for validation on restart)
    command TEXT NOT NULL,          -- Full command that was executed
    working_dir TEXT NOT NULL,      -- Directory where command runs
    
    -- Association (primarily worktree-based per requirements)
    worktree_id TEXT,               -- Worktree this process belongs to
    project_id TEXT,                -- Project for organizational purposes
    chat_id TEXT,                   -- Optional: if spawned by LLM
    user_id TEXT NOT NULL,          -- Owner of the process
    
    -- Execution state
    status INTEGER NOT NULL DEFAULT 1,
    exit_code INTEGER,              -- Exit code when completed
    
    -- Timing
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at DATETIME,
    
    -- Process signature for validation (command hash + start time)
    -- Used to verify PID still belongs to our process after restart
    signature TEXT,
    
    -- Source info (package command vs LLM-triggered)
    source_type TEXT NOT NULL DEFAULT 'llm' CHECK (source_type IN ('llm', 'package_command', 'manual')),
    package_type INTEGER,              -- If source_type is package_command: makefile, npm, taskfile
    command_name TEXT,              -- If source_type is package_command: target/script name
    
    -- Metadata
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- Foreign keys
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE SET NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE SET NULL
);
CREATE INDEX idx_bg_processes_worktree ON background_processes(worktree_id, status);
CREATE INDEX idx_bg_processes_user ON background_processes(user_id, status);
CREATE INDEX idx_bg_processes_chat ON background_processes(chat_id) 
    WHERE chat_id IS NOT NULL;
CREATE INDEX idx_bg_processes_running ON background_processes(status, pid) 
    WHERE status = 1;
CREATE INDEX idx_bg_processes_cleanup ON background_processes(status, ended_at) 
    WHERE status IN (2, 3, 4, 5, 6);
CREATE TABLE attachments (
    id TEXT PRIMARY KEY,              -- Attachment UUID
    user_id TEXT NOT NULL,            -- User who uploaded the attachment
    filename TEXT NOT NULL,           -- Original filename
    size INTEGER NOT NULL,            -- File size in bytes
    mime_type TEXT NOT NULL,          -- MIME type (e.g., image/jpeg, application/pdf)
    file_hash TEXT,                   -- SHA-256 hash of file content
    file_path TEXT NOT NULL,          -- Relative path to file in uploads directory
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
, attachment_type TEXT NOT NULL DEFAULT 'image'
    CHECK (attachment_type IN ('image', 'file_reference')), content BLOB);
CREATE INDEX idx_attachments_user_id ON attachments(user_id);
CREATE INDEX idx_attachments_file_hash ON attachments(file_hash);
CREATE TRIGGER update_attachments_timestamp 
    AFTER UPDATE ON attachments
    FOR EACH ROW
BEGIN
    UPDATE attachments SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;
CREATE TABLE step_executions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    step_id TEXT NOT NULL,              -- The step ID from workflow definition (e.g., "run_tests", "call_llm")
    activity_name TEXT NOT NULL,        -- The activity type name (e.g., "V2_ExecuteRunStep")
    output_json TEXT,                   -- Full JSON output from the activity
    exit_code INTEGER,                  -- Denormalized for fast queries (nullable for non-run steps)
    success INTEGER,                    -- Boolean: 1 for success, 0 for failure, NULL for unknown
    duration_ms INTEGER,                -- Execution duration in milliseconds
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, loop_node_id TEXT, loop_iteration INTEGER,
    
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);
CREATE INDEX idx_step_executions_lookup 
    ON step_executions(workflow_id, step_id, created_at);
CREATE INDEX idx_step_executions_workflow
    ON step_executions(workflow_id, created_at);
CREATE INDEX idx_step_executions_loop 
    ON step_executions(workflow_id, loop_node_id, loop_iteration) 
    WHERE loop_node_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS "user_updates" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    user_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    
    -- Hierarchical scoping (nullable based on scope level)
    project_id TEXT,      -- NULL = user-level update
    worktree_id TEXT,     -- NULL = project-level or higher
    chat_id TEXT,         -- NULL = worktree-level or higher
    
    -- What changed
    update_type INTEGER NOT NULL,
    entity_type INTEGER NOT NULL,
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
CREATE INDEX idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);
CREATE INDEX idx_user_updates_project ON user_updates(project_id, sequence_number DESC) 
    WHERE project_id IS NOT NULL;
CREATE INDEX idx_user_updates_chat ON user_updates(chat_id) 
    WHERE chat_id IS NOT NULL;
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);
CREATE TABLE IF NOT EXISTS "chats" (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state INTEGER DEFAULT 2,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    workflow_name TEXT,
    selected_presets TEXT, archived_worktree_name TEXT,
    unread INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE INDEX idx_chats_project ON chats(project_id);
CREATE INDEX idx_chats_user_id ON chats(user_id);
CREATE INDEX idx_chats_last_active ON chats(last_active DESC);
CREATE INDEX idx_chats_state ON chats(state);
CREATE INDEX idx_chats_workflow ON chats(workflow_id);
CREATE TABLE IF NOT EXISTS "message_content_blocks" (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    block_type INTEGER NOT NULL,
    content TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,
    version INTEGER,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number INTEGER DEFAULT 1,
    thought_signature TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);
CREATE INDEX idx_content_blocks_message_id ON message_content_blocks(message_id);
CREATE INDEX idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id);
CREATE INDEX idx_content_blocks_activity ON message_content_blocks(activity_id, workflow_run_id);
CREATE TABLE presets (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    -- NULL = global (user-wide), non-null = project-specific
    project_id TEXT,
    -- Display name for the preset
    name TEXT NOT NULL,
    -- URL-safe identifier for runtime reference
    slug TEXT NOT NULL,
    -- Human-readable description
    description TEXT,
    -- Tag declaring which workflow/group inputs this preset targets
    tag TEXT NOT NULL,
    -- JSON object of parameter name -> value
    params TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    -- Unique slug per user per scope (global or project)
    UNIQUE(user_id, project_id, slug)
);
CREATE INDEX idx_presets_user_id ON presets(user_id);
CREATE INDEX idx_presets_project ON presets(project_id);
CREATE INDEX idx_presets_tag ON presets(tag);
CREATE INDEX idx_presets_slug ON presets(user_id, slug);
CREATE INDEX idx_chats_archived_worktree_name ON chats(archived_worktree_name);
CREATE TABLE visibility_overrides (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    is_visible BOOLEAN NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, item_type, slug)
);
CREATE INDEX idx_visibility_overrides_user_id ON visibility_overrides(user_id);
CREATE INDEX idx_visibility_overrides_type ON visibility_overrides(user_id, item_type);
CREATE TABLE item_defaults (
    id TEXT PRIMARY KEY,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    -- true = hidden by default (specialist presets, internal workflows)
    is_hidden BOOLEAN NOT NULL DEFAULT FALSE,
    -- Human-readable reason for the default (optional, for documentation)
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Each item can only have one default
    UNIQUE(item_type, slug)
);
CREATE INDEX idx_item_defaults_type ON item_defaults(item_type);
CREATE INDEX idx_item_defaults_hidden ON item_defaults(item_type, is_hidden);
CREATE TABLE default_preset_assignments (
    id TEXT PRIMARY KEY,
    -- The workflow name (e.g., 'builtin://agent')
    workflow_name TEXT NOT NULL,
    -- The group name (empty string "" for top-level/workflow-level inputs)
    group_name TEXT NOT NULL DEFAULT '',
    -- The preset slug to use as default
    preset_slug TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workflow_name, group_name)
);
CREATE INDEX idx_default_preset_assignments_workflow ON default_preset_assignments(workflow_name);
CREATE TABLE workflow_scenarios (
    id TEXT PRIMARY KEY,
    workflow_draft_id TEXT,                -- FK to workflow_drafts (NULL if testing raw YAML)
    user_id TEXT NOT NULL,                 -- Owner of the scenario
    name TEXT NOT NULL,                    -- Human-readable scenario name
    description TEXT,                      -- What this scenario tests
    events TEXT NOT NULL,                  -- JSON array of event objects
    expect TEXT,                           -- JSON object with expected outcome
    
    -- Cached results from last run
    last_run_at DATETIME,
    last_run_status TEXT CHECK (last_run_status IN ('passed', 'failed', 'error')),
    last_run_result TEXT,                  -- JSON object with execution details
    
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, version INTEGER NOT NULL DEFAULT 1,
    
    FOREIGN KEY (workflow_draft_id) REFERENCES workflow_drafts(id) ON DELETE CASCADE
);
CREATE INDEX idx_workflow_scenarios_draft ON workflow_scenarios(workflow_draft_id);
CREATE INDEX idx_workflow_scenarios_user ON workflow_scenarios(user_id);
CREATE TRIGGER update_workflow_scenarios_timestamp 
AFTER UPDATE ON workflow_scenarios 
BEGIN 
    UPDATE workflow_scenarios 
    SET updated_at = datetime('now', 'utc') 
    WHERE id = NEW.id; 
END;
CREATE TABLE IF NOT EXISTS "workflow_drafts" (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    is_valid INTEGER NOT NULL DEFAULT 0,
    validation_errors TEXT,
    source_path TEXT,
    forked_from TEXT,
    is_hidden BOOLEAN NOT NULL DEFAULT 0,
    chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, version INTEGER NOT NULL DEFAULT 1);
CREATE UNIQUE INDEX idx_workflow_drafts_unique_slug ON workflow_drafts(user_id, slug);
CREATE INDEX idx_workflow_drafts_user ON workflow_drafts(user_id);
CREATE INDEX idx_workflow_drafts_slug ON workflow_drafts(slug);
CREATE INDEX idx_workflow_drafts_chat ON workflow_drafts(chat_id);
CREATE INDEX idx_workflow_drafts_forked_from ON workflow_drafts(forked_from);
CREATE INDEX idx_workflow_drafts_is_hidden ON workflow_drafts(is_hidden);
CREATE TRIGGER update_workflow_drafts_timestamp
AFTER UPDATE ON workflow_drafts
BEGIN
    UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id;
END;
CREATE TABLE command_favorites (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    -- The command key in format: {package_type}:{name} or {package_type}:{relative_path}:{name}
    -- Examples: 'npm:dev', 'makefile:build', 'npm:electron:start'
    command_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    -- Each user can only favorite a command once per project
    UNIQUE(user_id, project_id, command_key)
);
CREATE INDEX idx_command_favorites_user_project ON command_favorites(user_id, project_id);
CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    api_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, provider)
);
CREATE INDEX idx_api_keys_user ON api_keys(user_id);
CREATE INDEX idx_api_keys_provider ON api_keys(user_id, provider);
CREATE TABLE codex_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    id_token TEXT,
    account_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id)
);
CREATE INDEX idx_codex_auth_tokens_user ON codex_auth_tokens(user_id);
CREATE TABLE threads (
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
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, title TEXT,

    FOREIGN KEY (conversation_id) REFERENCES chats(id) ON DELETE CASCADE,
    -- Note: parent_thread_id is NOT a FK because it can point cross-conversation
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE SET NULL
);
CREATE INDEX idx_threads_conversation ON threads(conversation_id);
CREATE INDEX idx_threads_parent ON threads(parent_thread_id) WHERE parent_thread_id IS NOT NULL;
CREATE INDEX idx_threads_workflow ON threads(workflow_id) WHERE workflow_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS "workflows" (
    id TEXT PRIMARY KEY NOT NULL,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    spawned_by_node_id TEXT,
    loop_iteration INTEGER,
    created_at DATETIME NOT NULL,
    completed_at DATETIME,
    expired_at DATETIME,
    status INTEGER NOT NULL DEFAULT 2,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES workflows(id) ON DELETE SET NULL
);
CREATE INDEX idx_workflows_parent ON workflows(parent_id);
CREATE INDEX idx_workflows_chat ON workflows(chat_id);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_workflows_thread ON workflows(chat_id, thread);
CREATE INDEX idx_workflows_spawned_by_node ON workflows(parent_id, spawned_by_node_id) WHERE spawned_by_node_id IS NOT NULL;
CREATE INDEX idx_workflows_loop_iteration ON workflows(parent_id, spawned_by_node_id, loop_iteration) WHERE loop_iteration IS NOT NULL;
CREATE INDEX idx_workflows_paused ON workflows(status) WHERE status = 6;
CREATE TABLE IF NOT EXISTS "context_windows" (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence INTEGER NOT NULL DEFAULT 0,
    compaction_summary_message_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, parent_context_window_id TEXT REFERENCES context_windows(id) ON DELETE SET NULL, fork_at_ordinal INTEGER,
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
    UNIQUE(thread_id, sequence)
);
CREATE INDEX idx_context_windows_thread ON context_windows(thread_id, sequence DESC);
CREATE INDEX idx_context_windows_parent ON context_windows(parent_context_window_id) 
    WHERE parent_context_window_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS "chat_updates" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type INTEGER NOT NULL,
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);
CREATE TRIGGER chat_updates_chat_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.state IS NOT NEW.state OR
    OLD.title IS NOT NEW.title
)
BEGIN
    INSERT INTO chat_updates (chat_id, sequence_number, update_type, entity_id, data, created_at)
    VALUES (
        NEW.id,
        COALESCE((SELECT MAX(sequence_number) + 1 FROM chat_updates WHERE chat_id = NEW.id), 1),
        7,
        NEW.id,
        json_object(
            'update_type', 7,
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
CREATE TABLE IF NOT EXISTS "messages" (
    id TEXT PRIMARY KEY NOT NULL,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    thread_id TEXT NOT NULL,
    context_window_id TEXT NOT NULL,
    role INTEGER NOT NULL,
    display_style INTEGER,
    model TEXT,
    agent TEXT,
    token_count INTEGER,
    cost REAL,
    workflow_id TEXT,
    run_id TEXT,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    created_at DATETIME DEFAULT (datetime('now', 'utc')) NOT NULL,
    updated_at DATETIME DEFAULT (datetime('now', 'utc')) NOT NULL
);
CREATE INDEX idx_messages_chat_id ON messages(chat_id);
CREATE INDEX idx_messages_thread_id ON messages(thread_id);
CREATE INDEX idx_messages_context_window_id ON messages(context_window_id);
CREATE INDEX idx_messages_ordinal ON messages(ordinal);
CREATE UNIQUE INDEX idx_messages_chat_thread_ordinal ON messages(chat_id, thread_id, ordinal);
CREATE UNIQUE INDEX idx_workflow_drafts_user_name_unique 
ON workflow_drafts (user_id, name COLLATE NOCASE);
CREATE TABLE daemons (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    hostname TEXT,
    platform TEXT,
    status TEXT NOT NULL DEFAULT 'disconnected' CHECK (status IN ('active', 'idle', 'disconnected')),
    capabilities TEXT,   -- JSON array
    project_paths TEXT,  -- JSON array of paths requiring config
    projects_json TEXT,  -- JSON array of discovered projects
    connected_at DATETIME,
    last_heartbeat DATETIME,
    disconnected_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_daemons_user_id ON daemons(user_id);
CREATE INDEX idx_daemons_status ON daemons(status);
CREATE TABLE project_configs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    daemon_id TEXT NOT NULL,
    user_config_yaml TEXT,
    project_config_yaml TEXT,
    local_config_yaml TEXT,
    global_memory_md TEXT,
    project_memory_md TEXT,
    mcp_configs TEXT, -- JSON object: scope -> mcp.json content
    pushed_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE INDEX idx_project_configs_daemon_id ON project_configs(daemon_id);
CREATE INDEX idx_project_configs_pushed_at ON project_configs(pushed_at DESC);
CREATE TABLE yields (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    loop_node_id TEXT,
    loop_iteration INTEGER,
    status INTEGER NOT NULL DEFAULT 1,
    action_taken TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME
);
CREATE INDEX idx_yields_chat_id_status ON yields(chat_id, status);
CREATE INDEX idx_yields_workflow_step ON yields(workflow_id, step_id, status);

-- Keep in sync with internal/db/schema.sql and migrations.
-- Used by sqlc queries in internal/db/sqlite/queries/chats.sql
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