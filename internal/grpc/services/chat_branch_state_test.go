// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The web client decides "first send is StartChat, not SendMessage" from the
// workflow_state on the chat it was handed. A branch is always pending, so the
// BranchChat response itself must say so, not only a later GetChat.
func TestBranchChatResponseCarriesPendingWorkflowState(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-branch-state-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Branch State", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	sourceChatID, messageID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	service := &ChatService{database: repo, threads: threads.NewService(repo)}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: messageID,
	}))
	require.NoError(t, err)

	require.Equal(t, reliantv1.WorkflowState_WORKFLOW_STATE_PENDING, resp.Msg.Chat.WorkflowState,
		"BranchChat's response must carry PENDING so the first send routes to StartChat")

	got, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: resp.Msg.Chat.Id}))
	require.NoError(t, err)
	require.Equal(t, got.Msg.Chat.WorkflowState, resp.Msg.Chat.WorkflowState,
		"the response and a fresh GetChat must agree")
}
