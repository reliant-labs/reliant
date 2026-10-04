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
)

// GetWorkflowExecutions' BASIC view returns what the chat timeline renders —
// user-facing steps and the "-save" siblings that recorded a message — and not
// the internal plumbing that is 99.9% of the rows. These tests run against a
// real database because the contract lives in the SQL (the literal activity
// list, the loop-scoped sibling join), not in Go.

func basicViewStepIDs(t *testing.T, view reliantv1.WorkflowExecutionView, repo db.Repository, ctx context.Context, chatID string) (map[string]*reliantv1.StepExecution, *countingRepo) {
	t.Helper()
	counting := &countingRepo{Repository: repo}
	service := &ChatService{database: counting}
	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: chatID,
		View:   view,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.RootWorkflow)
	out := map[string]*reliantv1.StepExecution{}
	for _, step := range resp.Msg.RootWorkflow.Steps {
		key := step.StepId
		if step.LoopIteration != nil {
			key += "#" + string(rune('0'+*step.LoopIteration))
		}
		out[key] = step
	}
	return out, counting
}

func loopStep(stepID, activity, loopNode string, iteration int64, at time.Time, output string) *db.StepExecution {
	step := &db.StepExecution{
		ID:           uuid.NewString(),
		StepID:       stepID,
		ActivityName: activity,
		OutputJSON:   stepOutput(output),
		CreatedAt:    at,
	}
	step.LoopNodeID.String, step.LoopNodeID.Valid = loopNode, true
	step.LoopIteration.Int64, step.LoopIteration.Valid = iteration, true
	return step
}

func TestGetWorkflowExecutions_BasicViewReturnsOnlyTimelineSteps(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	base := time.Now().UTC()

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		// Internal plumbing: never returned.
		{ID: uuid.NewString(), StepID: "call_llm", ActivityName: "CallLLM", OutputJSON: stepOutput(`{"x":1}`), CreatedAt: base},
		{ID: uuid.NewString(), StepID: "exec_tools", ActivityName: "ExecuteTools", OutputJSON: stepOutput(`{"x":1}`), CreatedAt: base.Add(time.Millisecond)},
		// A save with no matching user-facing step: not a sibling of anything.
		saveStep("call_llm-save", "msg-orphan"),
		// User-facing step whose -save recorded a message: both returned.
		{ID: uuid.NewString(), StepID: "compact", ActivityName: "Compact", OutputJSON: stepOutput(`{"tokens":7}`), CreatedAt: base.Add(2 * time.Millisecond)},
		{ID: uuid.NewString(), StepID: "compact-save", ActivityName: "SaveMessage",
			OutputJSON: stepOutput(`{"message_id":"msg-compact"}`), CreatedAt: base.Add(3 * time.Millisecond)},
		// User-facing step whose -save has NO message id: no sibling.
		{ID: uuid.NewString(), StepID: "summarize", ActivityName: "Summarize", OutputJSON: stepOutput(`{"s":1}`), CreatedAt: base.Add(4 * time.Millisecond)},
		{ID: uuid.NewString(), StepID: "summarize-save", ActivityName: "SaveMessage",
			OutputJSON: stepOutput(`{"note":"nothing saved"}`), CreatedAt: base.Add(5 * time.Millisecond)},
	})

	steps, counting := basicViewStepIDs(t, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC, repo, ctx, chatID)

	require.Equal(t, 1, counting.basicCalls, "BASIC must use exactly one chat-scoped query")
	require.Zero(t, counting.chatScopedCalls, "BASIC must not run the FULL query")
	require.Zero(t, counting.perWorkflowCalls)

	require.Len(t, steps, 3, "got %v", keys(steps))
	require.Equal(t, `{"tokens":7}`, steps["compact"].GetOutputJson(), "user-facing output is what ActivityIndicator renders")
	require.Equal(t, "msg-compact", steps["compact-save"].GetSavedMessageId())
	require.Empty(t, steps["compact-save"].GetOutputJson())
	require.Contains(t, steps, "summarize")
	require.NotContains(t, steps, "summarize-save", "a save without saved_message_id is not a sibling")
	require.NotContains(t, steps, "call_llm")
	require.NotContains(t, steps, "exec_tools")
	require.NotContains(t, steps, "call_llm-save", "a save whose step is internal belongs to no visible step")
}

func TestGetWorkflowExecutions_BasicViewSiblingIsLoopScoped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	base := time.Now().UTC()

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		// Iteration 2 saved a message; iteration 3 of the same step did not.
		loopStep("review", "Review", "loop-a", 2, base, `{"i":2}`),
		loopStep("review-save", "SaveMessage", "loop-a", 2, base.Add(time.Millisecond), `{"message_id":"msg-iter2"}`),
		loopStep("review", "Review", "loop-a", 3, base.Add(2*time.Millisecond), `{"i":3}`),
	})

	steps, _ := basicViewStepIDs(t, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC, repo, ctx, chatID)

	require.Contains(t, steps, "review#2")
	require.Contains(t, steps, "review#3")
	require.Contains(t, steps, "review-save#2")
	require.NotContains(t, steps, "review-save#3", "iteration 3 must not pick up iteration 2's save")
	require.Len(t, steps, 3)
}

func TestGetWorkflowExecutions_FullViewReturnsInternalSteps(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)
	base := time.Now().UTC()

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		{ID: uuid.NewString(), StepID: "call_llm", ActivityName: "CallLLM", OutputJSON: stepOutput(`{"x":1}`), CreatedAt: base},
		saveStep("call_llm-save", "msg-1"),
		{ID: uuid.NewString(), StepID: "compact", ActivityName: "Compact", OutputJSON: stepOutput(`{"t":1}`), CreatedAt: base.Add(time.Millisecond)},
	})

	steps, counting := basicViewStepIDs(t, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL, repo, ctx, chatID)
	require.Len(t, steps, 3)
	require.Equal(t, 1, counting.chatScopedCalls)
	require.Zero(t, counting.basicCalls)
}

// The tree used to call GetThread, and for child threads
// GetContextWindowBySequence, once per workflow. Thread identity now arrives in
// one ListThreadsByConversation and fork-ness in one ListForkedThreadIDs.
func TestGetWorkflowExecutions_ThreadTreeHasNoPerWorkflowQueries(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, nil)
	const childCount = 9
	for i := 0; i < childCount; i++ {
		parent := chatID
		seedWorkflowWithSteps(t, repo, ctx, chatID, uuid.NewString(), &parent, nil)
	}

	// One more child whose thread names a parent, so the batched fork lookup
	// has something to ask about.
	forkWorkflowID := uuid.NewString()
	forkThreadID := "thread-" + forkWorkflowID
	parentThread := chatID
	seedThread(t, repo, ctx, &db.Thread{
		ID:             forkThreadID,
		ChatID:         chatID,
		ParentThreadID: &parentThread,
		CreatedAt:      time.Now().UTC(),
		Origin:         db.ThreadOriginSpawn,
		Status:         db.ThreadStatusRunning,
	})
	rootID := chatID
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: forkWorkflowID, ChatID: chatID, ParentID: &rootID,
		WorkflowName: "builtin://agent", Thread: forkThreadID,
		Status: db.Active(), CreatedAt: time.Now().UTC(),
	}))

	counting := &countingRepo{Repository: repo}
	service := &ChatService{database: counting}
	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.RootWorkflow.Children, childCount+1)
	for _, child := range resp.Msg.RootWorkflow.Children {
		require.Equal(t, "spawn", child.Origin, "thread metadata must still be attached")
	}

	require.Zero(t, counting.getThreadCalls, "GetThread per workflow is back")
	require.Zero(t, counting.contextWindowBySeqCalls, "GetContextWindowBySequence per workflow is back")
	require.Equal(t, 1, counting.listThreadsCalls, "threads must be listed once and shared")
	require.Equal(t, 1, counting.listForkedThreadIDsCalls, "fork-ness must be one batched query")
}

func keys(m map[string]*reliantv1.StepExecution) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
