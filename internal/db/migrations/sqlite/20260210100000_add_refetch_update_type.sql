-- +goose Up
-- Add 'refetch' update type to both user_updates and chat_updates tables.
-- This enables the backend to signal the frontend to re-fetch specific data
-- instead of the frontend blindly polling on intervals.

-- ============================================================================
-- PART 1: Add 'refetch' to user_updates CHECK constraint
-- ============================================================================

-- Drop trigger that references user_updates
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Recreate user_updates table with 'refetch' in CHECK constraint
CREATE TABLE user_updates_new (
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
        'chat_activity_changed',
        
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
        'process_port_changed',
        
        -- General notification
        'notification',

        -- Refetch signal
        'refetch'
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

-- Copy existing data
INSERT INTO user_updates_new (id, user_id, sequence_number, project_id, worktree_id, chat_id, update_type, entity_type, entity_id, data, created_at)
SELECT id, user_id, sequence_number, project_id, worktree_id, chat_id, update_type, entity_type, entity_id, data, created_at
FROM user_updates;

-- Drop old table and rename
DROP TABLE user_updates;
ALTER TABLE user_updates_new RENAME TO user_updates;

-- Recreate indexes
CREATE INDEX idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);
CREATE INDEX idx_user_updates_project ON user_updates(project_id, sequence_number DESC) 
    WHERE project_id IS NOT NULL;
CREATE INDEX idx_user_updates_chat ON user_updates(chat_id) 
    WHERE chat_id IS NOT NULL;
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);

-- Recreate the user_updates trigger (matches current chats schema: no agent, no model columns)
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
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
        'chat_config_changed',
        'chat',
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

-- ============================================================================
-- PART 2: Add 'refetch' to chat_updates CHECK constraint
-- ============================================================================

-- Drop trigger that references chat_updates
DROP TRIGGER IF EXISTS chat_updates_chat_update;

-- Recreate chat_updates table with 'refetch' in CHECK constraint
CREATE TABLE chat_updates_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'workflow_status',
        'error',
        'chat',
        'run_output',
        'node_execution',
        'execution_log',
        'workflow_execution',
        'info',
        'warning',
        'yield',
        'refetch'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

-- Copy all data
INSERT INTO chat_updates_new (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates;

-- Drop old table and rename
DROP TABLE chat_updates;
ALTER TABLE chat_updates_new RENAME TO chat_updates;

-- Recreate indexes
CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

-- Recreate the chat_updates trigger (matches current chats schema: no agent, no model columns)
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
        'chat',
        NEW.id,
        json_object(
            'update_type', 'chat',
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Remove 'refetch' from both tables

-- ============================================================================
-- Revert user_updates
-- ============================================================================
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

CREATE TABLE user_updates_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    user_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    project_id TEXT,
    worktree_id TEXT,
    chat_id TEXT,
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
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE CASCADE,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(user_id, sequence_number)
);

INSERT INTO user_updates_old (id, user_id, sequence_number, project_id, worktree_id, chat_id, update_type, entity_type, entity_id, data, created_at)
SELECT id, user_id, sequence_number, project_id, worktree_id, chat_id, update_type, entity_type, entity_id, data, created_at
FROM user_updates
WHERE update_type != 'refetch';

DROP TABLE user_updates;
ALTER TABLE user_updates_old RENAME TO user_updates;

CREATE INDEX idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);
CREATE INDEX idx_user_updates_project ON user_updates(project_id, sequence_number DESC) WHERE project_id IS NOT NULL;
CREATE INDEX idx_user_updates_chat ON user_updates(chat_id) WHERE chat_id IS NOT NULL;
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
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
        'chat_config_changed',
        'chat',
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

-- ============================================================================
-- Revert chat_updates
-- ============================================================================
DROP TRIGGER IF EXISTS chat_updates_chat_update;

CREATE TABLE chat_updates_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    chat_id TEXT NOT NULL,
    sequence_number INTEGER NOT NULL,
    update_type TEXT NOT NULL CHECK (update_type IN (
        'message',
        'approval',
        'thread',
        'tool_call',
        'workflow_status',
        'error',
        'chat',
        'run_output',
        'node_execution',
        'execution_log',
        'workflow_execution',
        'info',
        'warning'
    )),
    entity_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, sequence_number)
);

INSERT INTO chat_updates_old (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at
FROM chat_updates
WHERE update_type != 'refetch';

DROP TABLE chat_updates;
ALTER TABLE chat_updates_old RENAME TO chat_updates;

CREATE INDEX idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX idx_chat_updates_created ON chat_updates(created_at DESC);

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
        'chat',
        NEW.id,
        json_object(
            'update_type', 'chat',
            'chat_id', NEW.id,
            'workflow_name', NEW.workflow_name,
            'state', NEW.state,
            'title', NEW.title
        ),
        datetime('now')
    );
END;
-- +goose StatementEnd