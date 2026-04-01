-- Workflow Drafts - Simplified user-owned workflows
-- Workflows are owned by users and available across all projects
-- Project-specific workflows come from .reliant/workflows/*.yaml files (read-only)
-- A workflow is "usable" (shows in agent selector, can be loaded at runtime)
-- when is_hidden = 0 AND is_valid = 1.

-- name: CreateWorkflowDraft :one
INSERT INTO workflow_drafts (
    id, user_id, name, slug, description, definition,
    is_valid, validation_errors, source_path,
    forked_from, chat_id, created_at, updated_at, is_hidden, version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
RETURNING *;

-- name: GetWorkflowDraft :one
SELECT * FROM workflow_drafts WHERE id = ?;

-- name: GetWorkflowDraftBySlug :one
-- Lookup by user and slug (simple, no scope complexity)
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND slug = ?;

-- name: GetWorkflowDraftByChatID :one
SELECT * FROM workflow_drafts WHERE chat_id = ?;

-- name: GetWorkflowDraftBySourcePath :one
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND source_path = ?;

-- name: ListWorkflowDraftsByUser :many
-- List all workflows for a user, ordered by most recently updated
SELECT * FROM workflow_drafts 
WHERE user_id = ?
ORDER BY updated_at DESC;

-- name: ListUsableWorkflowsByUser :many
-- List workflows that are usable (valid and not hidden)
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND is_valid = 1 AND is_hidden = 0
ORDER BY name ASC;

-- name: GetUsableWorkflowBySlug :one
-- Get a usable workflow by slug (for runtime loading)
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND slug = ? AND is_valid = 1 AND is_hidden = 0;

-- name: UpsertWorkflowDraft :one
-- Create or update a workflow draft
-- Unique on (user_id, slug)
INSERT INTO workflow_drafts (
    id, user_id, name, slug, description, definition,
    is_valid, validation_errors, source_path,
    forked_from, chat_id, created_at, updated_at, is_hidden, version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(user_id, slug) DO UPDATE SET
    name = excluded.name,
    description = excluded.description,
    definition = excluded.definition,
    is_valid = excluded.is_valid,
    validation_errors = excluded.validation_errors,
    is_hidden = excluded.is_hidden,
    -- Don't update forked_from on upsert to preserve origin
    updated_at = datetime('now', 'utc'),
    version = workflow_drafts.version + 1
RETURNING *;

-- name: UpdateWorkflowDraft :one
UPDATE workflow_drafts SET
    name = ?,
    slug = ?,
    description = ?,
    definition = ?,
    is_valid = ?,
    validation_errors = ?,
    is_hidden = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: UpdateWorkflowDraftDefinition :one
UPDATE workflow_drafts SET
    name = ?,
    slug = ?,
    definition = ?,
    is_valid = ?,
    validation_errors = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: DeleteWorkflowDraft :exec
DELETE FROM workflow_drafts WHERE id = ?;

-- name: DeleteWorkflowDraftBySlug :exec
DELETE FROM workflow_drafts 
WHERE user_id = ? AND slug = ?;

-- name: CountWorkflowDraftsByUser :one
SELECT COUNT(*) FROM workflow_drafts WHERE user_id = ?;

-- name: WorkflowSlugExists :one
SELECT EXISTS(
    SELECT 1 FROM workflow_drafts 
    WHERE user_id = ? AND slug = ?
) as exists_flag;

-- name: GetWorkflowDraftByName :one
-- Check if a workflow with this exact name exists for the user
-- Used for duplicate name validation (different from slug check)
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND LOWER(name) = LOWER(?);

-- name: AssociateChatWithDraft :one
UPDATE workflow_drafts SET
    chat_id = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: GetWorkflowsForkedFrom :many
-- Get all workflows that were forked from a specific origin
SELECT * FROM workflow_drafts 
WHERE user_id = ? AND forked_from = ?;

-- name: UpdateWorkflowForkedFrom :one
-- Set or update the forked_from origin
UPDATE workflow_drafts SET
    forked_from = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: SetWorkflowDraftHidden :one
UPDATE workflow_drafts SET
    is_hidden = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;
