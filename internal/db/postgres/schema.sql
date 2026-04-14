CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    user_id TEXT NOT NULL,
    description TEXT,
    is_git_repo BOOLEAN NOT NULL DEFAULT TRUE,
    default_branch TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP NOT NULL,
    UNIQUE (user_id, path)
);

CREATE TABLE worktrees (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    project_id TEXT NOT NULL,
    chat_id TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP,
    is_main BOOLEAN NOT NULL DEFAULT FALSE,
    cleanup_metadata TEXT,
    UNIQUE (project_id, name)
);

CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    complexity INTEGER,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status INTEGER NOT NULL DEFAULT 1,
    position BIGINT NOT NULL,
    metadata TEXT,
    assignee TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE TABLE task_dependencies (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type)
);

CREATE TABLE settings (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    value_type TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, project_id, key)
);

CREATE TABLE chats (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state INTEGER DEFAULT 2,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP NOT NULL,
    worktree_id TEXT,
    workflow_name TEXT,
    selected_presets TEXT,
    archived_worktree_name TEXT,
    unread INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE workflows (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status INTEGER NOT NULL DEFAULT 2,
    spawned_by_node_id TEXT,
    loop_iteration BIGINT,
    created_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    worker_started_at TIMESTAMP,
    worker_stopped_at TIMESTAMP
);

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    parent_thread_id TEXT,
    fork_at_ordinal BIGINT,
    fork_at_context_window_id TEXT,
    workflow_id TEXT,
    created_at TIMESTAMP NOT NULL,
    title TEXT
);

CREATE TABLE context_windows (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    compaction_summary_message_id TEXT,
    created_at TIMESTAMP NOT NULL,
    parent_context_window_id TEXT,
    fork_at_ordinal BIGINT
);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread_id TEXT NOT NULL,
    context_window_id TEXT NOT NULL,
    role INTEGER NOT NULL,
    display_style INTEGER,
    model TEXT,
    agent TEXT,
    token_count BIGINT,
    cost_micros BIGINT,
    workflow_id TEXT,
    run_id TEXT,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position BIGINT NOT NULL,
    block_type INTEGER NOT NULL,
    content TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_call_id TEXT,
    is_error BOOLEAN,
    version BIGINT,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    workflow_run_id TEXT,
    attempt_number BIGINT,
    thought_signature TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        -- Awaiting input: pending approvals or yields
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM yields y
            WHERE y.chat_id = c.id AND y.status = 1  -- YIELD_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT

        -- Running: any workflow for this chat is running (including threads/forks)
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
        ) THEN 1  -- RUNNING

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

CREATE TABLE chat_updates (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    sequence_number BIGINT NOT NULL,
    update_type INTEGER NOT NULL,
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    approval_type INTEGER NOT NULL,
    entity_id TEXT NOT NULL,
    status INTEGER NOT NULL,
    denial_reason TEXT,
    title TEXT NOT NULL,
    metadata TEXT,
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    action_taken TEXT,
    UNIQUE (entity_id)
);

CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    size BIGINT NOT NULL,
    mime_type TEXT NOT NULL,
    file_hash TEXT,
    file_path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    attachment_type TEXT NOT NULL,
    content BYTEA
);

CREATE TABLE command_favorites (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    command_key TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, project_id, command_key)
);

CREATE TABLE yields (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    loop_node_id TEXT,
    loop_iteration BIGINT,
    status INTEGER NOT NULL DEFAULT 1,
    action_taken TEXT,
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP
);

CREATE TABLE visibility_overrides (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    is_visible BOOLEAN NOT NULL,
    created_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, item_type, slug)
);

CREATE TABLE codex_auth_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    id_token TEXT,
    account_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (user_id)
);

CREATE INDEX idx_codex_auth_tokens_user ON codex_auth_tokens(user_id);

CREATE TABLE item_defaults (
    id TEXT PRIMARY KEY,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    is_hidden BOOLEAN NOT NULL,
    reason TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (item_type, slug)
);

CREATE TABLE default_preset_assignments (
    id TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL,
    group_name TEXT NOT NULL,
    preset_slug TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (workflow_name, group_name)
);

CREATE TABLE presets (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    tag TEXT NOT NULL,
    params TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, project_id, slug)
);

CREATE TABLE step_executions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    activity_name TEXT NOT NULL,
    output_json TEXT,
    exit_code BIGINT,
    success BIGINT,
    duration_ms BIGINT,
    created_at TIMESTAMP NOT NULL,
    loop_node_id TEXT,
    loop_iteration BIGINT
);

CREATE TABLE workflow_drafts (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    is_valid BIGINT NOT NULL,
    validation_errors TEXT,
    source_path TEXT,
    forked_from TEXT,
    is_hidden BOOLEAN NOT NULL,
    chat_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    version BIGINT NOT NULL,
    UNIQUE (user_id, slug)
);

CREATE TABLE workflow_scenarios (
    id TEXT PRIMARY KEY,
    workflow_draft_id TEXT,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    events TEXT NOT NULL,
    expect TEXT,
    last_run_at TIMESTAMP,
    last_run_status TEXT,
    last_run_result TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    version BIGINT NOT NULL
);