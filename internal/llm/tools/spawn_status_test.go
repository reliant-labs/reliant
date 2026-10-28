// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
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
func seedMessage(t *testing.T, repo *db.Repo, ctx context.Context, chatID, threadID string, role reliantv1.MessageRole, content string) {
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
	t.Parallel()
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

// A child parked until a provider's usage limit resets is WAITING, not failed:
// the parent is told when it carries on, so it neither resumes it behind its
// back nor spawns a duplicate.
func TestSpawnStatus_Listing_ShowsAChildWaitingOnAUsageLimit(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	parentID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	tool := NewSpawnStatusTool(repo)
	rc := rctx.NewToolContext(ctx, chatID, parentID, nil, nil)

	resp, _ := runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.NotContains(t, resp.Content, "waiting for usage limit", "a child that is not waiting reads as running")

	resumeAt := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Second)
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, childID, "Anthropic", false, resumeAt, time.Now()))

	resp, _ = runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.Contains(t, resp.Content, "Status: waiting for usage limit")
	require.Contains(t, resp.Content, "Anthropic's usage limit until "+resumeAt.Format(time.RFC3339))
	require.Contains(t, resp.Content, "not failed and continues by itself")

	require.NoError(t, repo.ClearUsageLimitWait(ctx, chatID, childID, time.Now()))
	resp, _ = runSpawnStatus(t, tool, rc, SpawnStatusParams{})
	require.NotContains(t, resp.Content, "waiting for usage limit", "back to running once the wait is over")
}

func TestSpawnStatus_Listing_NoChildren_ReturnsFriendlyMessage(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A wait cut short — a message arrived for the caller, or it was interrupted —
// is not an error, says it stopped early, and leaves the agent alone.
func TestSpawnStatus_Wait_CutShortSaysItStoppedEarly(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)

	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	rc := rctx.NewToolContext(callCtx, chatID, callerID, nil, nil)

	resp, meta := runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 60})

	require.False(t, resp.IsError, "response: %+v", resp)
	require.True(t, meta.StoppedEarly, "metadata: %+v", meta)
	require.False(t, meta.TimedOut, "the budget did not run out")
	require.False(t, meta.HasExited)
	require.Contains(t, resp.Content, "Stopped waiting")
	require.Contains(t, resp.Content, "has not been stopped")

	thread, err := repo.GetThread(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, db.ThreadStatusRunning, thread.Status, "cutting the wait short must not touch the agent")
}

// The agent being waited on finishing is ITSELF a message to the caller — its
// completion report — so the commonest reason this wait is preempted is that it
// was about to succeed. The wait must report the finish, not "still running".
func TestSpawnStatus_Wait_CutShortReportsTheAgentFinishing(t *testing.T) {
	t.Parallel()
	for _, cutAt := range []string{"initial read", "waiting", "terminal snapshot", "activity snapshot"} {
		t.Run(cutAt, func(t *testing.T) {
			t.Parallel()
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			childID := "child"
			reads := 0
			// The repository callback is the barrier: completion and delivery
			// of its report happen at a specific read, never on a wall clock.
			// Like the real store, reads on a cancelled context fail, NOT vanish.
			repo := &MockspawnStatusStore{
				ListSpawnChildrenFunc: func(ctx context.Context, parentID string) ([]*db.SpawnChild, error) {
					require.NoError(t, ctx.Err())
					require.Equal(t, "parent", parentID)
					return []*db.SpawnChild{{ChildThreadID: &childID}}, nil
				},
				GetThreadFunc: func(ctx context.Context, id string) (*db.Thread, error) {
					require.Equal(t, childID, id)
					reads++
					if reads == 1 {
						switch cutAt {
						case "initial read":
							cancel()
						case "waiting":
							cancel()
							return &db.Thread{ID: childID, Status: db.ThreadStatusRunning}, nil
						case "terminal snapshot":
							cancel()
							return &db.Thread{ID: childID, Status: db.ThreadStatusCompleted}, nil
						}
					}
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					return &db.Thread{ID: childID, Status: db.ThreadStatusCompleted}, nil
				},
				GetLatestContextWindowFunc: func(ctx context.Context, id string) (*db.ContextWindow, error) {
					if cutAt == "activity snapshot" {
						cancel()
					}
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					// Final status AND answer must use a live, bounded context.
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.LessOrEqual(t, time.Until(deadline), stoppedEarlyCheckTimeout)
					return &db.ContextWindow{ID: "window", ThreadID: id}, nil
				},
				GetMessagesByContextWindowFunc: func(ctx context.Context, _ string, _ *int64) ([]*db.Message, error) {
					return []*db.Message{{ID: "answer", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT}}, ctx.Err()
				},
				ListContentBlocksFunc: func(ctx context.Context, _ string) ([]*db.MessageContentBlock, error) {
					answer := "the answer is 42"
					return []*db.MessageContentBlock{{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: &answer}}, ctx.Err()
				},
			}
			rc := rctx.NewToolContext(callCtx, "chat", "parent", nil, nil)
			resp, meta := runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 60})

			require.False(t, resp.IsError, "response: %+v", resp)
			require.True(t, meta.HasExited, "the agent finished; metadata: %+v", meta)
			require.Equal(t, "completed", meta.Status)
			require.Equal(t, childID, meta.AgentID)
			require.False(t, meta.StoppedEarly)
			require.False(t, meta.TimedOut)
			require.Contains(t, resp.Content, "the answer is 42")
			require.ErrorIs(t, callCtx.Err(), context.Canceled)
		})
	}
}

// A failed initial read is retried only when cancellation cut it short.
// A still-running child stays untouched; a genuinely missing child remains
// an error even after the detached last look.
func TestSpawnStatus_Wait_InitialReadCancellationPreservesStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		cancelRead bool
		missing    bool
	}{
		{name: "cancelled read of running child", cancelRead: true},
		{name: "cancelled read of missing child", cancelRead: true, missing: true},
		{name: "missing child", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			childID := "child"
			reads := 0
			repo := &MockspawnStatusStore{
				ListSpawnChildrenFunc: func(context.Context, string) ([]*db.SpawnChild, error) {
					return []*db.SpawnChild{{ChildThreadID: &childID}}, nil
				},
				GetThreadFunc: func(ctx context.Context, _ string) (*db.Thread, error) {
					reads++
					if reads == 1 && tc.cancelRead {
						cancel()
						return nil, ctx.Err()
					}
					require.NoError(t, ctx.Err())
					if tc.missing {
						return nil, nil
					}
					return &db.Thread{ID: childID, Status: db.ThreadStatusRunning}, nil
				},
			}
			rc := rctx.NewToolContext(callCtx, "chat", "parent", nil, nil)
			resp, meta := runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 60})
			require.Equal(t, tc.missing, resp.IsError, "response: %+v", resp)
			if tc.missing {
				require.Contains(t, resp.Content, "could not be found")
			} else {
				require.True(t, meta.StoppedEarly)
				require.False(t, meta.TimedOut)
				require.False(t, meta.HasExited)
				require.Equal(t, "running", meta.Status)
				require.Contains(t, resp.Content, "has not been stopped")
			}
			if tc.cancelRead {
				require.Equal(t, 2, reads, "one bounded last look, not a new wait")
			} else {
				require.Equal(t, 1, reads, "a genuine miss must fail fast")
			}
		})
	}
}

// Cancellation coinciding with a genuine store failure does not make that
// failure recoverable. A retry could otherwise hide a database/auth error.
func TestSpawnStatus_Wait_CancellationDoesNotMaskStoreFailure(t *testing.T) {
	t.Parallel()
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	childID := "child"
	storeErr := errors.New("permission denied by store")
	reads := 0
	repo := &MockspawnStatusStore{
		ListSpawnChildrenFunc: func(context.Context, string) ([]*db.SpawnChild, error) {
			return []*db.SpawnChild{{ChildThreadID: &childID}}, nil
		},
		GetThreadFunc: func(context.Context, string) (*db.Thread, error) {
			reads++
			if reads == 1 {
				cancel()
				return nil, storeErr
			}
			return &db.Thread{ID: childID, Status: db.ThreadStatusCompleted}, nil
		},
	}
	rc := rctx.NewToolContext(callCtx, "chat", "parent", nil, nil)
	resp, _ := runSpawnStatus(t, NewSpawnStatusTool(repo), rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 60})
	require.True(t, resp.IsError, "a coincident cancellation must not recover an unrelated store error")
	require.Equal(t, 1, reads, "non-cancellation errors must not trigger a detached retry")
	require.Equal(t, "Agent \"child\" could not be found.", resp.Content, "preserve the existing store-error response")
}

// timeout_seconds above the 240s ceiling must be clamped, not rejected — an
// already-terminal agent proves the call still completes normally rather
// than erroring on the oversized value.
func TestSpawnStatus_Wait_ClampsTimeoutAboveCeiling(t *testing.T) {
	t.Parallel()
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
	// stale (silently, while DB-backed tests skipped without a configured
	// database) when the budget was raised for long-running agents.
	require.Equal(t, MaxBlockingToolWait, spawnStatusMaxTimeout,
		"spawn_status must use the shared blocking-tool budget, or the executor ceiling derived from it no longer leaves headroom")

	// Ask for an hour; the call must still return promptly because the agent
	// is already terminal.
	resp, meta := runSpawnStatus(t, tool, rc, SpawnStatusParams{AgentID: childID, Wait: true, TimeoutSeconds: 3600})
	require.False(t, resp.IsError, "response: %+v", resp)
	require.True(t, meta.HasExited, "an already-terminal agent must be reported immediately regardless of the requested timeout")
}
