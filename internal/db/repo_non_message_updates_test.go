// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The chat snapshot used to collect its non-message updates by reading
// GetUpdatesSince(chatID, 0, 10000) — every update TYPE, oldest-first — and
// discarding message updates in Go. Message updates dominate chat_updates, so
// on a long-lived chat the limit was spent on rows the caller was about to
// throw away and the genuinely needed non-message updates past the cap never
// reached the client at all. The oldest rows evicted the newest, which is
// backwards for a snapshot, and nothing ever backfilled them: the snapshot's
// sequence high-water mark is computed separately, so the client's gap
// detection never saw a hole.
//
// These tests pin the replacement's two guarantees: completeness regardless of
// how much message traffic precedes the interesting rows, and exclusion of tool
// calls (served from the durable table instead).

func seedChatForUpdates(t *testing.T, repo *Repo, ctx context.Context) string {
	t.Helper()
	now := time.Now()
	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "Update coverage chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	return chatID
}

func TestGetLatestNonMessageUpdatesPerEntity_NotEvictedByMessageVolume(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// An approval arrives first, then a wall of message updates buries it.
	// Under the old oldest-first read the approval survived only because it was
	// early; the row that used to be LOST is the late one below.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-early", `{"k":"early"}`))

	const messageNoise = 250
	for i := 0; i < messageNoise; i++ {
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeMessage,
			fmt.Sprintf("msg-%d", i), `{"content":"noise"}`))
	}

	// The update that mattered: emitted last, after the message flood. This is
	// exactly the row the capped scan dropped.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-late", `{"k":"late"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	byEntity := make(map[string]ChatUpdate, len(updates))
	for _, u := range updates {
		byEntity[u.EntityID] = u
		require.NotEqual(t, UpdateTypeMessage, u.UpdateType,
			"message updates must be filtered out in SQL, got entity %s", u.EntityID)
	}

	require.Contains(t, byEntity, "approval-early")
	require.Contains(t, byEntity, "approval-late",
		"an update emitted after heavy message traffic must still reach the snapshot")
	require.Len(t, updates, 2, "only the two non-message updates should survive")

	// Ordering is by sequence, not by the dedup key the query groups on.
	require.Less(t, updates[0].SequenceNumber, updates[1].SequenceNumber)
}

// Tool-call updates are deliberately absent from this read: the snapshot
// synthesizes them from the durable tool_calls table (see snapshotToolCalls),
// because replaying the transition history was ~94% of the rows on a long chat.
// The per-tool-call "latest status wins" intent now lives in the snapshot-level
// tests in internal/handlers/chat (tool_call_status_read_test.go).
func TestGetLatestNonMessageUpdatesPerEntity_ExcludesToolCalls(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	for _, toolCallID := range []string{"toolu_a", "toolu_hyphen-b-c"} {
		for _, status := range []ToolCallStatus{ToolCallStatusPending, ToolCallStatusExecuting, ToolCallStatusCompleted} {
			require.NoError(t, repo.EmitToolCallUpdate(ctx, chatID, ToolCallUpdate{
				ToolCallID: toolCallID, ToolName: "view", Status: status,
			}))
			time.Sleep(time.Millisecond)
		}
	}
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, "approval-1", updates[0].EntityID)
}

func TestGetLatestNonMessageUpdatesPerEntity_ThreadAnnouncementSurvivesWorkflowStatus(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// An INLINE fork reuses its parent's workflow id (see
	// inline_workflow_executor.go: "Inline workflows use parent's workflow
	// ID"), and both thread and workflow_status updates key their entity_id on
	// that workflow id. So the fork's one-and-only announcement shares an
	// entity_id with every workflow_status row of the run that created it.
	//
	// Deduping on entity_id alone therefore collapses two DIFFERENT KINDS of
	// update into one bucket, and the last write wins. When the run finishes it
	// writes a workflow_status, which evicts the announcement — the snapshot
	// then has no origin for the thread, InterleavedTimeline cannot classify
	// it, and every message on it is dropped from the timeline.
	//
	// The tell is that this only breaks on a COMPLETED run: while streaming,
	// and on a run whose last write happened to be the announcement, the same
	// chat renders correctly.
	workflowID := uuid.New().String()
	threadID := uuid.New().String()

	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID,
		`{"update_type":"workflow_status","workflow_id":"`+workflowID+`","status":"started"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID,
		`{"update_type":"thread","id":"`+workflowID+`","thread":"`+threadID+`","origin":"fork","thread_title":"implement #1"}`))
	// The run completes. This is the row that used to evict the announcement.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID,
		`{"update_type":"workflow_status","workflow_id":"`+workflowID+`","status":"completed"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	var threadUpdate *ChatUpdate
	var workflowStatus *ChatUpdate
	for i := range updates {
		switch updates[i].UpdateType {
		case UpdateTypeThread:
			threadUpdate = &updates[i]
		case UpdateTypeWorkflowStatus:
			workflowStatus = &updates[i]
		}
	}

	require.NotNil(t, threadUpdate,
		"the thread announcement must survive: it is the only record of the thread's origin, "+
			"and without it the timeline drops every message on that thread")
	require.Contains(t, string(threadUpdate.Data), `"origin":"fork"`)
	require.Contains(t, string(threadUpdate.Data), threadID)

	// Workflow status is workflow state, which the snapshot does not carry
	// (TestGetLatestNonMessageUpdatesPerEntity_CarriesNoWorkflowState).
	require.Nil(t, workflowStatus, "the snapshot must not carry workflow_status")
}

func TestGetLatestNonMessageUpdatesPerEntity_CollapsesThreadToLatestStatus(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// Two threads under ONE inline workflow id — the shape the entity_id key
	// could not represent. Each must survive independently, and each must
	// collapse to its own latest status rather than replaying its history.
	workflowID := uuid.New().String()
	forkThread := uuid.New().String()
	nodeThread := uuid.New().String()

	for _, u := range []struct{ thread, origin, status string }{
		{forkThread, "fork", "running"},
		{nodeThread, "node", "running"},
		{forkThread, "fork", "completed"},
	} {
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID,
			`{"update_type":"thread","id":"`+workflowID+`","thread":"`+u.thread+
				`","origin":"`+u.origin+`","status":"`+u.status+`"}`))
	}

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	statusByThread := map[string]string{}
	originByThread := map[string]string{}
	for _, update := range updates {
		require.Equal(t, UpdateTypeThread, update.UpdateType)
		var payload struct {
			Thread string `json:"thread"`
			Origin string `json:"origin"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(update.Data, &payload))
		statusByThread[payload.Thread] = payload.Status
		originByThread[payload.Thread] = payload.Origin
	}

	require.Equal(t, map[string]string{
		forkThread: "completed",
		nodeThread: "running",
	}, statusByThread, "each thread keeps its own latest status")
	// Origin is what separates a spawned sub-agent from a node/fork thread in
	// the UI, so it must survive per-thread rather than be smeared together.
	require.Equal(t, map[string]string{
		forkThread: "fork",
		nodeThread: "node",
	}, originByThread)
}

func TestGetLatestNonMessageUpdatesPerEntity_ExcludesStreamFinalized(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// STREAM_FINALIZED markers retire in-flight streaming placeholders. A fresh
	// snapshot has no in-flight deltas to retire — the finalized messages are
	// already present as persisted rows — so shipping them is pure weight.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeStreamFinalized, "finalized-1", `{"message_id":"m1"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 1)
	require.Equal(t, "approval-1", updates[0].EntityID)
}

// AGENT_MESSAGES_DRAINED announces mailbox rows that just became transcript
// messages, so the pending-queue strip can retire them in the same commit. Its
// entity_id is unique per drain, so the per-entity dedup keeps EVERY drain the
// chat ever had: 624 rows / 90KB of a 1.7MB snapshot on a real chat. A fresh
// snapshot has no strip to retire rows from — the mailbox hook re-reads
// ListQueuedAgentMessages on mount — so a historical id tombstones nothing.
func TestGetLatestNonMessageUpdatesPerEntity_ExcludesAgentMessagesDrained(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	require.NoError(t, repo.EmitAgentMessagesDrainedUpdate(ctx, chatID, AgentMessagesDrainedUpdate{
		Thread: chatID, MessageIDs: []string{"agent-msg-1"},
	}))
	require.NoError(t, repo.EmitAgentMessagesDrainedUpdate(ctx, chatID, AgentMessagesDrainedUpdate{
		Thread: chatID, MessageIDs: []string{"agent-msg-2"},
	}))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 1)
	require.Equal(t, "approval-1", updates[0].EntityID)
}

// Workflow state — a run's status and its nodes' executions — is keyed per
// workflow and per workflow node, so every run a chat ever made adds entities
// to it; the snapshot read all of them on every open (prod: p50 0.2s, p95
// 2.5s, max 11s) for state nothing a chat open renders reads. It is not
// snapshot state: a run-status view reads its one run's nodes
// (GetLatestWorkflowNodeUpdates). Thread updates that share the workflow id
// as entity_id still come back, each thread's latest.
func TestGetLatestNonMessageUpdatesPerEntity_CarriesNoWorkflowState(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)
	workflowID := uuid.New().String()

	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID, `{"status":"running"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID, `{"thread":"t-1","status":"running"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeNodeExecution, workflowID+":call_llm", `{"node_id":"call_llm"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID, `{"status":"completed"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID, `{"thread":"t-2","status":"running"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	var threads []string
	for _, u := range updates {
		require.NotEqual(t, UpdateTypeWorkflowStatus, u.UpdateType, "workflow_status is not snapshot state")
		require.NotEqual(t, UpdateTypeNodeExecution, u.UpdateType, "node_execution is not snapshot state")
		if u.UpdateType == UpdateTypeThread {
			threads = append(threads, string(u.Data))
		}
	}
	require.Len(t, threads, 2, "each thread keeps its own latest row")
	require.Len(t, updates, 3)
	for i := 1; i < len(updates); i++ {
		require.Less(t, updates[i-1].SequenceNumber, updates[i].SequenceNumber)
	}
}

// A run-status view's node read: the newest update of each node of exactly
// the workflow asked for. Workflow ids that are prefixes of one another, or
// that sort between one workflow's nodes in a linguistic collation, must not
// leak in; nor may the workflow's status row, or another chat's run.
func TestGetLatestWorkflowNodeUpdates_OneRunsNewestPerNode(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)
	otherChat := seedChatForUpdates(t, repo, ctx)

	const wf = "8b2f0c1e-5d6a-4f7b-9c3d-2e1f0a9b8c7d"
	node := func(chat, workflow, nodeID, event string) {
		t.Helper()
		require.NoError(t, repo.CreateChatUpdate(ctx, chat, UpdateTypeNodeExecution, workflow+":"+nodeID,
			`{"workflow_id":"`+workflow+`","node_id":"`+nodeID+`","event_type":"`+event+`"}`))
	}
	node(chatID, wf, "call_llm", "started")
	node(chatID, wf+"0", "call_llm", "started") // a longer id sharing the prefix
	node(chatID, wf[:len(wf)-1], "call_llm", "started")
	node(chatID, wf, "tools", "started")
	node(chatID, wf, "call_llm", "completed")
	node(chatID, wf, "call_llm", "started") // the agent loop's next turn: newest wins
	node(otherChat, wf, "review", "completed")
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, wf, `{"status":"running"}`))

	updates, err := repo.GetLatestWorkflowNodeUpdates(ctx, chatID, wf)
	require.NoError(t, err)
	got := map[string]string{}
	for i, u := range updates {
		require.Equal(t, UpdateTypeNodeExecution, u.UpdateType)
		require.Equal(t, chatID, u.ChatID)
		got[u.EntityID] = string(u.Data)
		if i > 0 {
			require.Less(t, updates[i-1].SequenceNumber, u.SequenceNumber)
		}
	}
	require.Equal(t, map[string]string{
		wf + ":call_llm": `{"workflow_id":"` + wf + `","node_id":"call_llm","event_type":"started"}`,
		wf + ":tools":    `{"workflow_id":"` + wf + `","node_id":"tools","event_type":"started"}`,
	}, got)

	none, err := repo.GetLatestWorkflowNodeUpdates(ctx, chatID, "no-such-run")
	require.NoError(t, err)
	require.Empty(t, none)
}
