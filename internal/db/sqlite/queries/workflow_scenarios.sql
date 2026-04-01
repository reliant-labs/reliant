-- Workflow Scenarios - Test scenarios for workflow simulation
-- Scenarios define event sequences to test workflow behavior

-- name: CreateWorkflowScenario :one
INSERT INTO workflow_scenarios (
    id, workflow_draft_id, user_id, name, description,
    events, expect, created_at, updated_at, version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
RETURNING *;

-- name: GetWorkflowScenario :one
SELECT * FROM workflow_scenarios WHERE id = ?;

-- name: GetWorkflowScenarioByName :one
-- Get a scenario by name and draft ID (for upsert behavior)
SELECT * FROM workflow_scenarios 
WHERE workflow_draft_id = ? AND name = ?;

-- name: ListWorkflowScenariosByDraft :many
-- List all scenarios for a workflow draft
SELECT * FROM workflow_scenarios 
WHERE workflow_draft_id = ?
ORDER BY created_at DESC;

-- name: ListWorkflowScenariosByUser :many
-- List all scenarios for a user
SELECT * FROM workflow_scenarios 
WHERE user_id = ?
ORDER BY updated_at DESC;

-- name: UpdateWorkflowScenario :one
UPDATE workflow_scenarios SET
    name = ?,
    description = ?,
    events = ?,
    expect = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: UpdateWorkflowScenarioResult :one
-- Update the cached run result
UPDATE workflow_scenarios SET
    last_run_at = datetime('now', 'utc'),
    last_run_status = ?,
    last_run_result = ?,
    updated_at = datetime('now', 'utc'),
    version = version + 1
WHERE id = ?
RETURNING *;

-- name: DeleteWorkflowScenario :exec
DELETE FROM workflow_scenarios WHERE id = ?;

-- name: DeleteWorkflowScenariosByDraft :exec
-- Delete all scenarios for a workflow draft (used when deleting drafts)
DELETE FROM workflow_scenarios WHERE workflow_draft_id = ?;

-- name: CountWorkflowScenariosByDraft :one
SELECT COUNT(*) FROM workflow_scenarios WHERE workflow_draft_id = ?;

-- name: CountWorkflowScenariosByUser :one
SELECT COUNT(*) FROM workflow_scenarios WHERE user_id = ?;
