package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestChatService_CreateChat_RejectsDottedWorkflowParams(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-create-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "CreateChat Workflow Params Validation",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	service := &ChatService{database: repo}

	_, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"agent.model": mustStructValue(t, map[string]interface{}{"id": "gpt-4o"}),
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "agent.model")
	require.Contains(t, err.Error(), "nested objects")
}

func TestChatService_SendMessage_RejectsDottedWorkflowParamsForActiveWorkflowUpdate(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-send-" + uuid.NewString()
	chatID := "test-chat-send-" + uuid.NewString()
	workflowID := "test-workflow-send-" + uuid.NewString()
	now := time.Now().UTC()

	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "SendMessage Workflow Params Validation",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     "test-user",
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	service := &ChatService{database: repo}

	_, err := service.SendMessage(ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "continue",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"agent.model": mustStructValue(t, map[string]interface{}{"id": "gpt-4o"}),
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "agent.model")
	require.Contains(t, err.Error(), "nested objects")
}

func TestChatService_UpdateWorkflowParams_RejectsDottedKeysForActiveWorkflowUpdate(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-update-" + uuid.NewString()
	chatID := "test-chat-update-" + uuid.NewString()
	workflowID := "test-workflow-update-" + uuid.NewString()
	now := time.Now().UTC()

	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "UpdateWorkflowParams Validation",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     "test-user",
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	service := &ChatService{database: repo}

	_, err := service.UpdateWorkflowParams(ctx, connect.NewRequest(&reliantv1.UpdateWorkflowParamsRequest{
		ChatId: chatID,
		Params: map[string]*structpb.Value{
			"agent.model": mustStructValue(t, map[string]interface{}{"id": "gpt-4o"}),
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "agent.model")
	require.Contains(t, err.Error(), "nested objects")
}

// mustStructValue creates a structpb.Value from a map for model selector objects in tests.
func mustStructValue(t *testing.T, m map[string]interface{}) *structpb.Value {
	t.Helper()
	v, err := structpb.NewValue(m)
	require.NoError(t, err)
	return v
}
