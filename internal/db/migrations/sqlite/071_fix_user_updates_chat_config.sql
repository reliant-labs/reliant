-- +goose Up
-- Fix user_updates table to include 'chat_config_changed' in CHECK constraint
-- This is needed by the user_updates_chat_config_update trigger

-- Drop the trigger first
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Recreate user_updates table with correct CHECK constraint
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

-- Recreate the trigger
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS user_updates_chat_config_update
AFTER UPDATE ON chats
FOR EACH ROW
WHEN (
    -- Only emit to global websocket when agent/workflow/model/auto_approve changes
    -- (state changes have their own user_update via UpdateChatState)
    OLD.agent IS NOT NEW.agent OR
    OLD.workflow_name IS NOT NEW.workflow_name OR
    OLD.model IS NOT NEW.model OR
    OLD.auto_approve IS NOT NEW.auto_approve
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
            'agent', NEW.agent,
            'workflow_name', NEW.workflow_name,
            'model', NEW.model,
            'temperature', NEW.temperature,
            'max_tokens', NEW.max_tokens,
            'auto_approve', NEW.auto_approve,
            'state', NEW.state,
            'title', NEW.title,
            'previous_agent', OLD.agent,
            'previous_workflow_name', OLD.workflow_name,
            'updated_at', NEW.updated_at
        )
    );
END;
-- +goose StatementEnd

-- +goose Down
-- Drop trigger
DROP TRIGGER IF EXISTS user_updates_chat_config_update;

-- Recreate user_updates table without 'chat_config_changed'
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
        'chat_deleted',
        
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
WHERE update_type != 'chat_config_changed';

DROP TABLE user_updates;
ALTER TABLE user_updates_old RENAME TO user_updates;

CREATE INDEX idx_user_updates_poll ON user_updates(user_id, sequence_number DESC);
CREATE INDEX idx_user_updates_project ON user_updates(project_id, sequence_number DESC) WHERE project_id IS NOT NULL;
CREATE INDEX idx_user_updates_chat ON user_updates(chat_id) WHERE chat_id IS NOT NULL;
CREATE INDEX idx_user_updates_entity ON user_updates(entity_type, entity_id);
