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
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// TestBranchChat_InheritedMessage_UsesRequestingChatWorktree verifies that
// branching from an inherited message (originating in a parent chat) uses the
// requesting chat's worktree, not the message's original chat's worktree.
//
// Scenario:
//  1. Chat A lives in worktree W1, has messages M1, M2
//  2. Chat A is branched to Chat B in worktree W2
//  3. Chat B inherits M1 and M2 from Chat A
//  4. User branches from M1 while viewing Chat B (simple branch, no explicit worktreeId)
//  5. Expected: new branch gets worktree W2 (Chat B's worktree)
//  6. Bug (before fix): new branch got worktree W1 (M1's original chat's worktree)
func TestBranchChat_InheritedMessage_UsesRequestingChatWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-branch-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Test Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	// Create two worktrees
	worktreeID1 := "wt-1-" + uuid.NewString()
	worktreeID2 := "wt-2-" + uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         worktreeID1,
		Name:       "main",
		Path:       t.TempDir(),
		Branch:     "main",
		ProjectID:  projectID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         worktreeID2,
		Name:       "feature-x",
		Path:       t.TempDir(),
		Branch:     "feature-x",
		ProjectID:  projectID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	// ---- Chat A: worktree W1 ----
	chatAID := uuid.NewString()
	threadAID := chatAID
	cwAID := chatAID + ":" + threadAID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatAID,
		UserID:     "test-user",
		Title:      "Chat A",
		ProjectID:  projectID,
		WorktreeID: &worktreeID1,
		State:      db.ChatStateIdle,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	threadsSvc := threads.NewService(repo)
	_, _, _, err := threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chatAID,
			ChatID:       chatAID,
			WorkflowName: "builtin://agent",
			Thread:       threadAID,
			Status:       db.WorkflowStatusPending,
			CreatedAt:    now,
		},
		ThreadID: threadAID,
		ChatID:   chatAID,
	})
	require.NoError(t, err)

	// Add messages to Chat A
	msgM1ID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              msgM1ID,
		ChatID:          chatAID,
		ThreadID:        threadAID,
		ContextWindowID: cwAID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         1,
		CreatedAt:       now,
	}))

	msgM2ID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              msgM2ID,
		ChatID:          chatAID,
		ThreadID:        threadAID,
		ContextWindowID: cwAID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         2,
		CreatedAt:       now,
	}))

	// ---- Chat B: branched from Chat A at M2, using worktree W2 ----
	chatBID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatBID,
		UserID:     "test-user",
		Title:      "Chat B",
		ProjectID:  projectID,
		WorktreeID: &worktreeID2,
		State:      db.ChatStateIdle,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	threadBID := chatBID
	_, _, _, err = threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chatBID,
			ChatID:       chatBID,
			WorkflowName: "builtin://agent",
			Thread:       threadBID,
			Status:       db.WorkflowStatusPending,
			CreatedAt:    now,
		},
		ThreadID:        threadBID,
		ChatID:          chatBID,
		ForkFromMessage: &msgM2ID,
	})
	require.NoError(t, err)

	// ---- Now branch from M1 while viewing Chat B ----
	// M1 belongs to Chat A, but the user is in Chat B.
	// The branch should inherit Chat B's worktree (W2), not Chat A's (W1).
	service := &ChatService{
		database: repo,
		threads:  threadsSvc,
	}

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    chatBID, // User is viewing Chat B
		MessageId: msgM1ID, // Branching from M1 (which belongs to Chat A)
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	// Verify the branched chat has Chat B's worktree (W2), NOT Chat A's (W1)
	branchedChat, err := repo.GetChat(ctx, resp.Msg.Chat.Id)
	require.NoError(t, err)
	require.NotNil(t, branchedChat.WorktreeID, "branched chat should have a worktree")
	require.Equal(t, worktreeID2, *branchedChat.WorktreeID,
		"branched chat should inherit the requesting chat's worktree (W2), not the message's original chat's worktree (W1)")
}

// TestBranchChat_SameChat_UsesSourceWorktree verifies that branching from a
// message in the same chat (not inherited) still uses that chat's worktree.
func TestBranchChat_SameChat_UsesSourceWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-branch-same-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Same Chat Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	worktreeID := "wt-" + uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         worktreeID,
		Name:       "main",
		Path:       t.TempDir(),
		Branch:     "main",
		ProjectID:  projectID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	threadID := chatID
	cwID := chatID + ":" + threadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     "test-user",
		Title:      "Test Chat",
		ProjectID:  projectID,
		WorktreeID: &worktreeID,
		State:      db.ChatStateIdle,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	threadsSvc := threads.NewService(repo)
	_, _, _, err := threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chatID,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       threadID,
			Status:       db.WorkflowStatusPending,
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
		CreatedAt:       now,
	}))

	service := &ChatService{
		database: repo,
		threads:  threadsSvc,
	}

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    chatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	branchedChat, err := repo.GetChat(ctx, resp.Msg.Chat.Id)
	require.NoError(t, err)
	require.NotNil(t, branchedChat.WorktreeID, "branched chat should have a worktree")
	require.Equal(t, worktreeID, *branchedChat.WorktreeID,
		"branching from same chat should keep the same worktree")
}
