-- +goose Up
-- Migrate TEXT enum columns to INTEGER using proto enum numeric values.
-- Postgres supports ALTER COLUMN TYPE ... USING, so no table recreation needed.

-- 1. worktrees.status → WorktreeStatus (active=1, completed=2, abandoned=3, merging=4)
ALTER TABLE worktrees ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'active' THEN 1
        WHEN 'completed' THEN 2
        WHEN 'abandoned' THEN 3
        WHEN 'merging' THEN 4
        ELSE 0
    END;
ALTER TABLE worktrees ALTER COLUMN status SET DEFAULT 1;

-- 2. plans.status → PlanStatus (pending=1, in_progress=2, completed=3, cancelled=4)
ALTER TABLE plans ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'in_progress' THEN 2
        WHEN 'completed' THEN 3
        WHEN 'cancelled' THEN 4
        ELSE 0
    END;
ALTER TABLE plans ALTER COLUMN status SET DEFAULT 1;

-- 2b. plans.complexity → PlanComplexity (simple=1, moderate=2, complex=3) [nullable]
ALTER TABLE plans ALTER COLUMN complexity TYPE INTEGER USING
    CASE complexity
        WHEN 'simple' THEN 1
        WHEN 'moderate' THEN 2
        WHEN 'complex' THEN 3
        ELSE NULL
    END;

-- 3. tasks.status → TaskStatus (pending=1..skipped=7)
ALTER TABLE tasks ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'in_progress' THEN 2
        WHEN 'completed' THEN 3
        WHEN 'failed' THEN 4
        WHEN 'blocked' THEN 5
        WHEN 'cancelled' THEN 6
        WHEN 'skipped' THEN 7
        ELSE 0
    END;
ALTER TABLE tasks ALTER COLUMN status SET DEFAULT 1;

-- 4. task_dependencies.dependency_type → DependencyType (blocks=1, related=2, parallel_with=3)
ALTER TABLE task_dependencies DROP CONSTRAINT IF EXISTS task_dependencies_dependency_type_check;
ALTER TABLE task_dependencies ALTER COLUMN dependency_type TYPE INTEGER USING
    CASE dependency_type
        WHEN 'blocks' THEN 1
        WHEN 'related' THEN 2
        WHEN 'parallel_with' THEN 3
        ELSE 0
    END;

-- 5. approvals.approval_type → ApprovalType (tool=1, workflow_step=2)
ALTER TABLE approvals ALTER COLUMN approval_type TYPE INTEGER USING
    CASE approval_type
        WHEN 'tool' THEN 1
        WHEN 'workflow_step' THEN 2
        ELSE 0
    END;

-- 5b. approvals.status → ApprovalStatus (pending=1, approved=2, denied=3, cancelled=4)
ALTER TABLE approvals ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'approved' THEN 2
        WHEN 'denied' THEN 3
        WHEN 'cancelled' THEN 4
        ELSE 0
    END;

-- 6. background_processes.status → BackgroundProcessStatus (running=1..stale=6)
DROP INDEX IF EXISTS idx_bg_processes_running;
DROP INDEX IF EXISTS idx_bg_processes_cleanup;
ALTER TABLE background_processes DROP CONSTRAINT IF EXISTS background_processes_status_check;
ALTER TABLE background_processes ALTER COLUMN status DROP DEFAULT;
ALTER TABLE background_processes ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'running' THEN 1
        WHEN 'completed' THEN 2
        WHEN 'failed' THEN 3
        WHEN 'killed' THEN 4
        WHEN 'killed_externally' THEN 5
        WHEN 'stale' THEN 6
        ELSE 0
    END;
ALTER TABLE background_processes ALTER COLUMN status SET DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_bg_processes_running ON background_processes(status, pid)
    WHERE status = 1;
CREATE INDEX IF NOT EXISTS idx_bg_processes_cleanup ON background_processes(status, ended_at)
    WHERE status IN (2, 3, 4, 5, 6);

-- 6b. background_processes.package_type → PackageType (makefile=1, npm=2, taskfile=3) [nullable]
ALTER TABLE background_processes ALTER COLUMN package_type TYPE INTEGER USING
    CASE package_type
        WHEN 'makefile' THEN 1
        WHEN 'npm' THEN 2
        WHEN 'taskfile' THEN 3
        ELSE NULL
    END;

-- 7. message_content_blocks.block_type → ContentBlockType (text=1..file_reference=6)
ALTER TABLE message_content_blocks ALTER COLUMN block_type TYPE INTEGER USING
    CASE block_type
        WHEN 'text' THEN 1
        WHEN 'tool_call' THEN 2
        WHEN 'tool_result' THEN 3
        WHEN 'image' THEN 4
        WHEN 'thinking' THEN 5
        WHEN 'file_reference' THEN 6
        ELSE 0
    END;

-- 8. messages.role → MessageRole (user=1, assistant=2, system=3, tool=4)
ALTER TABLE messages ALTER COLUMN role TYPE INTEGER USING
    CASE role
        WHEN 'user' THEN 1
        WHEN 'assistant' THEN 2
        WHEN 'system' THEN 3
        WHEN 'tool' THEN 4
        ELSE 0
    END;

-- 8b. messages.display_style → DisplayStyle (info=1, warning=2, success=3, hidden=4) [nullable]
ALTER TABLE messages ALTER COLUMN display_style TYPE INTEGER USING
    CASE display_style
        WHEN 'info' THEN 1
        WHEN 'warning' THEN 2
        WHEN 'success' THEN 3
        WHEN 'hidden' THEN 4
        ELSE NULL
    END;

-- 9. workflows.status → ChatWorkflowStatus (pending=1..paused=6)
ALTER TABLE workflows ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'running' THEN 2
        WHEN 'completed' THEN 3
        WHEN 'failed' THEN 4
        WHEN 'cancelled' THEN 5
        WHEN 'paused' THEN 6
        ELSE 0
    END;
ALTER TABLE workflows ALTER COLUMN status SET DEFAULT 2;

-- 10. chats.state → ChatState (needs_attention=1, idle=2, archived=3)
ALTER TABLE chats ALTER COLUMN state TYPE INTEGER USING
    CASE state
        WHEN 'needs_attention' THEN 1
        WHEN 'idle' THEN 2
        WHEN 'archived' THEN 3
        ELSE 0
    END;
ALTER TABLE chats ALTER COLUMN state SET DEFAULT 2;

-- 11. chat_updates.update_type → ChatUpdateType (message=1..refetch=15, streaming_delta=16, skill_invocation=17)
ALTER TABLE chat_updates ALTER COLUMN update_type TYPE INTEGER USING
    CASE update_type
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

-- 12. user_updates.update_type → UserUpdateType (1-19)
ALTER TABLE user_updates DROP CONSTRAINT IF EXISTS user_updates_update_type_check;
ALTER TABLE user_updates ALTER COLUMN update_type TYPE INTEGER USING
    CASE update_type
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

-- 12b. user_updates.entity_type → EntityType (chat=1..system=5)
ALTER TABLE user_updates DROP CONSTRAINT IF EXISTS user_updates_entity_type_check;
ALTER TABLE user_updates ALTER COLUMN entity_type TYPE INTEGER USING
    CASE entity_type
        WHEN 'chat' THEN 1
        WHEN 'project' THEN 2
        WHEN 'worktree' THEN 3
        WHEN 'background_process' THEN 4
        WHEN 'system' THEN 5
        ELSE 0
    END;

-- 13. visibility_overrides.item_type → HiddenItemType (workflow=1, preset=2)
ALTER TABLE visibility_overrides ALTER COLUMN item_type TYPE INTEGER USING
    CASE item_type
        WHEN 'workflow' THEN 1
        WHEN 'preset' THEN 2
        ELSE 0
    END;

-- 14. item_defaults.item_type → HiddenItemType (workflow=1, preset=2)
ALTER TABLE item_defaults ALTER COLUMN item_type TYPE INTEGER USING
    CASE item_type
        WHEN 'workflow' THEN 1
        WHEN 'preset' THEN 2
        ELSE 0
    END;

-- 15. yields.status → YieldStatus (pending=1, resolved=2)
ALTER TABLE yields DROP CONSTRAINT IF EXISTS yields_status_check;
ALTER TABLE yields ALTER COLUMN status DROP DEFAULT;
ALTER TABLE yields ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'resolved' THEN 2
        ELSE 0
    END;
ALTER TABLE yields ALTER COLUMN status SET DEFAULT 1;

-- +goose Down
-- Pre-launch migration; down migration not supported.
