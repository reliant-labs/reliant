package services

import (
	"context"
	"errors"
	"strings"
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

// The workflow draft lifecycle (specs/workflow-draft-lifecycle.md): a DRAFT is
// work in progress — stored as-is, even invalid, and never runnable. COMPLETE
// passed validation when it was marked complete and is runnable; validation
// blocks becoming (or staying) complete, not every save. Validity itself is
// never stored — it is computed on read.

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

// validLifecycleWorkflow is the fixed version of routerSkipWorkflow: the
// router no longer skips scrape, so the read is safe.
const validLifecycleWorkflow = `
name: save-router-skip
entry: [scrape]
inputs:
  model: {type: model}
nodes:
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

func assertHasNodeOrderingError(t *testing.T, errs []*reliantv1.ValidationError) {
	t.Helper()
	for _, e := range errs {
		if e.Type == "node_ordering" {
			assert.Contains(t, e.Message, "nodes.scrape")
			assert.NotEmpty(t, e.Suggestion)
			return
		}
	}
	t.Fatalf("the unguarded router-skip read must be reported as a structured node_ordering error: %v", errs)
}

func saveWorkflow(t *testing.T, ctx context.Context, svc *WorkflowService, projectID, src string, status reliantv1.WorkflowDraftStatus) *reliantv1.SaveWorkflowResponse {
	t.Helper()
	resp, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID,
		Workflow:  mustParse(t, src),
		Status:    status,
	}))
	require.NoError(t, err)
	return resp.Msg
}

// An invalid workflow saved as a draft (the default intent) is STORED, with
// status draft, and the response carries every error.
func TestSaveWorkflow_InvalidDraftIsStoredWithErrors(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	for _, intent := range []reliantv1.WorkflowDraftStatus{
		reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_UNSPECIFIED,
		reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
	} {
		resp := saveWorkflow(t, ctx, svc, projectID, routerSkipWorkflow, intent)
		require.True(t, resp.Success, "an invalid draft must save (intent %v): %s", intent, resp.Message)
		assert.False(t, resp.IsValid)
		assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, resp.Status)
		assert.Contains(t, resp.Message, "draft")
		assertHasNodeOrderingError(t, resp.ValidationErrors)

		stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
		require.NoError(t, err)
		require.NotNil(t, stored, "an invalid draft is work in progress and must be stored")
		assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)
	}
}

// Intent complete on an invalid workflow is rejected and nothing is stored.
func TestSaveWorkflow_InvalidCompleteIsRejected(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	resp := saveWorkflow(t, ctx, svc, projectID, routerSkipWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE)
	assert.False(t, resp.Success, "an invalid workflow must not be saved as complete")
	assert.False(t, resp.IsValid)
	assert.Contains(t, resp.Message, "not saved")
	assertHasNodeOrderingError(t, resp.ValidationErrors)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
	require.NoError(t, err)
	assert.Nil(t, stored, "a rejected save must persist nothing")
}

// Saving a complete workflow without an explicit intent keeps it complete —
// so a save that introduces an error is rejected, and the stored workflow is
// left as it was (still runnable).
func TestSaveWorkflow_CompleteWorkflowCannotBeMadeInvalid(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	first := saveWorkflow(t, ctx, svc, projectID, validLifecycleWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE)
	require.True(t, first.Success, "%s %v", first.Message, first.ValidationErrors)
	require.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, first.Status)

	resp := saveWorkflow(t, ctx, svc, projectID, routerSkipWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_UNSPECIFIED)
	assert.False(t, resp.Success, "re-saving a complete workflow with an error must be rejected")
	assert.Contains(t, resp.Message, "draft", "the rejection points at save-as-draft")
	assertHasNodeOrderingError(t, resp.ValidationErrors)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, db.WorkflowDraftStatusComplete, stored.Status)
	assert.NotContains(t, stored.Definition, "classify", "the rejected definition must not be persisted")

	// Explicitly saving as a draft is the way to make invalid edits: it stores
	// and takes the workflow out of service.
	asDraft := saveWorkflow(t, ctx, svc, projectID, routerSkipWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT)
	require.True(t, asDraft.Success, asDraft.Message)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, asDraft.Status)
	usable, err := repo.GetUsableWorkflowBySlug(ctx, "test-user", "save-router-skip")
	assert.Nil(t, usable, "a draft is never runnable")
	var notRunnable *db.WorkflowDraftNotRunnableError
	assert.True(t, errors.As(err, &notRunnable), "a draft looked up for running reports it is a draft, got %v", err)
}

// Warnings never block, and are returned with a successful complete save.
func TestSaveWorkflow_WarningsReturnedWithSuccessfulSave(t *testing.T) {
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
	resp := saveWorkflow(t, ctx, svc, projectID, src, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE)
	require.True(t, resp.Success, "%s %v", resp.Message, resp.ValidationErrors)
	assert.True(t, resp.IsValid)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, resp.Status)
	var warned bool
	for _, e := range resp.ValidationErrors {
		if e.Type == "warning:conditional_access" {
			warned = true
		}
	}
	assert.True(t, warned, "warnings must be returned with a successful save: %v", resp.ValidationErrors)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-with-warning")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, db.WorkflowDraftStatusComplete, stored.Status)
}

// SetWorkflowStatus(COMPLETE) validates the current definition: rejected with
// the errors while invalid, applied once valid. Moving back to draft always
// succeeds.
func TestSetWorkflowStatus_MarkCompleteIsGatedByValidation(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	saved := saveWorkflow(t, ctx, svc, projectID, routerSkipWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT)
	require.True(t, saved.Success, saved.Message)

	markComplete := func() *reliantv1.SetWorkflowStatusResponse {
		resp, err := svc.SetWorkflowStatus(ctx, connect.NewRequest(&reliantv1.SetWorkflowStatusRequest{
			ProjectId: projectID,
			DraftId:   saved.Id,
			Status:    reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
		}))
		require.NoError(t, err)
		return resp.Msg
	}

	rejected := markComplete()
	assert.False(t, rejected.Success, "an invalid draft must not be marked complete")
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, rejected.Status)
	assertHasNodeOrderingError(t, rejected.ValidationErrors)
	stored, err := repo.GetWorkflowDraft(ctx, saved.Id)
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)

	fixed := saveWorkflow(t, ctx, svc, projectID, validLifecycleWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_UNSPECIFIED)
	require.True(t, fixed.Success, fixed.Message)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, fixed.Status, "a draft stays a draft until it is marked complete")

	accepted := markComplete()
	require.True(t, accepted.Success, "%s %v", accepted.Message, accepted.ValidationErrors)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, accepted.Status)
	usable, err := repo.GetUsableWorkflowBySlug(ctx, "test-user", "save-router-skip")
	require.NoError(t, err)
	require.NotNil(t, usable, "a complete workflow is runnable")

	back, err := svc.SetWorkflowStatus(ctx, connect.NewRequest(&reliantv1.SetWorkflowStatusRequest{
		ProjectId: projectID,
		DraftId:   saved.Id,
		Status:    reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
	}))
	require.NoError(t, err)
	assert.True(t, back.Msg.Success)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, back.Msg.Status)
}

// ImportWorkflow follows the same rules: an invalid import is stored as a
// draft by default and rejected as complete.
func TestImportWorkflow_InvalidDraftStoredCompleteRejected(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	rejected, err := svc.ImportWorkflow(ctx, connect.NewRequest(&reliantv1.ImportWorkflowRequest{
		ProjectId:   projectID,
		YamlContent: []byte(routerSkipWorkflow),
		Status:      reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
	}))
	require.NoError(t, err)
	assert.False(t, rejected.Msg.Success)
	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
	require.NoError(t, err)
	assert.Nil(t, stored)

	imported, err := svc.ImportWorkflow(ctx, connect.NewRequest(&reliantv1.ImportWorkflowRequest{
		ProjectId:   projectID,
		YamlContent: []byte(routerSkipWorkflow),
	}))
	require.NoError(t, err)
	require.True(t, imported.Msg.Success, imported.Msg.Message)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, imported.Msg.Status)
	assertHasNodeOrderingError(t, imported.Msg.ValidationErrors)
}

// ListWorkflows: the default (chat-safe) listing offers only complete,
// visible workflows that validate NOW. The management listing shows every
// workflow with its status and its findings computed on read — never a
// stored verdict.
func TestListWorkflows_StatusAndComputedValidity(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	now := time.Now().UTC()
	create := func(slug, def string, status db.WorkflowDraftStatus) {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: "test-user",
			Name: slug, Slug: slug, Definition: def, Status: status,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}))
	}
	// Marked complete before the validator got stricter: stale.
	create("save-router-skip", routerSkipWorkflow, db.WorkflowDraftStatusComplete)
	// A valid draft: passes validation, but is not runnable until complete.
	validDraft := strings.Replace(validLifecycleWorkflow, "name: save-router-skip", "name: valid-draft", 1)
	create("valid-draft", validDraft, db.WorkflowDraftStatusDraft)

	def, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	for _, wf := range def.Msg.Workflows {
		assert.NotEqual(t, "save-router-skip", wf.Filename, "a workflow that fails validation now must not be offered for chats")
		assert.NotEqual(t, "valid-draft", wf.Filename, "a draft must not be offered where a runnable workflow is required")
	}

	all, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID, IncludeHidden: true}))
	require.NoError(t, err)
	listed := map[string]*reliantv1.WorkflowListItem{}
	for _, wf := range all.Msg.Workflows {
		listed[wf.Filename] = wf
	}
	stale := listed["save-router-skip"]
	require.NotNil(t, stale, "include_hidden lists every workflow for the management UI")
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, stale.Status)
	assertHasNodeOrderingError(t, stale.ValidationErrors)

	draft := listed["valid-draft"]
	require.NotNil(t, draft)
	assert.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, draft.Status)
	for _, e := range draft.ValidationErrors {
		assert.Contains(t, e.Type, "warning:", "a valid draft carries no errors: %v", e)
	}
}
