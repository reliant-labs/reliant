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
		Seq:             1,
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
		Seq:             2,
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
		Seq:             1,
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

// TestBranchChat_PinsActiveDaemonFromWorktree verifies a branched chat inherits
// ActiveDaemonID from its worktree's owning daemon, so its first message routes
// tool execution to the machine holding the branch checkout (the fix for
// "chatting with a branch chat didn't work").
func TestBranchChat_PinsActiveDaemonFromWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-branch-daemon-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Daemon Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	daemonID := "daemon-owning-branch"
	worktreeID := "wt-" + uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         worktreeID,
		Name:       "feature",
		Path:       t.TempDir(),
		Branch:     "feature",
		ProjectID:  projectID,
		DaemonID:   &daemonID,
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
		Title:      "Source Chat",
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
		Seq:             1,
		CreatedAt:       now,
	}))

	service := &ChatService{database: repo, threads: threadsSvc}

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    chatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	branchedChat, err := repo.GetChat(ctx, resp.Msg.Chat.Id)
	require.NoError(t, err)
	require.NotNil(t, branchedChat.ActiveDaemonID, "branched chat must be pinned to the worktree's daemon")
	require.Equal(t, daemonID, *branchedChat.ActiveDaemonID)
}

// TestBranchChat_MultiThreadChat_ToolCallAdjustment_OrdinalSeqDiverge verifies
// that branching from an assistant message with a pending tool call still
// finds and includes the following tool-result message (chat_branch.go's
// "msg.Ordinal > branchPointMsg.Ordinal && ... ContextWindowID ==" gate) in a
// chat with a second, concurrently-writing thread — the scenario where a
// per-thread ordinal and the chat-global seq numerically diverge, so a
// mistaken flip of that comparison to Seq would hide here rather than in a
// single-thread test.
func TestBranchChat_MultiThreadChat_ToolCallAdjustment_OrdinalSeqDiverge(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-branch-multithread-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Branch Multi-Thread Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	mainThreadID := chatID
	mainCWID := chatID + ":" + mainThreadID + ":0"
	spawnThreadID := uuid.NewString()
	spawnCWID := chatID + ":" + spawnThreadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Multi-thread Chat",
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
			Thread:       mainThreadID,
			Status:       db.WorkflowStatusPending,
			CreatedAt:    now,
		},
		ThreadID: mainThreadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	// A second thread (e.g. a parallel spawned sub-agent) sharing the chat.
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:        spawnThreadID,
		ChatID:    chatID,
		CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:        spawnCWID,
		ThreadID:  spawnThreadID,
		Sequence:  0,
		CreatedAt: now,
	})
	require.NoError(t, err)

	nextSeq := int64(0)
	allocSeq := func() int64 {
		s := nextSeq
		nextSeq++
		return s
	}

	// Interleave writes across the two threads so main-thread ordinal and
	// chat-global seq diverge: spawn-a/spawn-b get seq 0/1 before any main
	// message is written, pushing every main-thread message's seq well past
	// its ordinal.
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: "spawn-a", ChatID: chatID, ThreadID: spawnThreadID, ContextWindowID: spawnCWID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Ordinal: 1, Seq: allocSeq(), CreatedAt: now,
	}))
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: "spawn-b", ChatID: chatID, ThreadID: spawnThreadID, ContextWindowID: spawnCWID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, Ordinal: 2, Seq: allocSeq(), CreatedAt: now,
	}))

	// main-1: assistant message with a tool call (ordinal=1, seq=2)
	assistantMsgID := "main-assistant-with-toolcall"
	toolCallID := "toolcall-1"
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: assistantMsgID, ChatID: chatID, ThreadID: mainThreadID, ContextWindowID: mainCWID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, Ordinal: 1, Seq: allocSeq(), CreatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.NewString(),
		MessageID:  assistantMsgID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		Position:   0,
	}))

	// More spawn-thread traffic between the assistant message and its tool
	// result, so the tool result's seq is even further from its ordinal.
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: "spawn-c", ChatID: chatID, ThreadID: spawnThreadID, ContextWindowID: spawnCWID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Ordinal: 3, Seq: allocSeq(), CreatedAt: now,
	}))

	// main-2: the tool result for main-1 (ordinal=2, seq=4)
	toolResultMsgID := "main-tool-result"
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: toolResultMsgID, ChatID: chatID, ThreadID: mainThreadID, ContextWindowID: mainCWID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, Ordinal: 2, Seq: allocSeq(), CreatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.NewString(),
		MessageID:  toolResultMsgID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolCallID: &toolCallID,
		Position:   0,
	}))

	// Sanity: confirm ordinal and seq genuinely diverge for both messages
	// involved, so this test exercises the divergence it's designed to catch.
	assistantMsg, err := repo.GetMessage(ctx, assistantMsgID)
	require.NoError(t, err)
	toolResultMsg, err := repo.GetMessage(ctx, toolResultMsgID)
	require.NoError(t, err)
	require.NotEqual(t, assistantMsg.Ordinal, assistantMsg.Seq, "test setup invalid: assistant message ordinal must differ from seq")
	require.NotEqual(t, toolResultMsg.Ordinal, toolResultMsg.Seq, "test setup invalid: tool result ordinal must differ from seq")
	t.Logf("assistant: ordinal=%d seq=%d; tool result: ordinal=%d seq=%d",
		assistantMsg.Ordinal, assistantMsg.Seq, toolResultMsg.Ordinal, toolResultMsg.Seq)

	service := &ChatService{database: repo, threads: threadsSvc}

	// ACT: branch from the assistant message (the one with the pending tool call).
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    chatID,
		MessageId: assistantMsgID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	// ASSERT: the branched thread's messages should include the tool result,
	// proving the branch point was auto-adjusted past the assistant message
	// to include the matching tool response — by identity, not by ordinal
	// magnitude.
	branchWorkflowID := resp.Msg.Chat.Id
	msgs, err := threadsSvc.LoadCurrentMessages(ctx, branchWorkflowID)
	require.NoError(t, err)

	var sawAssistant, sawToolResult bool
	for _, m := range msgs {
		switch m.ID {
		case assistantMsgID:
			sawAssistant = true
		case toolResultMsgID:
			sawToolResult = true
		case "spawn-a", "spawn-b", "spawn-c":
			t.Fatalf("FAIL: branched thread inherited spawn-thread message %q, which should not be visible", m.ID)
		}
	}
	require.True(t, sawAssistant, "branched thread should include the assistant message with the tool call")
	require.True(t, sawToolResult, "branched thread should include the tool result message (auto-adjustment past a pending tool call)")
}
