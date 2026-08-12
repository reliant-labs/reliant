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
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// TestChatSnapshot_CarriesSpawnChildWorkflowID pins the bug that made a LIVE
// spawn preview render "Starting…" for its entire run.
//
// The chat snapshot is what a client receives while work is happening. It used
// to convert messages with SequenceNumber alone -- no durable tool-call status,
// and therefore no child_workflow_id -- so a spawn's tool-call block arrived
// with nothing naming the thread it owned, and its preview had nothing to
// read. Reloading the same chat through ListMessages showed the transcript,
// because THAT path enriched the blocks. Two read paths for one screen, and
// the one that was wrong was the one serving live work.
//
// Both paths now assemble through assembleMessagesForDisplay. This test asserts
// the snapshot's output directly so the two cannot drift apart again.
func TestChatSnapshot_CarriesSpawnChildWorkflowID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	cwID := uuid.New().String()
	childThreadID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "live spawn", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	// An assistant message carrying a spawn tool call.
	msgID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: mainThreadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"child agent"}`),
		CreatedAt:  now, UpdatedAt: now,
	}))

	// The durable tool-call row: still EXECUTING, and it knows its child.
	// This is the state a spawn is in for the whole time its preview matters.
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &mainThreadID, MessageID: &msgID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	svc := NewStreamingService(repo, nil, nil, nil)
	snapshot, _, err := svc.buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	var spawnBlock *reliantv1.ContentBlock
	for _, m := range snapshot.Messages {
		for _, b := range m.ContentBlocks {
			if b.ToolCallId != nil && *b.ToolCallId == toolCallID {
				spawnBlock = b
			}
		}
	}
	require.NotNil(t, spawnBlock, "snapshot must carry the spawn tool-call block")

	require.NotNil(t, spawnBlock.ChildWorkflowId,
		"the snapshot must name the thread the spawn owns; without it a live spawn preview has nothing to read and renders \"Starting…\" for the whole run")
	require.Equal(t, childThreadID, *spawnBlock.ChildWorkflowId)

	require.NotNil(t, spawnBlock.ToolCallStatus, "durable status must survive the snapshot")
	require.Equal(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING, *spawnBlock.ToolCallStatus)
}

// TestChatSnapshot_MatchesListMessagesEnrichment states the invariant the
// shared assembler exists to hold: the live path and the reload path describe
// the same tool call the same way. A difference here is a user-visible
// inconsistency -- the screen changes when you reload it.
func TestChatSnapshot_MatchesListMessagesEnrichment(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	cwID := uuid.New().String()
	childThreadID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "parity", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: mainThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	msgID := uuid.New().String()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msgID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: mainThreadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: msgID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"child agent"}`),
		CreatedAt:  now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &mainThreadID, MessageID: &msgID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	findSpawnBlock := func(msgs []*reliantv1.Message) *reliantv1.ContentBlock {
		for _, m := range msgs {
			for _, b := range m.ContentBlocks {
				if b.ToolCallId != nil && *b.ToolCallId == toolCallID {
					return b
				}
			}
		}
		return nil
	}

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)
	fromSnapshot := findSpawnBlock(snapshot.Messages)
	require.NotNil(t, fromSnapshot)

	recent := int32(50)
	resp, err := (&ChatService{database: repo}).ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: chatID, Recent: &recent,
	}))
	require.NoError(t, err)
	fromList := findSpawnBlock(resp.Msg.Messages)
	require.NotNil(t, fromList)

	require.Equal(t, fromList.ChildWorkflowId, fromSnapshot.ChildWorkflowId,
		"live and reload paths must agree on which thread the spawn owns")
	require.Equal(t, fromList.ToolCallStatus, fromSnapshot.ToolCallStatus,
		"live and reload paths must agree on tool-call status")
}
