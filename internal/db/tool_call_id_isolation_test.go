// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A tool call id is chosen by the model provider, so nothing guarantees it is
// unique across chats: a local OpenAI-compatible server can answer `call_0`
// in every conversation it serves. These tests pin that a repeated id never
// carries one chat's record into another, whichever chat used it first.

const reusedToolCallID = "call_0"

// childThread creates a spawn's child thread: mailbox rows reference threads.
func childThread(t *testing.T, repo *Repo, id, chatID string) {
	t.Helper()
	_, err := repo.CreateThread(context.Background(), &Thread{ID: id, ChatID: chatID, ParentThreadID: &chatID, CreatedAt: time.Now()})
	require.NoError(t, err)
}

func spawnReport(chatID, from, to, body string) *AgentMessage {
	toolCallID := reusedToolCallID
	return &AgentMessage{
		ID: "am-" + chatID, ChatID: chatID, FromThreadID: from, ToThreadID: to,
		Kind: core.AgentMessageKindCompletion, Body: body, ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}
}

// Chat B reuses an id chat A's call already holds. B's write must be refused,
// not merged into A's row: the upsert's DO UPDATE never touched chat_id, so it
// used to leave a row that claimed to be A's while carrying B's thread, input
// and status — which A's transcript then rendered as its own.
func TestToolCallIDIsolation_UpsertDoesNotOverwriteAnotherChatsCall(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")

	now := time.Now().UTC()
	threadA, threadB := "chat-iso-a", "chat-iso-b"
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: reusedToolCallID, ChatID: "chat-iso-a", ThreadID: &threadA, ToolName: "bash",
		Input: []byte(`{"command":"cat /a/secret"}`), Status: core.ToolCallStatusCompleted,
		RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))

	err := repo.UpsertToolCall(ctx, &ToolCall{
		ID: reusedToolCallID, ChatID: "chat-iso-b", ThreadID: &threadB, ToolName: "bash",
		Input: []byte(`{"command":"ls /b"}`), Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	require.ErrorIs(t, err, core.ErrToolCallIDInAnotherChat, "a write for chat B must not land on the row chat A's call holds")

	got, err := repo.GetToolCall(ctx, reusedToolCallID)
	require.NoError(t, err)
	require.Equal(t, "chat-iso-a", got.ChatID)
	require.Equal(t, threadA, *got.ThreadID, "chat A's call must keep its own thread")
	require.JSONEq(t, `{"command":"cat /a/secret"}`, string(got.Input), "chat A's call must keep its own input")
	require.Equal(t, core.ToolCallStatusCompleted, got.Status)
}

// The same refusal through the status writer every tool path uses, which reads
// the existing row and inherits its fields. Inheriting from ANOTHER chat's row
// is how chat B's call used to acquire chat A's thread and message.
func TestToolCallIDIsolation_StatusWriteDoesNotMergeIntoAnotherChatsCall(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")

	now := time.Now().UTC()
	threadA := "chat-iso-a"
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: reusedToolCallID, ChatID: "chat-iso-a", ThreadID: &threadA, ToolName: "bash",
		Input: []byte(`{"command":"sleep 60"}`), Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	err := UpsertToolCallStatus(ctx, repo, &ToolCall{
		ID: reusedToolCallID, ChatID: "chat-iso-b", ToolName: "bash",
		Status: core.ToolCallStatusCompleted, RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	})
	require.ErrorIs(t, err, core.ErrToolCallIDInAnotherChat, "chat B's status must not be written onto chat A's call")

	got, err := repo.GetToolCall(ctx, reusedToolCallID)
	require.NoError(t, err)
	require.Equal(t, "chat-iso-a", got.ChatID)
	require.Equal(t, core.ToolCallStatusExecuting, got.Status, "chat A's call is still running; chat B finishing must not end it")
}

// A result belongs to its call's chat. Chat B's result under an id chat A's
// call holds satisfied the foreign key against A's call and replaced A's
// result; the writer must now prove it is the call's chat.
func TestToolCallIDIsolation_ResultWriteDoesNotReplaceAnotherChatsResult(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")

	now := time.Now().UTC()
	threadA := "chat-iso-a"
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: reusedToolCallID, ChatID: "chat-iso-a", ThreadID: &threadA, ToolName: "bash",
		Status: core.ToolCallStatusCompleted, RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCallResult(ctx, "chat-iso-a", &ToolCallResult{
		ToolCallID: reusedToolCallID, Content: "chat A's output", CreatedAt: now, UpdatedAt: now,
	}))

	err := repo.UpsertToolCallResult(ctx, "chat-iso-b", &ToolCallResult{
		ToolCallID: reusedToolCallID, Content: "chat B's output", CreatedAt: now, UpdatedAt: now,
	})
	require.ErrorIs(t, err, core.ErrToolCallIDInAnotherChat)

	got, err := repo.GetToolCallResult(ctx, reusedToolCallID)
	require.NoError(t, err)
	require.Equal(t, "chat A's output", got.Content)
}

// A spawn's report is keyed by its tool call id. Chat B's report for its own
// `call_0` spawn must not count as chat A's: the stranded-spawn sweep would
// then believe A's spawn had reported and never write A a placeholder, leaving
// A's parent waiting on a result that is never coming.
func TestToolCallIDIsolation_StrandedSpawnSweepSeesOnlyItsOwnChatsReport(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")

	childA := "wf-iso-child-a"
	insertTestWorkflowWithParent(t, repo, childA, "chat-iso-a", nil, Completed())
	insertTestSpawnToolCall(t, repo, reusedToolCallID, "chat-iso-a", "chat-iso-a", &childA, core.ToolCallStatusBackgrounded)

	childB := "wf-iso-child-b"
	insertTestWorkflowWithParent(t, repo, childB, "chat-iso-b", nil, Completed())
	childThread(t, repo, childB, "chat-iso-b")
	require.NoError(t, repo.EnqueueAgentMessage(ctx, spawnReport("chat-iso-b", childB, "chat-iso-b", "chat B's result")))

	stranded, err := repo.ListStrandedBackgroundSpawnToolCalls(ctx)
	require.NoError(t, err)
	var found *StrandedBackgroundSpawn
	for _, s := range stranded {
		if s.ToolCallID == reusedToolCallID && s.ChatID == "chat-iso-a" {
			found = s
		}
	}
	require.NotNil(t, found, "chat A's finished spawn must be swept")
	require.False(t, found.HasReport, "chat B's report is not chat A's: A's spawn has not reported")
}

// The relaunch after a root execution dies relaunches every backgrounded spawn
// that has not reported. Another chat's report under the same id must not
// make this chat's live spawn look finished.
func TestToolCallIDIsolation_LiveSpawnIsNotHiddenByAnotherChatsReport(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")

	rootA := "chat-iso-a"
	insertTestWorkflowWithParent(t, repo, rootA, "chat-iso-a", nil, Failed())
	childA := "wf-iso-live-child-a"
	insertTestWorkflowWithParent(t, repo, childA, "chat-iso-a", &rootA, Failed())
	insertTestSpawnToolCall(t, repo, reusedToolCallID, "chat-iso-a", rootA, &childA, core.ToolCallStatusBackgrounded)

	childB := "wf-iso-live-child-b"
	insertTestWorkflowWithParent(t, repo, childB, "chat-iso-b", nil, Completed())
	childThread(t, repo, childB, "chat-iso-b")
	require.NoError(t, repo.EnqueueAgentMessage(ctx, spawnReport("chat-iso-b", childB, "chat-iso-b", "chat B's result")))

	live, err := repo.ListLiveBackgroundSpawns(ctx, rootA)
	require.NoError(t, err)
	require.Len(t, live, 1, "chat A's spawn has not reported and must be relaunched")
	require.Equal(t, reusedToolCallID, live[0].ToolCallID)
}

// While the chat-blind slot still exists (the expand step keeps it for the
// previous release), chat A's placeholder under an id chat B already reported
// under cannot be stored. It must fail loudly, not as DO NOTHING's silent
// "already reported" -- the contract step, which drops the chat-blind index,
// turns this into the placeholder landing.
func TestToolCallIDIsolation_PlaceholderUnderAnotherChatsReportedIDFailsLoudly(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	createActivityTestChat(t, repo, "chat-iso-b")
	childThread(t, repo, "child-a", "chat-iso-a")
	childThread(t, repo, "child-b", "chat-iso-b")

	require.NoError(t, repo.EnqueueAgentMessage(ctx, spawnReport("chat-iso-b", "child-b", "chat-iso-b", "chat B's result")))

	placeholder := spawnReport("chat-iso-a", "child-a", "chat-iso-a", "Sub-agent finished while its result was lost in transit; check spawn_status.")
	placeholder.Synthesized = true
	inserted, err := repo.EnqueueAgentMessageIfAbsent(ctx, placeholder)
	require.ErrorIs(t, err, core.ErrSpawnReportSlotTaken, "chat A's placeholder must not vanish as \"already reported\"")
	require.False(t, inserted)

	queuedB, err := repo.ListQueuedAgentMessagesForThread(ctx, "chat-iso-b")
	require.NoError(t, err)
	require.Len(t, queuedB, 1)
	require.Equal(t, "chat B's result", queuedB[0].Body, "chat B's report must be untouched")
}

// The expand step's promise: the PREVIOUS release's report writers -- which
// arbitrate on the chat-blind index by naming ON CONFLICT (tool_call_id) --
// still work against this schema. These are that release's statements,
// verbatim. The contract step drops the index they need, and this test with it.
func TestSpawnReportSlot_PreviousReleaseStatementsStillWork(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-prev")
	childThread(t, repo, "child-prev", "chat-prev")

	const previousIfAbsent = `INSERT INTO agent_messages (
    id, chat_id, from_thread_id, to_thread_id, kind, body, tool_call_id,
    status, created_at, attachments, synthesized
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (tool_call_id) WHERE kind IN (2, 3, 4) DO NOTHING
RETURNING id`
	const previousSpawnReport = `INSERT INTO agent_messages (
    id, chat_id, from_thread_id, to_thread_id, kind, body, tool_call_id,
    status, created_at, attachments, synthesized
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, false
)
ON CONFLICT (tool_call_id) WHERE kind IN (2, 3, 4) DO UPDATE SET
    id = EXCLUDED.id,
    chat_id = EXCLUDED.chat_id,
    from_thread_id = EXCLUDED.from_thread_id,
    to_thread_id = EXCLUDED.to_thread_id,
    kind = EXCLUDED.kind,
    body = EXCLUDED.body,
    attachments = EXCLUDED.attachments,
    status = EXCLUDED.status,
    created_at = EXCLUDED.created_at,
    delivered_at = NULL,
    delivered_message_id = NULL,
    synthesized = false
WHERE agent_messages.synthesized
RETURNING id, (xmax = 0) AS inserted`

	now := time.Now().UTC()
	var id string
	require.NoError(t, rawDB.QueryRowContext(ctx, previousIfAbsent,
		"am-prev-placeholder", "chat-prev", "child-prev", "chat-prev", int32(core.AgentMessageKindCompletion),
		"lost in transit", reusedToolCallID, int32(core.AgentMessageStatusQueued), now, nil, true,
	).Scan(&id), "the previous release's placeholder write must still find its arbiter index")
	require.Equal(t, "am-prev-placeholder", id)

	var inserted bool
	require.NoError(t, rawDB.QueryRowContext(ctx, previousSpawnReport,
		"am-prev-real", "chat-prev", "child-prev", "chat-prev", int32(core.AgentMessageKindCompletion),
		"the real result", reusedToolCallID, int32(core.AgentMessageStatusQueued), now, nil,
	).Scan(&id, &inserted), "the previous release's real report must still supersede its placeholder")
	require.Equal(t, "am-prev-real", id)
	require.False(t, inserted, "superseded, not inserted")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, "chat-prev")
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "the real result", queued[0].Body)
}

// Within one chat the slot is still one per id. The sweep's placeholder for a
// spawn whose id another spawn of the same chat already reported under has
// nowhere to go, and says so instead of reporting "already reported".
func TestToolCallIDIsolation_PlaceholderForADifferentSpawnInTheSameChatFailsLoudly(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	createActivityTestChat(t, repo, "chat-iso-a")
	childThread(t, repo, "child-a-1", "chat-iso-a")
	childThread(t, repo, "child-a-2", "chat-iso-a")

	require.NoError(t, repo.EnqueueAgentMessage(ctx, spawnReport("chat-iso-a", "child-a-1", "chat-iso-a", "first spawn's result")))

	placeholder := spawnReport("chat-iso-a", "child-a-2", "chat-iso-a", "Sub-agent finished while its result was lost in transit; check spawn_status.")
	placeholder.ID = "am-placeholder-2"
	placeholder.Synthesized = true
	inserted, err := repo.EnqueueAgentMessageIfAbsent(ctx, placeholder)
	require.ErrorIs(t, err, core.ErrSpawnReportSlotTaken)
	require.False(t, inserted)

	// The same spawn's own report racing the sweep stays the ordinary no-op.
	sameSpawn := spawnReport("chat-iso-a", "child-a-1", "chat-iso-a", "Sub-agent finished while its result was lost in transit; check spawn_status.")
	sameSpawn.ID = "am-placeholder-1"
	sameSpawn.Synthesized = true
	inserted, err = repo.EnqueueAgentMessageIfAbsent(ctx, sameSpawn)
	require.NoError(t, err)
	require.False(t, inserted)
}
