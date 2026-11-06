// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

func TestExpandAgentIDPrefix(t *testing.T) {
	t.Parallel()
	ids := []string{
		"088f1c16-1111-4111-8111-111111111111",
		"088f2000-2222-4222-8222-222222222222",
		"cf3ffd18-3333-4333-8333-333333333333",
	}
	cases := []struct {
		name, in, want, errHas string
	}{
		{name: "exact id", in: ids[0], want: ids[0]},
		{name: "unique 8-char prefix", in: "088f1c16", want: ids[0]},
		{name: "unique short prefix", in: "cf3f", want: ids[2]},
		{name: "no match is passed through", in: "deadbeef", want: "deadbeef"},
		{name: "ambiguous prefix", in: "088f", errHas: "ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandAgentIDPrefix(tc.in, ids)
			if tc.errHas != "" {
				require.ErrorContains(t, err, tc.errHas)
				require.ErrorContains(t, err, ids[0])
				require.ErrorContains(t, err, ids[1])
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestLatestSpawnPerAgent_KeepsOneEntryPerAgent(t *testing.T) {
	t.Parallel()
	a, b := "agent-a", "agent-b"
	children := []*db.SpawnChild{
		{ToolCallID: "1", ChildThreadID: &a},
		{ToolCallID: "2", ChildThreadID: &b},
		{ToolCallID: "3"}, // child not landed yet
		{ToolCallID: "4", ChildThreadID: &a},
	}
	got := latestSpawnPerAgent(children)
	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.ToolCallID)
	}
	require.Equal(t, []string{"2", "3", "4"}, ids, "a resumed agent is listed once, by its latest spawn call")
}

// A parent that only kept the 8-character prefix of its child's id (the
// usual survivor of a context summary) can still inspect, message and stop it.
func TestSpawnTools_AcceptAnUnambiguousAgentIDPrefix(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp, meta := runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: childID[:8]})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Equal(t, childID, meta.AgentID)

	sendResolved, err := resolveSpawnAgentID(rc, repo, parentID, childID[:8])
	require.NoError(t, err)
	require.Equal(t, childID, sendResolved)

	// A prefix of an agent the caller does not own resolves to nothing, so
	// the tool's own refusal still applies.
	_, otherChild, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	resp, _ = runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: otherChild[:8]})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not a sub-agent spawned from this thread")
}

// The listing puts running agents first, lists a resumed agent once, and can
// be narrowed to running agents, so a busy orchestrator's live children are
// never cut off behind hundreds of finished ones.
func TestSpawnStatus_Listing_RunningFirstDedupedAndFilterable(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, runningChild, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	finishedChild := addSpawnChild(t, repo, ctx, chatID, parentID, db.ThreadStatusCompleted, time.Now().Add(time.Minute))
	// Resume the finished child: a second spawn call naming the same thread.
	resumedAt := time.Now().Add(2 * time.Minute)
	wf := finishedChild
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: "toolu_" + uuid.New().String(), ChatID: chatID, ThreadID: &parentID,
		ToolName: "spawn", Status: core.ToolCallStatusCompleted, ChildWorkflowID: &wf,
		RequestedAt: resumedAt, CompletedAt: &resumedAt, CreatedAt: resumedAt, UpdatedAt: resumedAt,
	}))

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp, meta := runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Equal(t, 2, meta.TotalAgents)
	require.Equal(t, 1, meta.RunningAgents)
	require.Equal(t, 1, strings.Count(resp.Content, "agent_id: "+finishedChild), "a resumed agent is listed once")
	require.Less(t, strings.Index(resp.Content, runningChild), strings.Index(resp.Content, finishedChild),
		"running agents are listed before finished ones, even when spawned earlier")

	resp, _ = runSpawnStatus(t, tool, rc, SpawnStatusParams{Status: "running"})
	require.Contains(t, resp.Content, runningChild)
	require.NotContains(t, resp.Content, "agent_id: "+finishedChild)

	resp, _ = runSpawnStatus(t, tool, rc, SpawnStatusParams{Status: "finished"})
	require.NotContains(t, resp.Content, "agent_id: "+runningChild)
	require.Contains(t, resp.Content, finishedChild)
}

// addSpawnChild seeds one more spawn child under an existing parent.
func addSpawnChild(t *testing.T, repo *db.Repo, ctx context.Context, chatID, parentID string, status db.ThreadStatus, at time.Time) string {
	t.Helper()
	childID := uuid.New().String()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: childID, ChatID: chatID, ParentThreadID: &parentID,
		Origin: db.ThreadOriginSpawn, Status: status, CreatedAt: at,
	})
	require.NoError(t, err)
	wfStatus := db.Active()
	if status == db.ThreadStatusCompleted {
		wfStatus = db.Completed()
	}
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childID, Status: wfStatus, CreatedAt: at,
	}))
	wf := childID
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: "toolu_" + uuid.New().String(), ChatID: chatID, ThreadID: &parentID,
		ToolName: "spawn", Status: core.ToolCallStatusCompleted, ChildWorkflowID: &wf,
		RequestedAt: at, CompletedAt: &at, CreatedAt: at, UpdatedAt: at,
	}))
	return childID
}
