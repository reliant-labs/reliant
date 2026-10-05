// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workflowWithTriggers is a runnable graph plus a triggers block.
func workflowWithTriggers(triggers string) string {
	return `name: triaged
inputs:
  issue_number:
    type: integer
    required: false
entry: [a]
nodes:
  - id: a
    type: approval
    args:
      title: Proceed?
triggers:
` + triggers
}

// The agent authors triggers through create_workflow / edit_workflow, so the
// trigger rules reach it the way every other validation finding does: in the
// tool's response, and as a block on complete: true.
func TestCreateWorkflowReportsTriggerErrors(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewCreateWorkflowTool(repo)
	ctx := createTestContext(t, "")

	content := workflowWithTriggers(`  - name: nightly
    schedule: {cron: "0 9 * *"}
    filter: "true"
  - name: new-issue
    integration: {integration: github, events: [issues.opened]}
    inputs:
      issue: "{{ trigger.payload.data.issue.number }}"
`)
	complete := true
	input, _ := json.Marshal(CreateWorkflowParams{Content: &content, Complete: &complete})
	resp, err := tool.Run(ctx, ToolCall{ID: "t1", Name: CreateWorkflowToolName, Input: string(input)})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "a complete workflow with trigger errors is rejected: %s", resp.Content)
	assert.Contains(t, resp.Content, "NOT created")
	assert.Contains(t, resp.Content, "triggers[0](nightly).schedule.cron")
	assert.Contains(t, resp.Content, "triggers[0](nightly).filter")
	assert.Contains(t, resp.Content, `triggers[1](new-issue).inputs.issue: "issue" is not a declared input`)

	// As a draft it saves, and the same errors come back so the agent can fix
	// them.
	input, _ = json.Marshal(CreateWorkflowParams{Content: &content})
	resp, err = tool.Run(ctx, ToolCall{ID: "t2", Name: CreateWorkflowToolName, Input: string(input)})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "NOT runnable")
	assert.Contains(t, resp.Content, "triggers[0](nightly).schedule.cron")
}

func TestEditWorkflowReportsTriggerFindings(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := createTestContext(t, "")

	content := workflowWithTriggers(`  - name: new-issue
    integration: {integration: github, events: [issues.opened]}
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
`)
	complete := true
	input, _ := json.Marshal(CreateWorkflowParams{Content: &content, Complete: &complete})
	created, err := NewCreateWorkflowTool(repo).Run(ctx, ToolCall{ID: "c", Name: CreateWorkflowToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, created.IsError, created.Content)
	var meta CreateWorkflowResult
	require.NoError(t, json.Unmarshal([]byte(created.Metadata), &meta))

	// A warning (a literal that looks like an expression) does not block,
	// but the agent is shown it.
	edit, _ := json.Marshal(EditWorkflowParams{
		ID:        meta.ID,
		OldString: `issue_number: "{{ trigger.payload.data.issue.number }}"`,
		NewString: `issue_number: trigger.payload.data.issue.number`,
	})
	resp, err := NewEditWorkflowTool(repo).Run(ctx, ToolCall{ID: "e", Name: ToolEditWorkflow, Input: string(edit)})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Warnings")
	assert.Contains(t, resp.Content, "triggers[0](new-issue).inputs.issue_number")

	// An edit that breaks a trigger on a complete workflow is rejected and
	// leaves it as it was.
	edit, _ = json.Marshal(EditWorkflowParams{
		ID:        meta.ID,
		OldString: `integration: github`,
		NewString: `integration: not_a_real_integration`,
	})
	resp, err = NewEditWorkflowTool(repo).Run(ctx, ToolCall{ID: "e2", Name: ToolEditWorkflow, Input: string(edit)})
	require.NoError(t, err)
	assert.True(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, `"not_a_real_integration" is not in the integration catalog`)

	stored, err := repo.GetWorkflowDraft(context.Background(), meta.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Definition, "integration: github")
}
