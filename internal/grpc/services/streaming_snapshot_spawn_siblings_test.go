// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// spawnSiblingsFixture is a chat whose main thread started a spawn that then
// out-wrote it, with a workflow-node thread in between — the three kinds of
// thread a snapshot has to treat differently.
type spawnSiblingsFixture struct {
	chatID        string
	mainThreadID  string
	spawnThreadID string
	spawnCallID   string
	mainMsgIDs    []string
	nodeMsgIDs    []string
	spawnMsgIDs   []string
}

// seedSpawnSiblingsChat writes, in seq order: mainBefore main-thread messages
// (the last of which carries the spawn tool call), two node-thread messages,
// spawnCount spawn-thread messages, and one closing main-thread message.
func seedSpawnSiblingsChat(t *testing.T, repo *db.Repo, ctx context.Context, mainBefore, spawnCount int) spawnSiblingsFixture {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	f := spawnSiblingsFixture{
		chatID:        chatID,
		mainThreadID:  chatID,
		spawnThreadID: uuid.New().String(),
		spawnCallID:   "toolu_" + uuid.New().String()[:8],
	}
	nodeThreadID := uuid.New().String()

	// The chat's workflow id IS its main thread id; without it the snapshot
	// has no main thread to measure its window against.
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "spawn siblings", ProjectID: "test-project", UserID: "test-user",
		WorkflowID: &chatID,
		CreatedAt:  now, UpdatedAt: now, LastActive: now,
	}))

	spawnTitle := "audit the snapshot"
	nodeID := "review"
	threads := []*db.Thread{
		{ID: f.mainThreadID, ChatID: chatID, Origin: db.ThreadOriginMain, CreatedAt: now},
		{
			ID: f.spawnThreadID, ChatID: chatID, ParentThreadID: &f.mainThreadID,
			WorkflowID: &f.spawnThreadID, Title: &spawnTitle,
			Origin: db.ThreadOriginSpawn, CreatedAt: now,
		},
		{
			ID: nodeThreadID, ChatID: chatID, ParentThreadID: &f.mainThreadID,
			Origin: db.ThreadOriginNode, OriginNodeID: &nodeID, CreatedAt: now,
		},
	}
	cwByThread := make(map[string]string, len(threads))
	for _, thread := range threads {
		_, err := repo.CreateThread(ctx, thread)
		require.NoError(t, err)
		cwID := uuid.New().String()
		_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: thread.ID, Sequence: 0, CreatedAt: now})
		require.NoError(t, err)
		cwByThread[thread.ID] = cwID
	}

	var seq int64
	ordinals := make(map[string]int64)
	addMsg := func(threadID string, role reliantv1.MessageRole, block *db.MessageContentBlock) string {
		seq++
		ordinals[threadID]++
		msgID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: chatID, Ordinal: ordinals[threadID], Seq: seq, ThreadID: threadID,
			ContextWindowID: cwByThread[threadID], Role: role,
			CreatedAt: now, UpdatedAt: now,
		}))
		if block == nil {
			block = &db.MessageContentBlock{
				BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
				Content:   ptr.Of("message"),
			}
		}
		block.ID = uuid.New().String()
		block.MessageID = msgID
		block.CreatedAt, block.UpdatedAt = now, now
		require.NoError(t, repo.CreateContentBlock(ctx, block))
		return msgID
	}

	for i := 0; i < mainBefore-1; i++ {
		f.mainMsgIDs = append(f.mainMsgIDs, addMsg(f.mainThreadID, reliantv1.MessageRole_MESSAGE_ROLE_USER, nil))
	}
	spawnMsgID := addMsg(f.mainThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, &db.MessageContentBlock{
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(f.spawnCallID),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"audit the snapshot"}`),
	})
	f.mainMsgIDs = append(f.mainMsgIDs, spawnMsgID)
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: f.spawnCallID, ChatID: chatID, ThreadID: &f.mainThreadID, MessageID: &spawnMsgID,
		ToolName: "spawn", Status: core.ToolCallStatusCompleted,
		ChildWorkflowID: &f.spawnThreadID,
		RequestedAt:     now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))

	for i := 0; i < 2; i++ {
		f.nodeMsgIDs = append(f.nodeMsgIDs, addMsg(nodeThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, nil))
	}
	for i := 0; i < spawnCount; i++ {
		f.spawnMsgIDs = append(f.spawnMsgIDs, addMsg(f.spawnThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, nil))
	}
	f.mainMsgIDs = append(f.mainMsgIDs, addMsg(f.mainThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, nil))

	threadUpdate, err := json.Marshal(map[string]any{
		"update_type": "thread",
		"id":          f.spawnThreadID,
		"chat_id":     chatID,
		"thread":      f.spawnThreadID,
		"workflow_id": f.spawnThreadID,
		"origin":      db.ThreadOriginSpawn,
		"status":      "completed",
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, f.spawnThreadID, string(threadUpdate)))

	return f
}

// TestChatSnapshot_OmitsSpawnThreadTranscripts: the chat-open snapshot must not
// carry spawn-thread messages, and must still carry everything a spawn card
// renders from.
//
// A spawn renders as ONE tool-call card in its parent's transcript. Collapsed
// (the default for agent tools) the card is a header built from the call's
// input and the child workflow's state. Expanded, SpawnPreview reads the child
// thread itself through ListMessages{thread_id}. Nothing reads spawn messages
// out of the snapshot, and the timeline drops them in preview mode — yet they
// were most of it: 636KB of a 2.32MB snapshot on a real 55k-message chat,
// picked by "whatever fell into the newest 200 rows chat-wide", so which cards
// got a partial thread was an accident.
//
// The same chat-wide cut also dropped the transcript's own sibling threads:
// a workflow-node thread renders inline, and a busy spawn pushed its messages
// out of the newest 200 rows entirely.
func TestChatSnapshot_OmitsSpawnThreadTranscripts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedSpawnSiblingsChat(t, repo, ctx, 3, 250)

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, f.chatID)
	require.NoError(t, err)

	got := make(map[string]bool, len(snapshot.Messages))
	var spawnBlock *reliantv1.ContentBlock
	for _, m := range snapshot.Messages {
		require.NotEqual(t, f.spawnThreadID, m.GetThread(),
			"the snapshot shipped a spawn-thread message; the spawn card fetches its own thread when expanded")
		got[m.Id] = true
		for _, b := range m.ContentBlocks {
			if b.GetToolCallId() == f.spawnCallID {
				spawnBlock = b
			}
		}
	}

	for _, id := range f.mainMsgIDs {
		require.True(t, got[id], "main-thread message %s missing from the snapshot", id)
	}
	for _, id := range f.nodeMsgIDs {
		require.True(t, got[id],
			"node-thread message %s missing: node threads render inline in the transcript, and a busy spawn must not push them out of the window", id)
	}

	// What the card needs, still carried.
	require.NotNil(t, spawnBlock, "the spawn tool-call block is the card itself")
	require.Equal(t, f.spawnThreadID, spawnBlock.GetChildWorkflowId(),
		"the block must name the child thread, or an expanded card has nothing to fetch")

	var threadPayload map[string]any
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == f.spawnThreadID {
			threadPayload = candidate
		}
	}
	require.NotNil(t, threadPayload, "the spawn's THREAD update carries its title and status")
	require.Equal(t, db.ThreadOriginSpawn, threadPayload["origin"])
	require.Equal(t, "audit the snapshot", threadPayload["thread_title"])

	// Everything the transcript can show is here. The 250 spawn messages are
	// not older transcript history, so they must not make the client page
	// back for more.
	require.False(t, snapshot.HasMore,
		"has_more must describe the transcript; spawn messages left out of the snapshot are not older history to page back to")
}

// TestChatSnapshot_HasMoreWhenMainThreadOutgrowsWindow is the other half of
// has_more: a main thread longer than the window must still report older
// history.
func TestChatSnapshot_HasMoreWhenMainThreadOutgrowsWindow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	f := seedSpawnSiblingsChat(t, repo, ctx, snapshotMessageLimit+5, 3)

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, f.chatID)
	require.NoError(t, err)

	mainCount := 0
	for _, m := range snapshot.Messages {
		if m.GetThread() == f.mainThreadID {
			mainCount++
		}
	}
	require.Equal(t, snapshotMessageLimit, mainCount, "the window is measured in main-thread messages")
	require.True(t, snapshot.HasMore, "the main thread has messages older than the window")
}
