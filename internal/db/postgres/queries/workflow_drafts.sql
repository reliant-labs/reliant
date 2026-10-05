-- Workflow Drafts - Simplified user-owned workflows
-- Workflows are owned by users and available across all projects
-- Project-specific workflows come from .reliant/workflows/*.yaml files (read-only)
-- A workflow is "usable" (shows in agent selector, can be loaded at runtime)
-- when status = 'complete' AND is_hidden = false. Validity is never stored: it
-- is computed on read and re-checked at run start.

-- name: CreateWorkflowDraft :one
INSERT INTO workflow_drafts (
    id, user_id, name, slug, description, definition,
    status, source_path,
    forked_from, created_at, updated_at, is_hidden, version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1)
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: GetWorkflowDraft :one
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts WHERE id = $1;

-- name: GetWorkflowDraftBySlug :one
-- Lookup by user and slug (simple, no scope complexity)
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2;

-- name: GetWorkflowDraftBySourcePath :one
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1 AND source_path = $2;

-- name: ListWorkflowDraftsByUser :many
-- List all workflows for a user, ordered by most recently updated
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: GetUsableWorkflowBySlug :one
-- Get a usable workflow by slug (for runtime loading)
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2 AND status = 'complete' AND is_hidden = false;

-- name: UpsertWorkflowDraft :one
-- Create or update a workflow draft
-- Unique on (user_id, slug)
INSERT INTO workflow_drafts (
    id, user_id, name, slug, description, definition,
    status, source_path,
    forked_from, created_at, updated_at, is_hidden, version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1)
ON CONFLICT(user_id, slug) DO UPDATE SET
    name = excluded.name,
    description = excluded.description,
    definition = excluded.definition,
    status = excluded.status,
    is_hidden = excluded.is_hidden,
    -- Don't update forked_from on upsert to preserve origin
    updated_at = NOW(),
    version = workflow_drafts.version + 1
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: UpdateWorkflowDraft :one
UPDATE workflow_drafts SET
    name = $1,
    slug = $2,
    description = $3,
    definition = $4,
    status = $5,
    is_hidden = $6,
    updated_at = NOW(),
    version = version + 1
WHERE id = $7
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: UpdateWorkflowDraftDefinition :one
UPDATE workflow_drafts SET
    name = $1,
    slug = $2,
    definition = $3,
    status = $4,
    updated_at = NOW(),
    version = version + 1
WHERE id = $5
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: DeleteWorkflowDraft :one
DELETE FROM workflow_drafts WHERE id = $1
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: DeleteWorkflowDraftBySlug :one
DELETE FROM workflow_drafts 
WHERE user_id = $1 AND slug = $2
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

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
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1 AND LOWER(name) = LOWER($2);

-- name: GetWorkflowsForkedFrom :many
-- Get all workflows that were forked from a specific origin
SELECT id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status FROM workflow_drafts 
WHERE user_id = $1 AND forked_from = $2;

-- name: UpdateWorkflowForkedFrom :one
-- Set or update the forked_from origin
UPDATE workflow_drafts SET
    forked_from = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: SetWorkflowDraftHidden :one
UPDATE workflow_drafts SET
    is_hidden = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- name: SetWorkflowDraftStatus :one
-- Move a draft between 'draft' and 'complete'. The caller validates before
-- marking complete; this query only records the decision.
UPDATE workflow_drafts SET
    status = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = $2
RETURNING id, user_id, name, slug, description, definition, source_path, forked_from, is_hidden, created_at, updated_at, version, status;

-- NOTE: workflow_drafts.chat_id is retired: no query reads or writes it, and every
-- SELECT/RETURNING lists columns explicitly so it is never selected. The column
-- stays until a follow-up contract migration (the previous release still reads
-- it during a rolling deploy).
