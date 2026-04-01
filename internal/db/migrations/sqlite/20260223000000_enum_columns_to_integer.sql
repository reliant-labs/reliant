-- +goose Up
-- Migrate TEXT enum columns to INTEGER using proto enum numeric values.
-- Proto enum value 0 is always UNSPECIFIED; real values start at 1.
-- Uses ALTER TABLE ADD/DROP/RENAME COLUMN where possible.
-- Tables with UNIQUE constraints on the converted column use table recreation.

-- ============================================================================
-- Step 0: Drop triggers that reference columns being converted
-- ============================================================================
DROP TRIGGER IF EXISTS chat_updates_chat_update;
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- ============================================================================
-- 1. worktrees.status: WorktreeStatus
--    active=1, completed=2, abandoned=3, merging=4
-- ============================================================================
DROP INDEX IF EXISTS idx_worktrees_status;
ALTER TABLE worktrees ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE worktrees SET status_new = CASE status
    WHEN 'active' THEN 1
    WHEN 'completed' THEN 2
    WHEN 'abandoned' THEN 3
    WHEN 'merging' THEN 4
    ELSE 0
END;
ALTER TABLE worktrees DROP COLUMN status;
ALTER TABLE worktrees RENAME COLUMN status_new TO status;
CREATE INDEX idx_worktrees_status ON worktrees(status);

-- ============================================================================
-- 2. plans.status: PlanStatus  (pending=1, in_progress=2, completed=3, cancelled=4)
--    plans.complexity: PlanComplexity (simple=1, moderate=2, complex=3) [nullable]
-- ============================================================================
DROP INDEX IF EXISTS idx_plans_status;
ALTER TABLE plans ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE plans SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'in_progress' THEN 2
    WHEN 'completed' THEN 3
    WHEN 'cancelled' THEN 4
    ELSE 0
END;
ALTER TABLE plans DROP COLUMN status;
ALTER TABLE plans RENAME COLUMN status_new TO status;
CREATE INDEX idx_plans_status ON plans(status);

ALTER TABLE plans ADD COLUMN complexity_new INTEGER;
UPDATE plans SET complexity_new = CASE complexity
    WHEN 'simple' THEN 1
    WHEN 'moderate' THEN 2
    WHEN 'complex' THEN 3
    ELSE NULL
END;
ALTER TABLE plans DROP COLUMN complexity;
ALTER TABLE plans RENAME COLUMN complexity_new TO complexity;

-- ============================================================================
-- 3. tasks.status: TaskStatus
--    pending=1, in_progress=2, completed=3, failed=4, blocked=5, cancelled=6, skipped=7
-- ============================================================================
DROP INDEX IF EXISTS idx_tasks_status;
ALTER TABLE tasks ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE tasks SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'in_progress' THEN 2
    WHEN 'completed' THEN 3
    WHEN 'failed' THEN 4
    WHEN 'blocked' THEN 5
    WHEN 'cancelled' THEN 6
    WHEN 'skipped' THEN 7
    ELSE 0
END;
ALTER TABLE tasks DROP COLUMN status;
ALTER TABLE tasks RENAME COLUMN status_new TO status;
CREATE INDEX idx_tasks_status ON tasks(status);

-- ============================================================================
-- 4. task_dependencies.dependency_type: DependencyType
--    blocks=1, related=2, parallel_with=3
--    Table recreation required: UNIQUE(from_task_id, to_task_id, dependency_type)
-- ============================================================================
CREATE TABLE task_dependencies_new (
    id TEXT PRIMARY KEY,
    from_task_id TEXT NOT NULL,
    to_task_id TEXT NOT NULL,
    dependency_type INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (from_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (to_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(from_task_id, to_task_id, dependency_type)
);
INSERT INTO task_dependencies_new SELECT
    id, from_task_id, to_task_id,
    CASE dependency_type
        WHEN 'blocks' THEN 1
        WHEN 'related' THEN 2
        WHEN 'parallel_with' THEN 3
        ELSE 0
    END,
    created_at
FROM task_dependencies;
DROP TABLE task_dependencies;
ALTER TABLE task_dependencies_new RENAME TO task_dependencies;
CREATE INDEX idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX idx_task_deps_to ON task_dependencies(to_task_id);
CREATE INDEX idx_task_deps_type ON task_dependencies(dependency_type);

-- ============================================================================
-- 5. approvals: approval_type (ApprovalType), status (ApprovalStatus)
--    approval_type: tool=1, workflow_step=2
--    status: pending=1, approved=2, denied=3, cancelled=4
-- ============================================================================
DROP INDEX IF EXISTS idx_approvals_chat_status;
DROP INDEX IF EXISTS idx_approvals_type;
ALTER TABLE approvals ADD COLUMN approval_type_new INTEGER NOT NULL DEFAULT 0;
UPDATE approvals SET approval_type_new = CASE approval_type
    WHEN 'tool' THEN 1
    WHEN 'workflow_step' THEN 2
    ELSE 0
END;
ALTER TABLE approvals DROP COLUMN approval_type;
ALTER TABLE approvals RENAME COLUMN approval_type_new TO approval_type;

ALTER TABLE approvals ADD COLUMN status_new INTEGER NOT NULL DEFAULT 0;
UPDATE approvals SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'approved' THEN 2
    WHEN 'denied' THEN 3
    WHEN 'cancelled' THEN 4
    ELSE 0
END;
ALTER TABLE approvals DROP COLUMN status;
ALTER TABLE approvals RENAME COLUMN status_new TO status;
CREATE INDEX idx_approvals_chat_status ON approvals(chat_id, status);
CREATE INDEX idx_approvals_type ON approvals(approval_type, status);

-- ============================================================================
-- 6. background_processes: status (BackgroundProcessStatus), package_type (PackageType nullable)
--    status: running=1, completed=2, failed=3, killed=4, killed_externally=5, stale=6
--    package_type: makefile=1, npm=2, taskfile=3 [nullable]
-- ============================================================================
DROP INDEX IF EXISTS idx_bg_processes_worktree;
DROP INDEX IF EXISTS idx_bg_processes_user;
DROP INDEX IF EXISTS idx_bg_processes_running;
DROP INDEX IF EXISTS idx_bg_processes_cleanup;
ALTER TABLE background_processes ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE background_processes SET status_new = CASE status
    WHEN 'running' THEN 1
    WHEN 'completed' THEN 2
    WHEN 'failed' THEN 3
    WHEN 'killed' THEN 4
    WHEN 'killed_externally' THEN 5
    WHEN 'stale' THEN 6
    ELSE 0
END;
ALTER TABLE background_processes DROP COLUMN status;
ALTER TABLE background_processes RENAME COLUMN status_new TO status;
CREATE INDEX idx_bg_processes_worktree ON background_processes(worktree_id, status);
CREATE INDEX idx_bg_processes_user ON background_processes(user_id, status);
CREATE INDEX idx_bg_processes_running ON background_processes(status, pid) WHERE status = 1;
CREATE INDEX idx_bg_processes_cleanup ON background_processes(status, ended_at) WHERE status IN (2, 3, 4, 5, 6);

ALTER TABLE background_processes ADD COLUMN package_type_new INTEGER;
UPDATE background_processes SET package_type_new = CASE package_type
    WHEN 'makefile' THEN 1
    WHEN 'npm' THEN 2
    WHEN 'taskfile' THEN 3
    ELSE NULL
END;
ALTER TABLE background_processes DROP COLUMN package_type;
ALTER TABLE background_processes RENAME COLUMN package_type_new TO package_type;

-- ============================================================================
-- 7. message_content_blocks.block_type: ContentBlockType
--    text=1, tool_call=2, tool_result=3, image=4, thinking=5, file_reference=6
-- ============================================================================
ALTER TABLE message_content_blocks ADD COLUMN block_type_new INTEGER NOT NULL DEFAULT 0;
UPDATE message_content_blocks SET block_type_new = CASE block_type
    WHEN 'text' THEN 1
    WHEN 'tool_call' THEN 2
    WHEN 'tool_result' THEN 3
    WHEN 'image' THEN 4
    WHEN 'thinking' THEN 5
    WHEN 'file_reference' THEN 6
    ELSE 0
END;
ALTER TABLE message_content_blocks DROP COLUMN block_type;
ALTER TABLE message_content_blocks RENAME COLUMN block_type_new TO block_type;

-- ============================================================================
-- 8. messages: role (MessageRole), display_style (DisplayStyle nullable)
--    role: user=1, assistant=2, system=3, tool=4
--    display_style: info=1, warning=2, success=3, hidden=4 [nullable]
-- ============================================================================
ALTER TABLE messages ADD COLUMN role_new INTEGER NOT NULL DEFAULT 0;
UPDATE messages SET role_new = CASE role
    WHEN 'user' THEN 1
    WHEN 'assistant' THEN 2
    WHEN 'system' THEN 3
    WHEN 'tool' THEN 4
    ELSE 0
END;
ALTER TABLE messages DROP COLUMN role;
ALTER TABLE messages RENAME COLUMN role_new TO role;

ALTER TABLE messages ADD COLUMN display_style_new INTEGER;
UPDATE messages SET display_style_new = CASE display_style
    WHEN 'info' THEN 1
    WHEN 'warning' THEN 2
    WHEN 'success' THEN 3
    WHEN 'hidden' THEN 4
    ELSE NULL
END;
ALTER TABLE messages DROP COLUMN display_style;
ALTER TABLE messages RENAME COLUMN display_style_new TO display_style;

-- ============================================================================
-- 9. workflows.status: ChatWorkflowStatus
--    pending=1, running=2, completed=3, failed=4, cancelled=5, paused=6
-- ============================================================================
DROP INDEX IF EXISTS idx_workflows_status;
ALTER TABLE workflows ADD COLUMN status_new INTEGER NOT NULL DEFAULT 2;
UPDATE workflows SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'running' THEN 2
    WHEN 'completed' THEN 3
    WHEN 'failed' THEN 4
    WHEN 'cancelled' THEN 5
    WHEN 'paused' THEN 6
    ELSE 0
END;
ALTER TABLE workflows DROP COLUMN status;
ALTER TABLE workflows RENAME COLUMN status_new TO status;
CREATE INDEX idx_workflows_status ON workflows(status);

-- ============================================================================
-- 10. chats.state: ChatState
--     needs_attention=1, idle=2, archived=3
-- ============================================================================
DROP INDEX IF EXISTS idx_chats_state;
ALTER TABLE chats ADD COLUMN state_new INTEGER DEFAULT 2;
UPDATE chats SET state_new = CASE state
    WHEN 'needs_attention' THEN 1
    WHEN 'idle' THEN 2
    WHEN 'archived' THEN 3
    ELSE 0
END;
ALTER TABLE chats DROP COLUMN state;
ALTER TABLE chats RENAME COLUMN state_new TO state;
CREATE INDEX idx_chats_state ON chats(state);

-- ============================================================================
-- 11. chat_updates.update_type: ChatUpdateType
--     message=1, approval=2, thread=3, tool_call=4, workflow_status=5,
--     error=6, chat=7, run_output=8, node_execution=9, execution_log=10,
--     workflow_execution=11, yield=12, info=13, warning=14, refetch=15,
--     streaming_delta=16, skill_invocation=17
-- ============================================================================
ALTER TABLE chat_updates ADD COLUMN update_type_new INTEGER NOT NULL DEFAULT 0;
UPDATE chat_updates SET update_type_new = CASE update_type
    WHEN 'message' THEN 1
    WHEN 'approval' THEN 2
    WHEN 'thread' THEN 3
    WHEN 'tool_call' THEN 4
    WHEN 'workflow_status' THEN 5
    WHEN 'error' THEN 6
    WHEN 'chat' THEN 7
    WHEN 'run_output' THEN 8
    WHEN 'node_execution' THEN 9
    WHEN 'execution_log' THEN 10
    WHEN 'workflow_execution' THEN 11
    WHEN 'yield' THEN 12
    WHEN 'info' THEN 13
    WHEN 'warning' THEN 14
    WHEN 'refetch' THEN 15
    WHEN 'streaming_delta' THEN 16
    WHEN 'skill_invocation' THEN 17
    ELSE 0
END;
ALTER TABLE chat_updates DROP COLUMN update_type;
ALTER TABLE chat_updates RENAME COLUMN update_type_new TO update_type;

-- ============================================================================
-- 12. user_updates: update_type (UserUpdateType), entity_type (EntityType)
--     update_type: chat_state_change=1..refetch=19
--     entity_type: chat=1, project=2, worktree=3, background_process=4, system=5
-- ============================================================================
DROP INDEX IF EXISTS idx_user_updates_entity;
ALTER TABLE user_updates ADD COLUMN update_type_new INTEGER NOT NULL DEFAULT 0;
UPDATE user_updates SET update_type_new = CASE update_type
    WHEN 'chat_state_change' THEN 1
    WHEN 'chat_config_changed' THEN 2
    WHEN 'chat_created' THEN 3
    WHEN 'chat_title_changed' THEN 4
    WHEN 'chat_deleted' THEN 5
    WHEN 'chat_activity_changed' THEN 6
    WHEN 'project_created' THEN 7
    WHEN 'project_deleted' THEN 8
    WHEN 'project_settings_changed' THEN 9
    WHEN 'worktree_created' THEN 10
    WHEN 'worktree_deleted' THEN 11
    WHEN 'worktree_status_changed' THEN 12
    WHEN 'process_started' THEN 13
    WHEN 'process_output' THEN 14
    WHEN 'process_completed' THEN 15
    WHEN 'process_failed' THEN 16
    WHEN 'process_port_changed' THEN 17
    WHEN 'notification' THEN 18
    WHEN 'refetch' THEN 19
    ELSE 0
END;
ALTER TABLE user_updates DROP COLUMN update_type;
ALTER TABLE user_updates RENAME COLUMN update_type_new TO update_type;

ALTER TABLE user_updates ADD COLUMN entity_type_new INTEGER NOT NULL DEFAULT 0;
UPDATE user_updates SET entity_type_new = CASE entity_type
    WHEN 'chat' THEN 1
    WHEN 'project' THEN 2
    WHEN 'worktree' THEN 3
    WHEN 'background_process' THEN 4
    WHEN 'system' THEN 5
    ELSE 0
END;
ALTER TABLE user_updates DROP COLUMN entity_type;
ALTER TABLE user_updates RENAME COLUMN entity_type_new TO entity_type;
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);

-- ============================================================================
-- 13. visibility_overrides.item_type: HiddenItemType
--     workflow=1, preset=2
--     Table recreation required: UNIQUE(user_id, item_type, slug)
-- ============================================================================
DROP INDEX IF EXISTS idx_visibility_overrides_user_id;
DROP INDEX IF EXISTS idx_visibility_overrides_type;
CREATE TABLE visibility_overrides_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    is_visible BOOLEAN NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, item_type, slug)
);
INSERT INTO visibility_overrides_new SELECT
    id, user_id,
    CASE item_type
        WHEN 'workflow' THEN 1
        WHEN 'preset' THEN 2
        ELSE 0
    END,
    slug, is_visible, created_at
FROM visibility_overrides;
DROP TABLE visibility_overrides;
ALTER TABLE visibility_overrides_new RENAME TO visibility_overrides;
CREATE INDEX idx_visibility_overrides_user_id ON visibility_overrides(user_id);
CREATE INDEX idx_visibility_overrides_type ON visibility_overrides(user_id, item_type);

-- ============================================================================
-- 14. item_defaults.item_type: HiddenItemType
--     workflow=1, preset=2
--     Table recreation required: UNIQUE(item_type, slug)
-- ============================================================================
DROP INDEX IF EXISTS idx_item_defaults_type;
DROP INDEX IF EXISTS idx_item_defaults_hidden;
CREATE TABLE item_defaults_new (
    id TEXT PRIMARY KEY,
    item_type INTEGER NOT NULL,
    slug TEXT NOT NULL,
    is_hidden BOOLEAN NOT NULL DEFAULT FALSE,
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(item_type, slug)
);
INSERT INTO item_defaults_new SELECT
    id,
    CASE item_type
        WHEN 'workflow' THEN 1
        WHEN 'preset' THEN 2
        ELSE 0
    END,
    slug, is_hidden, reason, created_at, updated_at
FROM item_defaults;
DROP TABLE item_defaults;
ALTER TABLE item_defaults_new RENAME TO item_defaults;
CREATE INDEX idx_item_defaults_type ON item_defaults(item_type);
CREATE INDEX idx_item_defaults_hidden ON item_defaults(item_type, is_hidden);

-- ============================================================================
-- 15. yields.status: YieldStatus
--     pending=1, resolved=2
-- ============================================================================
DROP INDEX IF EXISTS idx_yields_chat_id_status;
DROP INDEX IF EXISTS idx_yields_workflow_step;
ALTER TABLE yields ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE yields SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'resolved' THEN 2
    ELSE 0
END;
ALTER TABLE yields DROP COLUMN status;
ALTER TABLE yields RENAME COLUMN status_new TO status;
CREATE INDEX idx_yields_chat_id_status ON yields(chat_id, status);
CREATE INDEX idx_yields_workflow_step ON yields(workflow_id, step_id, status);

-- ============================================================================
-- Step 16: Recreate triggers with integer values
-- ============================================================================

-- Recreate chat_updates trigger with integer update_type value (ChatUpdateType CHAT = 7)
-- +goose StatementBegin
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
-- +goose StatementEnd

-- Recreate user_updates trigger with integer values
-- update_type: chat_config_changed=2, entity_type: chat=1
-- +goose StatementBegin
CREATE TRIGGER user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    OLD.workflow_name IS NOT NEW.workflow_name
)
BEGIN
    INSERT INTO user_updates (
        user_id,
        sequence_number,
        project_id,
        worktree_id,
        chat_id,
        update_type,
        entity_type,
        entity_id,
        data
    ) VALUES (
        NEW.user_id,
        COALESCE((SELECT MAX(sequence_number) FROM user_updates WHERE user_id = NEW.user_id), 0) + 1,
        NEW.project_id,
        NEW.worktree_id,
        NEW.id,
        2,
        1,
        NEW.id,
        json_object(
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title,
            'previous_workflow_name', OLD.workflow_name,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- This is a destructive pre-launch migration; down is not supported.
-- To rollback, restore from backup or re-run all migrations from scratch.
