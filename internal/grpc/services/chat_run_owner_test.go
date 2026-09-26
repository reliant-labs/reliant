// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The root run a chat is created with records its owner at creation. These go
// through the RPC rather than the store, because the RPC is the thing that can
// forget to set it.

func newRunOwnerProject(t *testing.T, ctx context.Context, repo *db.Repo) string {
	t.Helper()
	now := time.Now().UTC()
	projectID := "test-project-run-owner-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Run Owner Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return projectID
}

func requireRunOwner(t *testing.T, repo *db.Repo, runID, want string) {
	t.Helper()
	run, err := repo.GetWorkflow(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, run, "run %s was not created", runID)
	require.NotNil(t, run.OwnerUserID, "run %s was created without an owner", runID)
	require.Equal(t, want, *run.OwnerUserID)
}

func TestChatService_CreateChat_RootRunCarriesOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	// CreateChat refuses a project without a main worktree.
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)

	temporal := &atomicityTestTemporalClient{}
	service := &ChatService{
		database:   repo,
		threads:    threads.NewService(repo),
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	resp, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{"id": "mock"}),
		},
	}))
	require.NoError(t, err)

	// The root run's id is the chat's id.
	requireRunOwner(t, repo, resp.Msg.GetChat().GetId(), "test-user")
}

func TestChatService_BranchChat_RootRunCarriesOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := newRunOwnerProject(t, ctx, repo)
	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	service := &ChatService{
		database: repo,
		threads:  threads.NewService(repo),
	}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)

	requireRunOwner(t, repo, resp.Msg.GetChat().GetId(), "test-user")
}
