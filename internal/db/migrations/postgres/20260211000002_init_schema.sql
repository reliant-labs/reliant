-- +goose Up

CREATE TABLE IF NOT EXISTS projects (
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

CREATE TABLE IF NOT EXISTS worktrees (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    project_id TEXT NOT NULL,
    chat_id TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP,
    is_main BOOLEAN NOT NULL DEFAULT FALSE,
    cleanup_metadata TEXT,
    UNIQUE (project_id, name)
);

CREATE TABLE IF NOT EXISTS plans (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    parent_task_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL,
    position BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
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

CREATE TABLE IF NOT EXISTS chats (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    project_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state TEXT,
    workflow_id TEXT,
    run_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP NOT NULL,
    worktree_id TEXT,
    workflow_name TEXT,
    cancelled_at TIMESTAMP,
    selected_presets TEXT,
    archived_worktree_name TEXT
);

CREATE TABLE IF NOT EXISTS workflows (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    chat_id TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    thread TEXT NOT NULL,
    status TEXT NOT NULL,
    spawned_by_node_id TEXT,
    loop_iteration BIGINT,
    created_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    paused_at TIMESTAMP,
    worker_started_at TIMESTAMP,
    worker_stopped_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    parent_thread_id TEXT,
    fork_at_ordinal BIGINT,
    fork_at_context_window_id TEXT,
    workflow_id TEXT,
    created_at TIMESTAMP NOT NULL,
    title TEXT
);

CREATE TABLE IF NOT EXISTS context_windows (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    compaction_summary_message_id TEXT,
    created_at TIMESTAMP NOT NULL,
    parent_context_window_id TEXT,
    fork_at_ordinal BIGINT
);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    thread_id TEXT NOT NULL,
    context_window_id TEXT NOT NULL,
    role TEXT NOT NULL,
    display_style TEXT,
    model TEXT,
    agent TEXT,
    token_count BIGINT,
    cost REAL,
    workflow_id TEXT,
    run_id TEXT,
    node_id TEXT,
    node_path TEXT,
    activity_id TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS message_content_blocks (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    position BIGINT NOT NULL,
    block_type TEXT NOT NULL,
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

CREATE TABLE IF NOT EXISTS chat_updates (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    sequence_number BIGINT NOT NULL,
    update_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    approval_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    status TEXT NOT NULL,
    denial_reason TEXT,
    title TEXT NOT NULL,
    description TEXT,
    actions TEXT,
    metadata TEXT,
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    action_taken TEXT,
    UNIQUE (entity_id)
);

CREATE TABLE IF NOT EXISTS attachments (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    size BIGINT NOT NULL,
    mime_type TEXT NOT NULL,
    file_hash TEXT,
    file_path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    attachment_type TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS command_favorites (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    command_key TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, project_id, command_key)
);

CREATE TABLE IF NOT EXISTS visibility_overrides (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_type TEXT NOT NULL,
    slug TEXT NOT NULL,
    is_visible BOOLEAN NOT NULL,
    created_at TIMESTAMP NOT NULL,
    UNIQUE (user_id, item_type, slug)
);

CREATE TABLE IF NOT EXISTS item_defaults (
    id TEXT PRIMARY KEY,
    item_type TEXT NOT NULL,
    slug TEXT NOT NULL,
    is_hidden BOOLEAN NOT NULL,
    reason TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (item_type, slug)
);

CREATE TABLE IF NOT EXISTS default_preset_assignments (
    id TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL,
    group_name TEXT NOT NULL,
    preset_slug TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (workflow_name, group_name)
);

CREATE TABLE IF NOT EXISTS presets (
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

CREATE TABLE IF NOT EXISTS step_executions (
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

CREATE TABLE IF NOT EXISTS workflow_drafts (
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

CREATE TABLE IF NOT EXISTS workflow_scenarios (
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

-- +goose Down

DROP TABLE IF EXISTS workflow_scenarios;
DROP TABLE IF EXISTS workflow_drafts;
DROP TABLE IF EXISTS step_executions;
DROP TABLE IF EXISTS presets;
DROP TABLE IF EXISTS default_preset_assignments;
DROP TABLE IF EXISTS item_defaults;
DROP TABLE IF EXISTS visibility_overrides;
DROP TABLE IF EXISTS command_favorites;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS chat_updates;
DROP TABLE IF EXISTS message_content_blocks;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS context_windows;
DROP TABLE IF EXISTS threads;
DROP TABLE IF EXISTS workflows;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS plans;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS projects;