package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
		IsValid:    true,
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
		IsValid:    false,
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
		IsValid:    true,
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
		require.True(t, listedWorkflow.IsValid)
		require.False(t, listedWorkflow.IsHidden)
		require.NotNil(t, listedWorkflow.DraftId)

		err = chatService.validateCreateChatWorkflowTree(ctx, "test-user", listedWorkflow.Filename, projectID)
		require.NoError(t, err, "workflow returned by default ListWorkflows must pass CreateChat workflow resolution")
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
		require.False(t, userWorkflowSlugs["listed-invalid-workflow"].IsValid)
		require.True(t, userWorkflowSlugs["listed-hidden-workflow"].IsHidden)
	})
}
