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
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// branchNoteForkTime is the fork point's timestamp; the note names it, so it is
// pinned rather than taken from the clock.
var branchNoteForkTime = time.Date(2026, 10, 6, 0, 39, 0, 0, time.UTC)

// setupBranchNoteSource builds a chat whose transcript ends at forkPoint, with
// `spawns` background sub-agents issued from an earlier assistant message —
// the shape of the 2026-10-06 roofers orchestrator that was branched mid-wait.
func setupBranchNoteSource(t *testing.T, repo *db.Repo, ctx context.Context, projectID string, spawns int) (chatID, forkPoint string) {
	t.Helper()
	chatID = uuid.NewString()
	threadID := chatID
	cwID := chatID + ":" + threadID + ":0"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Roofing Management App", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: branchNoteForkTime, UpdatedAt: branchNoteForkTime,
	}))
	_, _, _, err := threads.NewService(repo).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: threadID, Status: db.Active(), CreatedAt: branchNoteForkTime,
		},
		ThreadID: threadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	message := func(ordinal int64, role reliantv1.MessageRole) string {
		id := uuid.NewString()
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: id, ChatID: chatID, ThreadID: threadID, ContextWindowID: cwID,
			Role: role, Ordinal: ordinal, Seq: ordinal, CreatedAt: branchNoteForkTime,
		}))
		return id
	}
	message(1, reliantv1.MessageRole_MESSAGE_ROLE_USER)
	spawnMsg := message(2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	for i := 0; i < spawns; i++ {
		child := uuid.NewString()
		_, err := repo.CreateThread(ctx, &db.Thread{
			ID: child, ChatID: chatID, ParentThreadID: &threadID,
			Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning, CreatedAt: branchNoteForkTime,
		})
		require.NoError(t, err)
		require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
			ID: child, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: child, Status: db.Active(), CreatedAt: branchNoteForkTime,
		}))
		require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
			ID: "toolu_" + uuid.NewString(), ChatID: chatID, ThreadID: &threadID, MessageID: &spawnMsg,
			ToolName: "spawn", Status: core.ToolCallStatusBackgrounded, ChildWorkflowID: &child,
			RequestedAt: branchNoteForkTime, CreatedAt: branchNoteForkTime, UpdatedAt: branchNoteForkTime,
		}))
	}
	forkPoint = message(3, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	return chatID, forkPoint
}

// branchNote branches sourceChatID at forkPoint and returns the hidden note
// the branch starts with — the only message the branch's own thread holds.
func branchNote(t *testing.T, repo *db.Repo, ctx context.Context, sourceChatID, forkPoint string) string {
	t.Helper()
	service := &ChatService{database: repo, threads: threads.NewService(repo)}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId: sourceChatID, MessageId: forkPoint,
	}))
	require.NoError(t, err)
	branchThread := resp.Msg.Chat.Id

	count, err := repo.CountMessagesInThread(ctx, branchThread)
	require.NoError(t, err)
	require.Equal(t, 1, count, "a fresh branch holds exactly one message of its own: the branch note")

	note, err := repo.GetLatestMessageInThread(ctx, branchThread)
	require.NoError(t, err)
	require.NotNil(t, note, "the branch must start with a note explaining what a branch is")
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, note.Role)
	require.NotNil(t, note.DisplayStyle)
	require.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *note.DisplayStyle,
		"the note is for the model; the user's transcript must not show it")

	blocks, err := repo.ListContentBlocks(ctx, note.ID)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Content)
	return *blocks[0].Content
}

// A branch's model inherits a transcript that reads as its own past, so it
// must be told it is a branch: of which chat, from which point, and that the
// original may still be running alongside it.
func TestBranchChat_HiddenNote_StatesBranchFacts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	sourceChatID, forkPoint := setupBranchNoteSource(t, repo, ctx, newBranchTestProject(t, repo, ctx), 0)
	note := branchNote(t, repo, ctx, sourceChatID, forkPoint)

	require.Equal(t, fmt.Sprintf(
		`This chat is a branch of "Roofing Management App" (chat %s), forked at the message above (2026-10-06 00:39 UTC). `+
			`The original chat may still be running.`,
		sourceChatID), note,
		"with no inherited sub-agents the note is the two branch facts and nothing else")
}

// The wording edges a database fixture would be slow to reach: one agent,
// a resumed agent (one thread, several spawn rows), a branch of a branch whose
// agents belong to more than one chat, and an untitled source.
func TestBranchContextNote_Edges(t *testing.T) {
	forkPoint := &db.Message{CreatedAt: branchNoteForkTime}
	source := &db.Chat{ID: "src", Title: "Roofing"}
	thread := func(id string) *string { return &id }
	child := func(toolCallID, threadID, sourceChatID string) *db.InheritedSpawnChild {
		c := &db.InheritedSpawnChild{SourceChatID: sourceChatID}
		c.ToolCallID = toolCallID
		if threadID != "" {
			c.ChildThreadID = thread(threadID)
		}
		return c
	}
	const head = `This chat is a branch of "Roofing" (chat src), forked at the message above (2026-10-06 00:39 UTC). The original chat may still be running.`

	require.Equal(t, head+"\n"+
		`The sub-agent spawned before the branch belongs to the original chat. `+
		`Here it is visible read-only with spawn_status; its results and notifications are not delivered to this chat. `+
		`Re-spawn anything you need from this chat.`,
		branchContextNote(source, forkPoint, []*db.InheritedSpawnChild{
			child("tc-1", "agent-a", "src"),
			child("tc-2", "agent-a", "src"), // a resumption of the same agent
		}), "a resumed agent is still one agent")

	require.Equal(t, head+"\n"+
		`The 2 sub-agents spawned before the branch belong to the chats that spawned them. `+
		`Here they are visible read-only with spawn_status; their results and notifications are not delivered to this chat. `+
		`Re-spawn anything you need from this chat.`,
		branchContextNote(source, forkPoint, []*db.InheritedSpawnChild{
			child("tc-1", "agent-a", "src"),
			child("tc-2", "", "grandparent"), // child rows never landed: counted by its call
		}), "agents inherited from an older chat do not belong to the original")

	require.Equal(t,
		`This chat is a branch of chat src, forked at the message above (2026-10-06 00:39 UTC). The original chat may still be running.`,
		branchContextNote(&db.Chat{ID: "src"}, forkPoint, nil))
}

// The case that lost an orchestrator its sub-agents: the inherited transcript
// holds "you will be notified" spawn handles that will never notify the branch.
// The note must say so — and say what to do instead.
func TestBranchChat_HiddenNote_ExplainsInheritedSubAgents(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	sourceChatID, forkPoint := setupBranchNoteSource(t, repo, ctx, newBranchTestProject(t, repo, ctx), 3)
	note := branchNote(t, repo, ctx, sourceChatID, forkPoint)

	require.Equal(t, fmt.Sprintf(
		`This chat is a branch of "Roofing Management App" (chat %s), forked at the message above (2026-10-06 00:39 UTC). `+
			`The original chat may still be running.`+"\n"+
			`The 3 sub-agents spawned before the branch belong to the original chat. `+
			`Here they are visible read-only with spawn_status; their results and notifications are not delivered to this chat. `+
			`Re-spawn anything you need from this chat.`,
		sourceChatID), note)
}
