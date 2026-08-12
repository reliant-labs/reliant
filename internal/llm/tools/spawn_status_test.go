// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

func runSpawnStatus(t *testing.T, tool Tool, rc *rctx.ToolContext, params SpawnStatusParams) (ToolResponse, SpawnStatusResponseMetadata) {
	t.Helper()
	inputJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(rc, ToolCall{ID: "test-call", Name: SpawnStatusToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	var meta SpawnStatusResponseMetadata
	if resp.Metadata != "" {
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	}
	return resp, meta
}

// seedMessage creates the child thread's context window (if not already
// present) and saves one message of the given role/content via
// threads.Service, mirroring how real messages land during a spawn's run.
func seedMessage(t *testing.T, repo db.Repository, ctx context.Context, chatID, threadID string, role reliantv1.MessageRole, content string) {
	t.Helper()
	if _, err := repo.GetLatestContextWindow(ctx, threadID); err != nil {
		_, cwErr := repo.CreateContextWindow(ctx, &db.ContextWindow{
			ID:       chatID + ":" + threadID + ":0",
			ThreadID: threadID,
			Sequence: 0,
		})
		require.NoError(t, cwErr)
	}
	svc := threads.NewService(repo)
	_, err := svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  threadID,
		Role:    int32(role),
		Content: content,
	})
	require.NoError(t, err)
}

// --- listing mode ---

// Listing mode must return only the caller's OWN spawn children — tool_calls
// belonging to an unrelated parent thread in the same chat must never leak
// into the listing, even though they share a chat_id.
func TestSpawnStatus_Listing_OnlyReturnsCallersOwnChildren(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerParentID, callerChildID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	// A second, unrelated parent in the SAME chat with its own child. This is
	// the leak this test guards against: tool_calls.thread_id scoping must
	// exclude it from the first parent's listing.
	otherParentID, otherChildID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	require.NotEqual(t, callerParentID, otherParentID)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerParentID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, callerChildID)
	require.NotContains(t, resp.Content, otherChildID,
		"an unrelated parent's spawn children must never appear in this thread's spawn_status listing")

	// Sanity: the unrelated parent's OWN listing does see its own child.
	otherRC := rctx.NewToolContext(ctx, chatID, otherParentID, nil, nil)
	otherResp, _ := runSpawnStatus(t, tool, otherRC, SpawnStatusParams{})
	require.Contains(t, otherResp.Content, otherChildID)
}

func TestSpawnStatus_Listing_NoChildren_ReturnsFriendlyMessage(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	threadID := uuid.New().String()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: threadID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning,
	})
	require.NoError(t, err)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, threadID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "No sub-agents")
}

// --- single-agent, non-waiting mode ---

// Single-agent mode must reject an agent_id that is not the caller's own
// spawn child — it must not be usable as a general "peek at any thread" tool.
func TestSpawnStatus_SingleAgent_RejectsNonChildAgent(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, _, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	_, unrelatedChildID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: unrelatedChildID})
	require.True(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "not a sub-agent spawned from this thread")
}

func TestSpawnStatus_SingleAgent_OwnChildWithNoMessages_ReturnsFriendlyMessage(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "no activity yet")
}

// Single-agent mode must return only the LAST assistant message, not the
// first one and not a concatenation of all of them — an orchestrator wants
// the agent's current answer, not its whole history.
func TestSpawnStatus_SingleAgent_ReturnsLastAssistantMessage_NotFirstOrConcatenated(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	seedMessage(t, repo, ctx, chatID, childID, reliantv1.MessageRole_MESSAGE_ROLE_USER, "do the thing")
	seedMessage(t, repo, ctx, chatID, childID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "first draft answer")
	seedMessage(t, repo, ctx, chatID, childID, reliantv1.MessageRole_MESSAGE_ROLE_USER, "keep going")
	seedMessage(t, repo, ctx, chatID, childID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "final answer")

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "final answer")
	require.NotContains(t, resp.Content, "first draft answer",
		"must return only the LAST assistant message, not an earlier one")
}

// --- wait mode (new) ---

// A child that is already terminal at call time must return promptly with
// wait: true — no reason to poll or block at all.
func TestSpawnStatus_Wait_ReturnsPromptlyWhenAlreadyTerminal(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	start := time.Now()
	resp, meta := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 30})
	elapsed := time.Since(start)

	require.False(t, resp.IsError, "response: %+v", resp)
	require.True(t, meta.HasExited, "an already-terminal agent must report has_exited")
	require.False(t, meta.TimedOut, "an already-terminal agent did not time out")
	require.Contains(t, resp.Content, "completed")
	require.Less(t, elapsed, 2*time.Second, "must not poll when already terminal, took %s", elapsed)
}

// A wait on a nonexistent / not-owned agent must fail fast — blocking the
// full budget and then saying "not found" wastes exactly the time waiting
// exists to save.
func TestSpawnStatus_Wait_UnknownAgentFailsFast(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, _, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	start := time.Now()
	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: uuid.New().String(), Wait: true, TimeoutSeconds: 200})
	elapsed := time.Since(start)

	require.True(t, resp.IsError, "an unowned/unknown agent_id is a real error")
	require.Less(t, elapsed, 2*time.Second, "must not consume the wait budget on an invalid agent, took %s", elapsed)
}

// A wait against a still-running agent must return a non-error, timed_out
// result once its (short) budget elapses, and must not have touched the
// agent.
func TestSpawnStatus_Wait_TimeoutIsNotAnError(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	start := time.Now()
	resp, meta := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 1})
	elapsed := time.Since(start)

	require.False(t, resp.IsError, "a still-running agent must not be reported as a tool error")
	require.True(t, meta.TimedOut, "timed_out must be true when the budget elapses")
	require.False(t, meta.HasExited, "has_exited must be false — the agent is still going")
	require.Contains(t, strings.ToUpper(resp.Content), "STILL RUNNING")
	require.Contains(t, resp.Content, "not been stopped")
	require.Contains(t, resp.Content, "wait=true")
	require.GreaterOrEqual(t, elapsed, 1*time.Second)
	require.Less(t, elapsed, 5*time.Second, "must not run past its short timeout budget, took %s", elapsed)

	// The agent itself must be untouched.
	thread, err := repo.GetThread(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, db.ThreadStatusRunning, thread.Status)
}

// timeout_seconds above the 240s ceiling must be clamped, not rejected — an
// already-terminal agent proves the call still completes normally rather
// than erroring on the oversized value.
func TestSpawnStatus_Wait_ClampsTimeoutAboveCeiling(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusCompleted)

	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, callerID, nil, nil)

	// Stated against the shared constant, not a literal. toolexec's ceiling is
	// derived from MaxBlockingToolWait with headroom, so pinning a number here
	// only re-encodes a value that moves — this assertion said 240s and went
	// stale (silently, because it skips without DATABASE_URL) when the budget
	// was raised for long-running agents.
	require.Equal(t, MaxBlockingToolWait, spawnStatusMaxTimeout,
		"spawn_status must use the shared blocking-tool budget, or the executor ceiling derived from it no longer leaves headroom")

	// Ask for an hour; the call must still return promptly because the agent
	// is already terminal.
	resp, meta := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 3600})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.True(t, meta.HasExited, "an already-terminal agent must be reported immediately regardless of the requested timeout")
}
