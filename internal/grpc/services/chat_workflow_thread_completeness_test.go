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
	"github.com/reliant-labs/reliant/internal/ptr"
)

// GetWorkflowExecutions used to build its tree purely from the `workflows`
// table, which cannot represent every thread that exists.
//
// A `workflows` row carries ONE `thread` value, but an INLINE sub-workflow node
// reuses its parent's workflow id (inline_workflow_executor.go: "Inline
// workflows use parent's workflow ID"). CreateWorkflow is
// `ON CONFLICT (id) DO NOTHING`, so the second thread under that id never gets
// a row of its own and the pre-existing row keeps pointing at the parent's
// thread. The thread itself is created correctly, with a NOT NULL origin --
// only the workflow projection is missing.
//
// The consequence was total: InterleavedTimeline skips any thread it cannot
// classify, so every message on such a thread vanished from the UI. Measured on
// a real database, 185 threads holding real messages across 55 chats, including
// one where the fork thread held 230 of the chat's 233 messages.
//
// The fix reads threads from the table that OWNS thread identity and
// synthesizes the missing nodes. These tests pin that, and pin the property
// that made the old guess-based fallback dangerous: origin is never inferred,
// so a spawn stays a spawn and a fork stays a fork.

// seedChatForThreadTree creates a project + chat and returns the chat id. The
// chat's own id doubles as the main thread id, matching production.
func seedChatForThreadTree(t *testing.T, repo *db.Repo, ctx context.Context, userID string) string {
	t.Helper()
	now := time.Now().UTC()

	projectID := "test-project-thread-tree-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Thread Tree Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "Thread tree chat",
		ProjectID:  projectID,
		UserID:     userID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	return chatID
}

// seedThread inserts a thread row, discarding the echoed row the repo returns.
func seedThread(t *testing.T, repo *db.Repo, ctx context.Context, thread *db.Thread) {
	t.Helper()
	_, err := repo.CreateThread(ctx, thread)
	require.NoError(t, err)
}

// collectThreads flattens the returned execution tree into thread id -> node.
func collectThreads(root *reliantv1.WorkflowExecution) map[string]*reliantv1.WorkflowExecution {
	byThread := map[string]*reliantv1.WorkflowExecution{}
	var walk func(*reliantv1.WorkflowExecution)
	walk = func(wf *reliantv1.WorkflowExecution) {
		if wf == nil {
			return
		}
		if _, seen := byThread[wf.Thread]; !seen {
			byThread[wf.Thread] = wf
		}
		for _, child := range wf.Children {
			walk(child)
		}
	}
	walk(root)
	return byThread
}

func TestGetWorkflowExecutions_IncludesInlineForkThreadWithoutWorkflowRow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	now := time.Now().UTC()

	// The root run. Its workflow id equals the chat id, as production does.
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           chatID,
		ChatID:       chatID,
		WorkflowName: "builtin://forge-one-shot",
		Thread:       chatID,
		Status:       db.Active(),
		CreatedAt:    now,
	}))
	seedThread(t, repo, ctx, &db.Thread{
		ID:        chatID,
		ChatID:    chatID,
		CreatedAt: now,
		Origin:    db.ThreadOriginMain,
		Status:    db.ThreadStatusRunning,
	})

	// The inline fork: a real thread, correct origin, and deliberately NO
	// workflow row -- it reused the root's id, which already existed.
	forkThreadID := uuid.NewString()
	seedThread(t, repo, ctx, &db.Thread{
		ID:             forkThreadID,
		ChatID:         chatID,
		ParentThreadID: ptr.Of(chatID),
		WorkflowID:     ptr.Of(chatID),
		Title:          ptr.Of("implement #1"),
		CreatedAt:      now.Add(time.Second),
		Origin:         db.ThreadOriginFork,
		Status:         db.ThreadStatusRunning,
	})

	service := &ChatService{database: repo}
	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.RootWorkflow)

	byThread := collectThreads(resp.Msg.RootWorkflow)

	forkNode, ok := byThread[forkThreadID]
	require.True(t, ok,
		"a thread with messages must reach the UI even when no workflow row projects it; "+
			"without this the timeline cannot classify it and drops every message on it")
	require.Equal(t, string(db.ThreadOriginFork), forkNode.Origin,
		"origin must come from the threads table, never be inferred")
	require.Equal(t, "implement #1", forkNode.GetThreadTitle())
	require.Equal(t, chatID, forkNode.GetParentThread())
}

func TestGetWorkflowExecutions_PreservesSpawnAndNodeOriginDistinction(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	now := time.Now().UTC()

	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           chatID,
		ChatID:       chatID,
		WorkflowName: "builtin://forge-one-shot",
		Thread:       chatID,
		Status:       db.Active(),
		CreatedAt:    now,
	}))
	seedThread(t, repo, ctx, &db.Thread{
		ID:        chatID,
		ChatID:    chatID,
		CreatedAt: now,
		Origin:    db.ThreadOriginMain,
		Status:    db.ThreadStatusRunning,
	})

	// An inline fork with no workflow row of its own (synthesized by the fix).
	forkThreadID := uuid.NewString()
	seedThread(t, repo, ctx, &db.Thread{
		ID:             forkThreadID,
		ChatID:         chatID,
		ParentThreadID: ptr.Of(chatID),
		WorkflowID:     ptr.Of(chatID),
		Title:          ptr.Of("implement #1"),
		CreatedAt:      now.Add(time.Second),
		Origin:         db.ThreadOriginFork,
		Status:         db.ThreadStatusRunning,
	})

	// A spawned sub-agent DOES get its own workflow row, so it arrives through
	// the normal tree path. It must keep origin "spawn": that is what makes the
	// UI collapse it into its tool-call card instead of rendering it inline.
	spawnThreadID := uuid.NewString()
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           spawnThreadID,
		ParentID:     ptr.Of(chatID),
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       spawnThreadID,
		Status:       db.Active(),
		CreatedAt:    now.Add(2 * time.Second),
	}))
	seedThread(t, repo, ctx, &db.Thread{
		ID:             spawnThreadID,
		ChatID:         chatID,
		ParentThreadID: ptr.Of(forkThreadID),
		WorkflowID:     ptr.Of(spawnThreadID),
		Title:          ptr.Of("Backend: billing"),
		CreatedAt:      now.Add(2 * time.Second),
		Origin:         db.ThreadOriginSpawn,
		Status:         db.ThreadStatusRunning,
	})

	// A graph-node thread, also without its own workflow row.
	nodeThreadID := uuid.NewString()
	seedThread(t, repo, ctx, &db.Thread{
		ID:             nodeThreadID,
		ChatID:         chatID,
		ParentThreadID: ptr.Of(chatID),
		WorkflowID:     ptr.Of(chatID),
		CreatedAt:      now.Add(3 * time.Second),
		Origin:         db.ThreadOriginNode,
		OriginNodeID:   ptr.Of("review"),
		Status:         db.ThreadStatusRunning,
	})

	service := &ChatService{database: repo}
	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.RootWorkflow)

	byThread := collectThreads(resp.Msg.RootWorkflow)

	originByThread := map[string]string{}
	for id, node := range byThread {
		originByThread[id] = node.Origin
	}

	// Each origin survives intact. Collapsing these into one bucket -- which an
	// inferred fallback would do -- is what previously dumped whole sub-agent
	// transcripts inline into the parent chat.
	require.Equal(t, map[string]string{
		chatID:        string(db.ThreadOriginMain),
		forkThreadID:  string(db.ThreadOriginFork),
		spawnThreadID: string(db.ThreadOriginSpawn),
		nodeThreadID:  string(db.ThreadOriginNode),
	}, originByThread)

	// The spawn still arrives with its real workflow row, not a synthesized
	// stand-in, so its steps and lifecycle keep working.
	require.Equal(t, spawnThreadID, byThread[spawnThreadID].Id)
	require.Equal(t, "builtin://agent", byThread[spawnThreadID].WorkflowName)

	// A node thread names the node that created it; that is genuine provenance.
	require.Equal(t, "review", byThread[nodeThreadID].GetOriginNodeId())
}
