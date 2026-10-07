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
	"github.com/stretchr/testify/require"
)

func TestWorkflowService_ListWorkflows_OnlyReturnsCreateChatUsableUserWorkflows(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-workflow-list-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Workflow Listing Contract Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	validWorkflow := `
name: listed-valid-workflow
apiVersion: v2
inputs:
  model:
    type: model
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`
	invalidWorkflow := `
name: listed-invalid-workflow
apiVersion: v2
entry: [broken]
nodes:
  - id: broken
    type: not_a_real_node
`

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     "test-user",
		Name:       "Listed Valid Workflow",
		Slug:       "listed-valid-workflow",
		Definition: validWorkflow,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
		IsHidden:   false,
		Version:    1,
	}))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     "test-user",
		Name:       "Listed Invalid Workflow",
		Slug:       "listed-invalid-workflow",
		Definition: invalidWorkflow,
		Status:     db.WorkflowDraftStatusDraft,
		CreatedAt:  now,
		UpdatedAt:  now,
		IsHidden:   false,
		Version:    1,
	}))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     "test-user",
		Name:       "Listed Hidden Workflow",
		Slug:       "listed-hidden-workflow",
		Definition: validWorkflow,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
		IsHidden:   true,
		Version:    1,
	}))

	workflowService := &WorkflowService{database: repo}
	chatService := &ChatService{database: repo}

	t.Run("default listing is create-chat safe", func(t *testing.T) {
		resp, err := workflowService.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
			ProjectId: projectID,
		}))
		require.NoError(t, err)

		userWorkflowSlugs := make(map[string]*reliantv1.WorkflowListItem)
		for _, workflow := range resp.Msg.Workflows {
			if workflow.Source == "user" {
				userWorkflowSlugs[workflow.Filename] = workflow
			}
		}

		require.Contains(t, userWorkflowSlugs, "listed-valid-workflow")
		require.NotContains(t, userWorkflowSlugs, "listed-invalid-workflow")
		require.NotContains(t, userWorkflowSlugs, "listed-hidden-workflow")

		listedWorkflow := userWorkflowSlugs["listed-valid-workflow"]
		require.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, listedWorkflow.Status)
		require.False(t, listedWorkflow.IsHidden)
		require.NotNil(t, listedWorkflow.DraftId)

		err = chatService.launcher().ValidateCreateChatWorkflowTree(ctx, "test-user", listedWorkflow.Filename, projectID)
		require.NoError(t, err, "workflow returned by default ListWorkflows must pass StartChat workflow resolution")
	})

	t.Run("include_hidden exposes management drafts for workflow hub", func(t *testing.T) {
		resp, err := workflowService.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
			ProjectId:     projectID,
			IncludeHidden: true,
		}))
		require.NoError(t, err)

		userWorkflowSlugs := make(map[string]*reliantv1.WorkflowListItem)
		for _, workflow := range resp.Msg.Workflows {
			if workflow.Source == "user" {
				userWorkflowSlugs[workflow.Filename] = workflow
			}
		}

		require.Contains(t, userWorkflowSlugs, "listed-valid-workflow")
		require.Contains(t, userWorkflowSlugs, "listed-invalid-workflow")
		require.Contains(t, userWorkflowSlugs, "listed-hidden-workflow")
		invalid := userWorkflowSlugs["listed-invalid-workflow"]
		require.Equal(t, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT, invalid.Status)
		require.NotEmpty(t, invalid.ValidationErrors, "findings are computed on read")
		require.True(t, userWorkflowSlugs["listed-hidden-workflow"].IsHidden)
	})
}

// A workflow with its Chat trigger off (automation_only) is still a runnable
// workflow — automations pick it from the default listing — but the item
// says so, which is what every chat picker filters on, and a chat start of
// it is refused. The item also carries the declared triggers, so a list can
// show the ones nobody has activated.
func TestWorkflowService_ListWorkflows_MarksAutomationOnlyWorkflows(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-automation-only-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Automation Only Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	definition := func(name string, automationOnly bool) string {
		flag := ""
		if automationOnly {
			flag = "automation_only: true\n"
		}
		return "name: " + name + "\napiVersion: v2\n" + flag + `inputs:
  model:
    type: model
entry: [ask]
triggers:
  - name: nightly
    schedule: { cron: ["0 9 * * 1-5"], timezone: UTC }
    prompt: Summarise yesterday.
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`
	}
	for _, wf := range []struct {
		slug           string
		automationOnly bool
	}{{"nightly-digest", true}, {"chatty-digest", false}} {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: "test-user", Name: wf.slug, Slug: wf.slug,
			Definition: definition(wf.slug, wf.automationOnly), Status: db.WorkflowDraftStatusComplete,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}))
	}

	workflowService := &WorkflowService{database: repo}
	chatService := &ChatService{database: repo}

	resp, err := workflowService.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	bySlug := map[string]*reliantv1.WorkflowListItem{}
	for _, item := range resp.Msg.Workflows {
		bySlug[item.Filename] = item
	}

	require.Contains(t, bySlug, "nightly-digest", "an automation-only workflow is still listed for automations to pick")
	require.True(t, bySlug["nightly-digest"].AutomationOnly)
	require.False(t, bySlug["chatty-digest"].AutomationOnly)
	require.Len(t, bySlug["nightly-digest"].Triggers, 1)
	require.Equal(t, "nightly", bySlug["nightly-digest"].Triggers[0].GetName())
	require.Equal(t, "Summarise yesterday.", bySlug["nightly-digest"].Triggers[0].GetPrompt())

	err = chatService.launcher().ValidateCreateChatWorkflowTree(ctx, "test-user", "nightly-digest", projectID)
	require.Error(t, err, "a chat start of an automation-only workflow must be refused")
	require.Contains(t, err.Error(), "cannot be started from a chat")
	require.NoError(t, chatService.launcher().ValidateCreateChatWorkflowTree(ctx, "test-user", "chatty-digest", projectID))
}
