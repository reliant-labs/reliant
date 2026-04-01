-- +goose Up
-- Simplify workflow_drafts: Remove project/scope/worktree coupling
-- Workflows are now user-owned and available across all projects
-- Project-specific workflows come from .reliant/workflows/*.yaml files (read-only)

-- Step 1: Create new simplified table
CREATE TABLE workflow_drafts_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,                   -- Display name (can have spaces, caps)
    slug TEXT NOT NULL,                   -- Runtime reference name (lowercase, hyphenated)
    description TEXT,
    definition TEXT NOT NULL,             -- JSON workflow definition
    is_valid INTEGER NOT NULL DEFAULT 0,  -- Boolean: passes validation
    validation_errors TEXT,               -- JSON array of validation errors
    status TEXT NOT NULL DEFAULT 'draft'  -- 'draft' or 'published'
        CHECK (status IN ('draft', 'published')),
    source_path TEXT,                     -- Original file path if imported
    published_at DATETIME,                -- When status changed to published
    chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Step 2: Migrate data from old schema
-- Generate slug from name (lowercase, spaces to hyphens)
-- Generate status from published_at (if set, 'published', else 'draft')
-- Deduplicate by (user_id, generated_slug), keeping newest
INSERT INTO workflow_drafts_new (id, user_id, name, slug, description, definition, is_valid, validation_errors, status, source_path, published_at, chat_id, created_at, updated_at)
SELECT
    id,
    user_id,
    name,
    LOWER(REPLACE(REPLACE(REPLACE(name, ' ', '-'), '_', '-'), '.', '-')) as slug,
    description,
    definition,
    is_valid,
    validation_errors,
    CASE WHEN published_at IS NOT NULL THEN 'published' ELSE 'draft' END as status,
    source_path,
    published_at,
    chat_id,
    created_at,
    updated_at
FROM workflow_drafts
WHERE id IN (
    -- For each (user_id, name) group, keep newest
    SELECT id FROM (
        SELECT
            id,
            ROW_NUMBER() OVER (
                PARTITION BY user_id, LOWER(REPLACE(REPLACE(REPLACE(name, ' ', '-'), '_', '-'), '.', '-'))
                ORDER BY
                    CASE WHEN published_at IS NOT NULL THEN 0 ELSE 1 END,
                    updated_at DESC
            ) as rn
        FROM workflow_drafts
    ) WHERE rn = 1
);

-- Step 3: Drop old table and rename
DROP TABLE workflow_drafts;
ALTER TABLE workflow_drafts_new RENAME TO workflow_drafts;

-- Step 4: Create new indexes
-- Unique constraint: slug must be unique per user
CREATE UNIQUE INDEX idx_workflow_drafts_unique_slug ON workflow_drafts(user_id, slug);

-- Index for listing user's workflows
CREATE INDEX idx_workflow_drafts_user ON workflow_drafts(user_id, status);

-- Index for slug lookups
CREATE INDEX idx_workflow_drafts_slug ON workflow_drafts(slug, status);

-- Index for chat association
CREATE INDEX idx_workflow_drafts_chat ON workflow_drafts(chat_id);

-- Step 5: Recreate trigger for updated_at
-- +goose StatementBegin
CREATE TRIGGER update_workflow_drafts_timestamp 
AFTER UPDATE ON workflow_drafts 
BEGIN 
    UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id; 
END;
-- +goose StatementEnd

-- +goose Down
-- Recreate original table structure with project/scope columns
CREATE TABLE workflow_drafts_old (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    is_valid INTEGER NOT NULL DEFAULT 0,
    validation_errors TEXT,
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published')),
    scope TEXT NOT NULL DEFAULT 'project'
        CHECK (scope IN ('global', 'project', 'worktree')),
    worktree_id TEXT,
    source_path TEXT,
    migrated_from TEXT,
    published_at DATETIME,
    chat_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO workflow_drafts_old (id, project_id, user_id, name, slug, description, definition, is_valid, validation_errors, status, scope, source_path, published_at, chat_id, created_at, updated_at)
SELECT id, '', user_id, name, slug, description, definition, is_valid, validation_errors, status, 'project', source_path, published_at, chat_id, created_at, updated_at
FROM workflow_drafts;

DROP TABLE workflow_drafts;
ALTER TABLE workflow_drafts_old RENAME TO workflow_drafts;

-- Recreate original indexes
CREATE INDEX idx_workflow_drafts_project ON workflow_drafts(project_id, user_id);
CREATE INDEX idx_workflow_drafts_source ON workflow_drafts(project_id, source_path);
CREATE INDEX idx_workflow_drafts_chat ON workflow_drafts(chat_id);
CREATE INDEX idx_workflow_drafts_slug ON workflow_drafts(slug, status);
CREATE INDEX idx_workflow_drafts_scope ON workflow_drafts(project_id, scope);
CREATE UNIQUE INDEX idx_workflow_drafts_unique_slug 
    ON workflow_drafts(project_id, scope, COALESCE(worktree_id, ''), slug);

-- +goose StatementBegin
CREATE TRIGGER update_workflow_drafts_timestamp 
AFTER UPDATE ON workflow_drafts 
BEGIN 
    UPDATE workflow_drafts SET updated_at = datetime('now', 'utc') WHERE id = NEW.id; 
END;
-- +goose StatementEnd
