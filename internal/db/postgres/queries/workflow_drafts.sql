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
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1)
RETURNING *;

-- name: GetWorkflowDraft :one
SELECT * FROM workflow_drafts WHERE id = $1;

-- name: GetWorkflowDraftBySlug :one
-- Lookup by user and slug (simple, no scope complexity)
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2;

-- name: GetWorkflowDraftByChatID :one
SELECT * FROM workflow_drafts WHERE chat_id = $1;

-- name: GetWorkflowDraftBySourcePath :one
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND source_path = $2;

-- name: ListWorkflowDraftsByUser :many
-- List all workflows for a user, ordered by most recently updated
SELECT * FROM workflow_drafts 
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: ListUsableWorkflowsByUser :many
-- List workflows that are usable (valid and not hidden)
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND is_valid = 1 AND is_hidden = false
ORDER BY name ASC;

-- name: GetUsableWorkflowBySlug :one
-- Get a usable workflow by slug (for runtime loading)
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2 AND is_valid = 1 AND is_hidden = false;

-- name: UpsertWorkflowDraft :one
-- Create or update a workflow draft
-- Unique on (user_id, slug)
INSERT INTO workflow_drafts (
    id, user_id, name, slug, description, definition,
    is_valid, validation_errors, source_path,
    forked_from, chat_id, created_at, updated_at, is_hidden, version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1)
ON CONFLICT(user_id, slug) DO UPDATE SET
    name = excluded.name,
    description = excluded.description,
    definition = excluded.definition,
    is_valid = excluded.is_valid,
    validation_errors = excluded.validation_errors,
    is_hidden = excluded.is_hidden,
    -- Don't update forked_from on upsert to preserve origin
    updated_at = NOW(),
    version = workflow_drafts.version + 1
RETURNING *;

-- name: UpdateWorkflowDraft :one
UPDATE workflow_drafts SET
    name = $1,
    slug = $2,
    description = $3,
    definition = $4,
    is_valid = $5,
    validation_errors = $6,
    is_hidden = $7,
    updated_at = NOW(),
    version = version + 1
WHERE id = $8
RETURNING *;

-- name: UpdateWorkflowDraftDefinition :one
UPDATE workflow_drafts SET
    name = $1,
    slug = $2,
    definition = $3,
    is_valid = $4,
    validation_errors = $5,
    updated_at = NOW(),
    version = version + 1
WHERE id = $6
RETURNING *;

-- name: DeleteWorkflowDraft :exec
DELETE FROM workflow_drafts WHERE id = $1;

-- name: DeleteWorkflowDraftBySlug :exec
DELETE FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2;

-- name: CountWorkflowDraftsByUser :one
SELECT COUNT(*) FROM workflow_drafts WHERE user_id = $1;

-- name: WorkflowSlugExists :one
SELECT EXISTS(
    SELECT 1 FROM workflow_drafts 
    WHERE user_id = $1 AND slug = $2
) as exists_flag;

-- name: GetWorkflowDraftByName :one
-- Check if a workflow with this exact name exists for the user
-- Used for duplicate name validation (different from slug check)
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND LOWER(name) = LOWER($2);

-- name: AssociateChatWithDraft :one
UPDATE workflow_drafts SET
    chat_id = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING *;

-- name: GetWorkflowsForkedFrom :many
-- Get all workflows that were forked from a specific origin
SELECT * FROM workflow_drafts 
WHERE user_id = $1 AND forked_from = $2;

-- name: UpdateWorkflowForkedFrom :one
-- Set or update the forked_from origin
UPDATE workflow_drafts SET
    forked_from = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING *;

-- name: SetWorkflowDraftHidden :one
UPDATE workflow_drafts SET
    is_hidden = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING *;
