package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestChatService_CreateChat_EarlyWorkflowTreeValidationFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-chat-create-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Chat Create Validation Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	invalidWorkflow := strings.TrimSpace(`
name: invalid-spawn-tree
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
      tools_config:
        spawn:
          - "spawn:builtin://definitely-missing(general)"
`)

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:               uuid.NewString(),
		UserID:           "test-user",
		Name:             "Invalid Spawn Tree",
		Slug:             "invalid-spawn-tree",
		Definition:       invalidWorkflow,
		IsValid:          true,
		ValidationErrors: nil,
		CreatedAt:        now,
		UpdatedAt:        now,
		IsHidden:         false,
		Version:          1,
	}))

	service := &ChatService{database: repo}

	_, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "invalid-spawn-tree",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
	}))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	require.Contains(t, connectErr.Message(), "workflow tree validation failed")
	require.Contains(t, connectErr.Message(), "invalid-spawn-tree")

	chats, listErr := repo.ListChats(ctx, db.ChatFilters{ProjectID: &projectID})
	require.NoError(t, listErr)
	require.Len(t, chats, 0, "chat should not be created when workflow validation fails")
}

func TestChatService_ValidateWorkflowInputs_RejectsMissingRequiredWorkflowParams(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	requiredWorkflow := strings.TrimSpace(`
name: required-param-workflow
apiVersion: v2
inputs:
  prompt:
    type: string
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo {{inputs.prompt}}"
`)

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:               uuid.NewString(),
		UserID:           "test-user",
		Name:             "Required Param Workflow",
		Slug:             "required-param-workflow",
		Definition:       requiredWorkflow,
		IsValid:          true,
		ValidationErrors: nil,
		CreatedAt:        now,
		UpdatedAt:        now,
		IsHidden:         false,
		Version:          1,
	}))

	service := &ChatService{database: repo}
	validationErrors := service.validateWorkflowInputs(ctx, "required-param-workflow", "", map[string]interface{}{})

	require.NotEmpty(t, validationErrors)
	require.Contains(t, validationErrors[0].Error(), "required input 'prompt' is not provided")
}

func TestChatService_CreateChat_EarlyWorkflowTreeValidationSuccess(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-chat-create-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Chat Create Validation Success Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	validWorkflow := strings.TrimSpace(`
name: valid-spawn-tree
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
      tools_config:
        spawn:
          - "spawn:builtin://agent(general)"
`)

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:               uuid.NewString(),
		UserID:           "test-user",
		Name:             "Valid Spawn Tree",
		Slug:             "valid-spawn-tree",
		Definition:       validWorkflow,
		IsValid:          true,
		ValidationErrors: nil,
		CreatedAt:        now,
		UpdatedAt:        now,
		IsHidden:         false,
		Version:          1,
	}))

	service := &ChatService{database: repo}
	err := service.validateCreateChatWorkflowTree(ctx, "test-user", "valid-spawn-tree", "")
	require.NoError(t, err)
}

func TestChatService_CreateChat_WorkflowNameSpawnUsesCanonicalReference(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-chat-create-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Chat Create Canonical Workflow Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	workflowWithWorkflowNameSpawn := strings.TrimSpace(`
name: agent
apiVersion: v2
inputs:
  model:
    type: model
  spawn_presets:
    type: preset
    tags: [agent]
    multi: true
    default: [general]
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
      tools_config:
        spawn: "{{['spawn:' + spawn(workflow.name, inputs.spawn_presets)]}}"
`)

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:               uuid.NewString(),
		UserID:           "test-user",
		Name:             "Agent",
		Slug:             "agent",
		Definition:       workflowWithWorkflowNameSpawn,
		IsValid:          true,
		ValidationErrors: nil,
		CreatedAt:        now,
		UpdatedAt:        now,
		IsHidden:         false,
		Version:          1,
	}))

	service := &ChatService{database: repo}
	err := service.validateCreateChatWorkflowTree(ctx, "test-user", "agent", "")
	require.NoError(t, err)
}
