-- Workflow Scenarios - Test scenarios for workflow simulation
-- Scenarios define event sequences to test workflow behavior

-- name: CreateWorkflowScenario :one
INSERT INTO workflow_scenarios (
    id, workflow_draft_id, user_id, name, description,
    events, expect, created_at, updated_at, version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1)
RETURNING *;

-- name: GetWorkflowScenario :one
SELECT * FROM workflow_scenarios WHERE id = $1;

-- name: GetWorkflowScenarioByName :one
-- Get a scenario by name and draft ID (for upsert behavior)
SELECT * FROM workflow_scenarios 
WHERE workflow_draft_id = $1 AND name = $2;

-- name: ListWorkflowScenariosByDraft :many
-- List all scenarios for a workflow draft
SELECT * FROM workflow_scenarios 
WHERE workflow_draft_id = $1
ORDER BY created_at DESC;

-- name: ListWorkflowScenariosByUser :many
-- List all scenarios for a user
SELECT * FROM workflow_scenarios 
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: UpdateWorkflowScenario :one
UPDATE workflow_scenarios SET
    name = $1,
    description = $2,
    events = $3,
    expect = $4,
    updated_at = NOW(),
    version = version + 1
WHERE id = $5
RETURNING *;

-- name: UpdateWorkflowScenarioResult :one
-- Update the cached run result
UPDATE workflow_scenarios SET
    last_run_at = NOW(),
    last_run_status = $1,
    last_run_result = $2,
    updated_at = NOW(),
    version = version + 1
WHERE id = $3
RETURNING *;

-- name: DeleteWorkflowScenario :exec
DELETE FROM workflow_scenarios WHERE id = $1;

-- name: DeleteWorkflowScenariosByDraft :exec
-- Delete all scenarios for a workflow draft (used when deleting drafts)
DELETE FROM workflow_scenarios WHERE workflow_draft_id = $1;

-- name: CountWorkflowScenariosByDraft :one
SELECT COUNT(*) FROM workflow_scenarios WHERE workflow_draft_id = $1;

-- name: CountWorkflowScenariosByUser :one
SELECT COUNT(*) FROM workflow_scenarios WHERE user_id = $1;
