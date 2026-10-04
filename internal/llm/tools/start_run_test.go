// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// startRunCall issues start_run from callerID with the given tool call id.
func startRunCall(t *testing.T, repo db.Repository, starter RunStarter, callerID, toolCallID string, params StartRunParams) ToolResponse {
	t.Helper()
	return callRunTool(t, NewStartRunTool(repo, starter), callerRC(callerID, toolCallID), StartRunToolName, params)
}

func startedChatID(t *testing.T, resp ToolResponse) string {
	t.Helper()
	var meta StartRunResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta), "response: %+v", resp)
	require.NotEmpty(t, meta.ChatID)
	return meta.ChatID
}

// recordAgentStart writes the launch event an agent-started run leaves behind,
// for a run (child) started from parent. This is exactly what the launcher
// records, so it builds lineage chains for the depth guard to walk.
func recordAgentStart(t *testing.T, repo db.Repository, parent, child string) {
	t.Helper()
	created, err := repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID: uuid.NewString(), UserID: runTestUser, Kind: core.TriggerEventKindAgentStartRun,
		DedupeKey: parent + ":" + uuid.NewString(), OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"parent_chat_id": parent},
		Outcome: core.TriggerEventLaunched, ChatID: &child,
	})
	require.NoError(t, err)
	require.True(t, created)
}

func TestStartRun_LaunchesAsTheCallersOwnerWithLineage(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	starter := &fakeRunStarter{}
	resp := startRunCall(t, repo, starter, callerID, "toolu_1", StartRunParams{
		Workflow: "builtin://agent", Message: "audit the repo", Title: "audit",
		Inputs: map[string]any{"depth": 2}, Presets: map[string]string{"model": "fast"},
	})
	require.False(t, resp.IsError, resp.Content)

	calls := starter.calls()
	require.Len(t, calls, 1)
	req := calls[0]
	assert.Equal(t, runTestUser, req.OwnerUserID, "the owner comes from the calling chat, never a parameter")
	assert.Equal(t, "test-project", req.ProjectID, "project defaults to the caller's own")
	assert.Equal(t, "builtin://agent", req.Workflow)
	assert.Equal(t, "audit the repo", req.Message)
	assert.Equal(t, "audit", req.Title)
	assert.Equal(t, callerID, req.ParentChatID, "lineage: the starting chat is recorded as the parent")
	assert.Equal(t, callerID+":toolu_1", req.DedupeKey, "the dedupe key is the calling chat plus the tool call id")
	assert.EqualValues(t, 2, req.Inputs["depth"])
	assert.Equal(t, map[string]string{"model": "fast"}, req.Presets)
	assert.Equal(t, startedChatID(t, resp), startedChatID(t, resp))
}

// The new run executes on the daemon the calling chat is bound to.
func TestStartRun_InheritsTheCallersDaemon(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)
	chat, err := repo.GetChat(context.Background(), callerID)
	require.NoError(t, err)
	daemon := "daemon-42"
	chat.ActiveDaemonID = &daemon
	require.NoError(t, repo.UpdateChat(context.Background(), chat))
	require.NoError(t, repo.UpdateChatActiveDaemon(context.Background(), callerID, &daemon))

	starter := &fakeRunStarter{}
	resp := startRunCall(t, repo, starter, callerID, "toolu_d", StartRunParams{Workflow: "builtin://agent", Message: "go"})
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, starter.calls(), 1)
	assert.Equal(t, "daemon-42", starter.calls()[0].DaemonID)
}

func TestStartRun_ExplicitProjectOverridesTheDefault(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	starter := &fakeRunStarter{}
	resp := startRunCall(t, repo, starter, callerID, "toolu_p", StartRunParams{
		Workflow: "builtin://agent", Message: "go", ProjectID: "other-project",
	})
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, starter.calls(), 1)
	assert.Equal(t, "other-project", starter.calls()[0].ProjectID)
}

// A retried ExecuteTools activity re-issues the same tool call. The second
// attempt must attach to the run the first one started, not start another.
func TestStartRun_SameToolCallIsIdempotent(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	starter := &fakeRunStarter{repo: repo}
	params := StartRunParams{Workflow: "builtin://agent", Message: "do it once"}

	first := startRunCall(t, repo, starter, callerID, "toolu_retry", params)
	require.False(t, first.IsError, first.Content)
	firstChat := startedChatID(t, first)

	second := startRunCall(t, repo, starter, callerID, "toolu_retry", params)
	require.False(t, second.IsError, second.Content)
	assert.Equal(t, firstChat, startedChatID(t, second), "the retry attaches to the run already started")
	assert.Contains(t, second.Content, "already done")
	assert.Len(t, starter.calls(), 1, "the launcher must be called exactly once for one tool call")

	// A DIFFERENT tool call is new work.
	third := startRunCall(t, repo, starter, callerID, "toolu_other", params)
	require.False(t, third.IsError, third.Content)
	assert.NotEqual(t, firstChat, startedChatID(t, third))
	assert.Len(t, starter.calls(), 2)
}

// The same tool call id in a different chat is a different call: ids are minted
// by the model and are only unique within one conversation.
func TestStartRun_ToolCallIDsAreScopedToTheCallingChat(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerA := newCaller(t, repo)
	callerB := newCaller(t, repo)

	starter := &fakeRunStarter{repo: repo}
	params := StartRunParams{Workflow: "builtin://agent", Message: "go"}
	a := startRunCall(t, repo, starter, callerA, "toolu_same", params)
	b := startRunCall(t, repo, starter, callerB, "toolu_same", params)
	require.False(t, a.IsError, a.Content)
	require.False(t, b.IsError, b.Content)
	assert.NotEqual(t, startedChatID(t, a), startedChatID(t, b))
	assert.Len(t, starter.calls(), 2)
}

// Depth: a run a human started is depth 0, so it may start a run (depth 1),
// which may start another (2), which may start another (3). A run at depth 3
// may not start a fourth.
func TestStartRun_DepthCapStopsTheChain(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	// human -> d1 -> d2 -> d3
	human := newCaller(t, repo)
	chain := []string{human}
	for i := 0; i < MaxAgentStartRunDepth; i++ {
		child := uuid.NewString()
		seedRun(t, repo, runSeed{id: child, user: runTestUser, status: db.Completed()})
		recordAgentStart(t, repo, chain[len(chain)-1], child)
		chain = append(chain, child)
	}

	for depth, chatID := range chain {
		starter := &fakeRunStarter{}
		resp := startRunCall(t, repo, starter, chatID, "toolu_depth", StartRunParams{Workflow: "builtin://agent", Message: "go"})
		if depth < MaxAgentStartRunDepth {
			assert.False(t, resp.IsError, "a run at depth %d may start a run: %s", depth, resp.Content)
			assert.Len(t, starter.calls(), 1, "depth %d", depth)
			continue
		}
		require.True(t, resp.IsError, "a run at depth %d must not start another", depth)
		assert.Contains(t, resp.Content, "3 level")
		assert.Empty(t, starter.calls(), "the launcher must not be reached past the depth cap")
	}
}

// A corrupt lineage that loops must report "too deep", not hang.
func TestStartRun_CyclicLineageTerminates(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	a := newCaller(t, repo)
	b := uuid.NewString()
	seedRun(t, repo, runSeed{id: b, user: runTestUser, status: db.Completed()})
	recordAgentStart(t, repo, a, b)
	// b's lineage names a as its parent, and a's names b: a loop.
	_, err := repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID: uuid.NewString(), UserID: runTestUser, Kind: core.TriggerEventKindAgentStartRun,
		DedupeKey: "loop:" + uuid.NewString(), OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"parent_chat_id": b}, Outcome: core.TriggerEventLaunched, ChatID: &a,
	})
	require.NoError(t, err)

	done := make(chan ToolResponse, 1)
	go func() {
		done <- startRunCall(t, repo, &fakeRunStarter{}, a, "toolu_loop", StartRunParams{Workflow: "builtin://agent", Message: "go"})
	}()
	select {
	case resp := <-done:
		assert.True(t, resp.IsError, "a looping lineage is refused")
	case <-time.After(10 * time.Second):
		t.Fatal("agentStartRunDepth did not terminate on a cyclic lineage")
	}
}

// Concurrency: at most MaxConcurrentAgentStartedRuns agent-started runs may be
// live (pending, running or paused) at once per user. Finished runs do not
// count.
func TestStartRun_ConcurrencyCapCountsOnlyLiveAgentStartedRuns(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	caller := newCaller(t, repo)

	live := []db.WorkflowStatus{db.Active(), db.Paused(), db.Pending()}
	for i := 0; i < MaxConcurrentAgentStartedRuns; i++ {
		child := uuid.NewString()
		seedRun(t, repo, runSeed{id: child, user: runTestUser, status: live[i%len(live)]})
		recordAgentStart(t, repo, caller, child)
	}

	// Finished runs, and a live run the HUMAN started, do not count.
	for _, finished := range []db.WorkflowStatus{db.Completed(), db.Failed(), db.Cancelled()} {
		child := uuid.NewString()
		seedRun(t, repo, runSeed{id: child, user: runTestUser, status: finished})
		recordAgentStart(t, repo, caller, child)
	}
	seedRun(t, repo, runSeed{id: uuid.NewString(), user: runTestUser, status: db.Active()})

	starter := &fakeRunStarter{}
	resp := startRunCall(t, repo, starter, caller, "toolu_cap", StartRunParams{Workflow: "builtin://agent", Message: "one too many"})
	require.True(t, resp.IsError, "the 11th live agent-started run must be refused: %s", resp.Content)
	assert.Contains(t, resp.Content, "limit is 10")
	assert.Empty(t, starter.calls(), "the launcher must not be reached past the concurrency cap")
}

func TestStartRun_FreeingASlotAllowsAnotherStart(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	caller := newCaller(t, repo)

	var children []string
	for i := 0; i < MaxConcurrentAgentStartedRuns; i++ {
		child := uuid.NewString()
		seedRun(t, repo, runSeed{id: child, user: runTestUser, status: db.Active()})
		recordAgentStart(t, repo, caller, child)
		children = append(children, child)
	}
	params := StartRunParams{Workflow: "builtin://agent", Message: "go"}
	require.True(t, startRunCall(t, repo, &fakeRunStarter{}, caller, "toolu_full", params).IsError)

	require.NoError(t, repo.UpdateWorkflowStatus(context.Background(), children[0], db.Completed()))
	starter := &fakeRunStarter{}
	resp := startRunCall(t, repo, starter, caller, "toolu_freed", params)
	require.False(t, resp.IsError, resp.Content)
	assert.Len(t, starter.calls(), 1)
}

// A retry of a start that already happened must be answered from the event
// row even when the user is AT the cap: refusing it would hide the very run it
// is trying to report.
func TestStartRun_RetryIsAnsweredEvenAtTheCap(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	caller := newCaller(t, repo)

	starter := &fakeRunStarter{repo: repo}
	params := StartRunParams{Workflow: "builtin://agent", Message: "go"}
	first := startRunCall(t, repo, starter, caller, "toolu_first", params)
	require.False(t, first.IsError, first.Content)
	for i := 1; i < MaxConcurrentAgentStartedRuns; i++ {
		child := uuid.NewString()
		seedRun(t, repo, runSeed{id: child, user: runTestUser, status: db.Active()})
		recordAgentStart(t, repo, caller, child)
	}
	require.True(t, startRunCall(t, repo, &fakeRunStarter{}, caller, "toolu_new", params).IsError, "at the cap, new work is refused")

	retry := startRunCall(t, repo, starter, caller, "toolu_first", params)
	require.False(t, retry.IsError, retry.Content)
	assert.Equal(t, startedChatID(t, first), startedChatID(t, retry))
}

func TestStartRun_ValidatesInputAndSurfacesLauncherFailure(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	caller := newCaller(t, repo)

	assert.True(t, startRunCall(t, repo, &fakeRunStarter{}, caller, "t1", StartRunParams{Message: "x"}).IsError, "workflow required")
	assert.True(t, startRunCall(t, repo, &fakeRunStarter{}, caller, "t2", StartRunParams{Workflow: "w"}).IsError, "message required")

	failing := &fakeRunStarter{err: assertErr("workflow \"nope\" not found")}
	resp := startRunCall(t, repo, failing, caller, "t3", StartRunParams{Workflow: "nope", Message: "x"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "not found")

	resp = startRunCall(t, repo, nil, caller, "t4", StartRunParams{Workflow: "w", Message: "x"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "not available")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
