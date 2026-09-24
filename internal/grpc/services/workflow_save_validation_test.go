package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SaveWorkflow validates exactly as run start will, and validation ERRORS
// BLOCK THE SAVE: nothing is persisted, and the response carries every error.
// A stored draft is runnable, so saving a broken one only defers the failure
// to whoever runs it next.

func saveTestSetup(t *testing.T) (context.Context, db.Repository, *WorkflowService, string) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-save-validation-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Save Validation Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return ctx, repo, &WorkflowService{database: repo}, projectID
}

func mustParse(t *testing.T, src string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return wf
}

// The F1 shape from specs/template-validation-gaps.md: a router can skip a
// node that a later inject reads unguarded.
const routerSkipWorkflow = `
name: save-router-skip
entry: [classify]
inputs:
  model: {type: model}
nodes:
  - id: classify
    type: router
    model: "{{inputs.model}}"
    nodes:
      - {id: scrape, description: "scrape first"}
      - {id: plan, description: "plan directly"}
  - id: scrape
    type: call_llm
    args: {model: "{{inputs.model}}"}
  - id: plan
    type: call_llm
    args:
      model: "{{inputs.model}}"
      system_prompt: "Use this research: {{nodes.scrape.response_text}}"
edges:
  - {from: scrape, default: [plan]}
`

func TestSaveWorkflow_ValidationErrorsBlockTheSave(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	resp, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID,
		Workflow:  mustParse(t, routerSkipWorkflow),
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success, "a workflow with validation errors must not save")
	assert.False(t, resp.Msg.IsValid)
	assert.Contains(t, resp.Msg.Message, "not saved")
	require.NotEmpty(t, resp.Msg.ValidationErrors)
	var found bool
	for _, e := range resp.Msg.ValidationErrors {
		if e.Type == "node_ordering" {
			found = true
			assert.Contains(t, e.Message, "nodes.scrape")
			assert.NotEmpty(t, e.Suggestion)
		}
	}
	assert.True(t, found, "the unguarded router-skip read must be reported as a structured node_ordering error: %v", resp.Msg.ValidationErrors)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
	require.NoError(t, err)
	assert.Nil(t, stored, "a rejected save must persist nothing")
}

func TestSaveWorkflow_ValidWorkflowSavesAndReturnsWarnings(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	// Guarded: valid. The conditional run node read unguarded is a warning
	// (the skip output zero-fills exit_code), returned but not blocking.
	src := `
name: save-with-warning
entry: [start]
inputs:
  lint: {type: boolean, default: false}
nodes:
  - {id: start, type: save_message, args: {role: user, content: go}}
  - id: lint
    type: run
    condition: "inputs.lint"
    command: "true"
  - id: report
    type: save_message
    args:
      role: assistant
      content: "lint exit: {{nodes.lint.exit_code}}"
edges:
  - {from: start, default: [lint]}
  - {from: lint, default: [report]}
`
	resp, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID,
		Workflow:  mustParse(t, src),
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, "%s %v", resp.Msg.Message, resp.Msg.ValidationErrors)
	assert.True(t, resp.Msg.IsValid)
	var warned bool
	for _, e := range resp.Msg.ValidationErrors {
		if e.Type == "warning:conditional_access" {
			warned = true
		}
	}
	assert.True(t, warned, "warnings must be returned with a successful save: %v", resp.Msg.ValidationErrors)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-with-warning")
	require.NoError(t, err)
	require.NotNil(t, stored)
}

// ListWorkflows decides validity by validating now, not from the stored flag,
// which goes stale when the validator gets stricter.
func TestListWorkflows_StaleInvalidDraftIsHiddenAndMarkedInvalid(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user",
		Name: "save-router-skip", Slug: "save-router-skip",
		Definition: routerSkipWorkflow,
		IsValid:    true, // stale: saved before the check existed
		CreatedAt:  now, UpdatedAt: now, Version: 1,
	}))

	def, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	for _, wf := range def.Msg.Workflows {
		assert.NotEqual(t, "save-router-skip", wf.Filename, "a draft that fails validation now must not be offered for chats")
	}

	all, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID, IncludeHidden: true}))
	require.NoError(t, err)
	var listed *reliantv1.WorkflowListItem
	for _, wf := range all.Msg.Workflows {
		if wf.Filename == "save-router-skip" {
			listed = wf
		}
	}
	require.NotNil(t, listed, "include_hidden lists every draft for the management UI")
	assert.False(t, listed.IsValid, "IsValid is computed on read, not copied from the stale stored flag")
}
