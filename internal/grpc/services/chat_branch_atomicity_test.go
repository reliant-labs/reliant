// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// branchAtomicityRepo wraps db.Repository so it satisfies the whole
// interface, but fails the CreateUserUpdate call — the last write inside
// BranchChat's transaction — so the test can assert that nothing earlier in
// the transaction (branch chat row, forked workflow/thread) survives the
// abort.
type branchAtomicityRepo struct {
	db.Repository
	shouldFail bool
}

func (r *branchAtomicityRepo) CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error {
	if r.shouldFail {
		return fmt.Errorf("injected failure: CreateUserUpdate")
	}
	return r.Repository.CreateUserUpdate(ctx, update)
}

// setupBranchAtomicitySource creates a source chat with one message,
// suitable as a branch point, and returns (sourceChatID, messageID).
func setupBranchAtomicitySource(t *testing.T, repo db.Repository, ctx context.Context, projectID string) (string, string) {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.NewString()
	threadID := chatID
	cwID := chatID + ":" + threadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Source Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: now,
		UpdatedAt: now,
	}))

	threadsSvc := threads.NewService(repo)
	_, _, _, err := threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chatID,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       threadID,
			Status:       db.Pending(),
			CreatedAt:    now,
		},
		ThreadID: threadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	msgID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         1,
		Seq:             1,
		CreatedAt:       now,
	}))

	return chatID, msgID
}

// TestBranchChat_AtomicOnMidTransactionFailure proves that a failure on the
// LAST write inside BranchChat's transaction (the chat_created user update)
// leaves NO partial state behind: no orphan branch chat row, no orphan
// forked workflow/thread. Before the transaction wrapper this test fails —
// verified by reverting the wrapper, confirming failure, and restoring it.
func TestBranchChat_AtomicOnMidTransactionFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-branch-atomic-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Atomicity Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	failingRepo := &branchAtomicityRepo{Repository: repo, shouldFail: true}
	service := &ChatService{
		database: failingRepo,
		threads:  threads.NewService(failingRepo),
	}

	// Baseline: chats that exist before the branch attempt.
	before, err := repo.ListChats(ctx, db.ChatFilters{ProjectID: &projectID})
	require.NoError(t, err)
	beforeCount := len(before)

	_, branchErr := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.Error(t, branchErr, "BranchChat must fail when the trailing chat_created write fails")

	after, err := repo.ListChats(ctx, db.ChatFilters{ProjectID: &projectID})
	require.NoError(t, err)
	require.Len(t, after, beforeCount, "no orphan branch chat row should survive a failed transaction")

	updates, err := repo.GetUserUpdatesSince(ctx, "test-user", 0, 100)
	require.NoError(t, err)
	for _, u := range updates {
		require.NotEqual(t, db.UserUpdateChatCreated, u.UpdateType,
			"no chat_created update should exist for a branch that was never committed (entity=%s)", u.EntityID)
	}
}

// TestBranchChat_SucceedsWithoutInjectedFailure is the control for
// TestBranchChat_AtomicOnMidTransactionFailure: with the injected failure
// disabled, branching must succeed and leave a consistent branch chat +
// forked workflow/thread + chat_created update behind.
func TestBranchChat_SucceedsWithoutInjectedFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-branch-atomic-ok-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Atomicity Control Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	failingRepo := &branchAtomicityRepo{Repository: repo, shouldFail: false}
	service := &ChatService{
		database: failingRepo,
		threads:  threads.NewService(failingRepo),
	}

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	branchChatID := resp.Msg.Chat.Id
	branchChat, getErr := repo.GetChat(ctx, branchChatID)
	require.NoError(t, getErr)
	require.NotNil(t, branchChat)

	wf, wfErr := repo.GetWorkflow(ctx, branchChatID)
	require.NoError(t, wfErr)
	require.NotNil(t, wf, "forked root workflow should exist for the branched chat")

	thread, threadErr := repo.GetThread(ctx, branchChatID)
	require.NoError(t, threadErr)
	require.NotNil(t, thread, "forked root thread should exist for the branched chat")

	updates, updatesErr := repo.GetUserUpdatesSince(ctx, "test-user", 0, 100)
	require.NoError(t, updatesErr)
	foundChatCreated := false
	for _, u := range updates {
		if u.UpdateType == db.UserUpdateChatCreated && u.EntityID == branchChatID {
			foundChatCreated = true
		}
	}
	require.True(t, foundChatCreated, "chat_created user update should exist for the successfully created branch")
}
