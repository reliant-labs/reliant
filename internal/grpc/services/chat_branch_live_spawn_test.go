// Copyright (c) 2025 Reliant Labs
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
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// liveSpawnFixture is a chat whose latest assistant message requested a spawn
// that is still running: the tool_call row is EXECUTING with a child workflow,
// and no tool_result block exists yet because the sub-agent hasn't finished.
type liveSpawnFixture struct {
	chatID       string
	threadID     string
	assistantMsg string
	toolCallID   string
}

func setupLiveSpawnChat(t *testing.T, repo *db.Repo, ctx context.Context, projectID string) liveSpawnFixture {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.NewString()
	threadID := chatID
	cwID := chatID + ":" + threadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Chat with live spawn",
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
			Status:       db.Active(),
			CreatedAt:    now,
		},
		ThreadID: threadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	userMsgID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         1,
		Seq:             1,
		CreatedAt:       now,
	}))

	// The assistant message that requested the spawn.
	assistantMsgID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              assistantMsgID,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         2,
		Seq:             2,
		CreatedAt:       now,
	}))

	toolCallID := "toolu_" + uuid.NewString()
	toolName := "spawn"
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.NewString(),
		MessageID:  assistantMsgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolCallID: &toolCallID,
		IsComplete: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	// The durable row says this call is alive: EXECUTING, with the child
	// workflow it started. This is what distinguishes a running spawn from a
	// tool call abandoned by a killed run.
	childWorkflowID := uuid.NewString()
	startedAt := now
	require.NoError(t, db.UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:              toolCallID,
		ChatID:          chatID,
		ThreadID:        &threadID,
		MessageID:       &assistantMsgID,
		ToolName:        "spawn",
		Status:          core.ToolCallStatusExecuting,
		ChildWorkflowID: &childWorkflowID,
		StartedAt:       &startedAt,
		RequestedAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	return liveSpawnFixture{
		chatID:       chatID,
		threadID:     threadID,
		assistantMsg: assistantMsgID,
		toolCallID:   toolCallID,
	}
}

func newBranchTestProject(t *testing.T, repo *db.Repo, ctx context.Context) string {
	t.Helper()
	now := time.Now().UTC()
	projectID := "test-project-live-spawn-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Live Spawn Branch Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	return projectID
}

// Branching at a message whose spawn is still running must not touch the source
// conversation. The spawn is going to deliver a real result to that thread; a
// synthetic "interrupted" result written there is a lie that both the source and
// the branch then display (the branch inherits the source's messages through the
// context-window chain rather than copying them), and it collides with the real
// result when the sub-agent finishes.
func TestBranchChat_LiveSpawn_DoesNotRepairSourceThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := newBranchTestProject(t, repo, ctx)
	f := setupLiveSpawnChat(t, repo, ctx, projectID)

	service := &ChatService{database: repo, threads: threads.NewService(repo)}

	_, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    f.chatID,
		MessageId: f.assistantMsg,
	}))
	require.NoError(t, err)

	// No synthetic tool_result may exist for the running spawn.
	resultBlock, err := repo.GetToolResultBlock(ctx, f.toolCallID)
	require.NoError(t, err)
	require.Nil(t, resultBlock,
		"branching must not synthesize a tool_result for a spawn that is still running")

	// The durable status must still say the call is alive.
	call, err := repo.GetToolCall(ctx, f.toolCallID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusExecuting, call.Status,
		"branching must not move a running spawn out of EXECUTING")

	// And no extra tool-role message may have been appended to the source thread.
	msgs, err := repo.ListMessages(ctx, f.chatID, db.MessageListOptions{Limit: 100})
	require.NoError(t, err)
	for _, m := range msgs {
		require.NotEqual(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, m.Role,
			"branching must not append a repair tool message to the source thread")
	}
}

// The branch inherits the assistant message carrying the live spawn, and with it
// the spawn's durable EXECUTING status -- but the spawn is running in the SOURCE
// thread and will deliver its result there. Nothing will ever resolve it here, so
// the branch must not present it as in-flight work of its own.
func TestBranchChat_LiveSpawn_BranchDoesNotShowSpawnAsRunning(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := newBranchTestProject(t, repo, ctx)
	f := setupLiveSpawnChat(t, repo, ctx, projectID)

	service := &ChatService{database: repo, threads: threads.NewService(repo)}

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    f.chatID,
		MessageId: f.assistantMsg,
	}))
	require.NoError(t, err)

	listed, err := service.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: resp.Msg.Chat.Id,
	}))
	require.NoError(t, err)

	for _, m := range listed.Msg.Messages {
		for _, b := range m.ContentBlocks {
			if b.ToolCallId == nil || *b.ToolCallId != f.toolCallID {
				continue
			}
			require.NotNil(t, b.ToolCallStatus,
				"the inherited spawn block should carry an explicit status")
			require.NotEqual(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING, *b.ToolCallStatus,
				"the branch must not show a spawn running in the source thread as its own in-flight work")
		}
	}
}

// The mirror of the test above: in the thread that actually owns the spawn, it is
// genuinely running and must still read that way. The branch-aware
// reinterpretation must not leak into the source conversation.
func TestBranchChat_LiveSpawn_SourceStillShowsSpawnRunning(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := newBranchTestProject(t, repo, ctx)
	f := setupLiveSpawnChat(t, repo, ctx, projectID)

	service := &ChatService{database: repo, threads: threads.NewService(repo)}

	_, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    f.chatID,
		MessageId: f.assistantMsg,
	}))
	require.NoError(t, err)

	listed, err := service.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: f.chatID,
	}))
	require.NoError(t, err)

	found := false
	for _, m := range listed.Msg.Messages {
		for _, b := range m.ContentBlocks {
			if b.ToolCallId == nil || *b.ToolCallId != f.toolCallID {
				continue
			}
			found = true
			require.NotNil(t, b.ToolCallStatus)
			require.Equal(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING, *b.ToolCallStatus,
				"the thread that owns the spawn must still show it as running")
		}
	}
	require.True(t, found, "expected to find the spawn tool call block in the source chat")
}

// A tool call with no result and no live child workflow IS genuinely orphaned --
// a run killed mid-execution. That call still needs a synthetic result, because
// an assistant message with an unpaired tool_use block is an invalid LLM request.
// This is the case the repair path exists for, and it must keep working.
func TestBranchChat_DeadToolCall_StillRepaired(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := newBranchTestProject(t, repo, ctx)

	chatID := uuid.NewString()
	threadID := chatID
	cwID := chatID + ":" + threadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Chat with dead tool call",
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

	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              uuid.NewString(),
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         1,
		Seq:             1,
		CreatedAt:       now,
	}))

	assistantMsgID := uuid.NewString()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              assistantMsgID,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         2,
		Seq:             2,
		CreatedAt:       now,
	}))

	// A bash call with no result and no tool_calls row at all: the run died
	// before recording anything. Nothing will ever answer it.
	toolCallID := "toolu_" + uuid.NewString()
	toolName := "bash"
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.NewString(),
		MessageID:  assistantMsgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolCallID: &toolCallID,
		IsComplete: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	service := &ChatService{database: repo, threads: threadsSvc}

	_, err = service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    chatID,
		MessageId: assistantMsgID,
	}))
	require.NoError(t, err)

	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock,
		"a genuinely orphaned tool call must still be repaired so the history is valid")
	require.NotNil(t, resultBlock.IsError)
	require.True(t, *resultBlock.IsError)
}
