// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// GetWorkflowExecutions used to read step executions with a loop:
// ListWorkflowsByChat, then GetStepExecutionsByWorkflow once per workflow. On
// the worst real chat that was 83 serial queries, each a SELECT * carrying
// output_json — a column that is TOASTed out of line and made up 82 MB for that
// one chat, essentially all of which the handler then discarded. Measured
// against the dev database: 2.61s of SQL for one call, on a pool of 8
// connections, refetched on every workflow_executions pulse.
//
// These tests pin the two properties that fixed it, because both are invisible
// in the response shape and so would regress silently:
//
//  1. the number of step queries is ONE, whatever the workflow count. A
//     reintroduced loop would still return a correct tree — just slowly, which
//     no assertion about content would catch.
//  2. the saved message id of a "-save" step survives WITHOUT output_json. That
//     id is the only thing the timeline ever read out of output_json here
//     (activityIndicators.ts), and it now arrives as its own field, derived by
//     the database. Internal-activity steps carry no output at all.

// countingRepo counts the step-execution reads GetWorkflowExecutions performs.
//
// It embeds db.Repository rather than reimplementing it, so it stays a thin
// spy: every method not named here is the real one, and the counters describe
// only the calls under test.
type countingRepo struct {
	db.Repository

	mu                  sync.Mutex
	perWorkflowCalls    int
	chatScopedCalls     int
	perWorkflowArgument []string
	basicCalls          int

	// Thread-identity reads the tree walk must NOT make per workflow.
	getThreadCalls           int
	contextWindowBySeqCalls  int
	listThreadsCalls         int
	listForkedThreadIDsCalls int
}

func (r *countingRepo) GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error) {
	r.mu.Lock()
	r.basicCalls++
	r.mu.Unlock()
	return r.Repository.GetBasicStepExecutionsForChat(ctx, chatID)
}

func (r *countingRepo) GetThread(ctx context.Context, id string) (*db.Thread, error) {
	r.mu.Lock()
	r.getThreadCalls++
	r.mu.Unlock()
	return r.Repository.GetThread(ctx, id)
}

func (r *countingRepo) GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*db.ContextWindow, error) {
	r.mu.Lock()
	r.contextWindowBySeqCalls++
	r.mu.Unlock()
	return r.Repository.GetContextWindowBySequence(ctx, threadID, sequence)
}

func (r *countingRepo) ListThreadsByConversation(ctx context.Context, chatID string) ([]*db.Thread, error) {
	r.mu.Lock()
	r.listThreadsCalls++
	r.mu.Unlock()
	return r.Repository.ListThreadsByConversation(ctx, chatID)
}

func (r *countingRepo) ListForkedThreadIDs(ctx context.Context, threadIDs []string) ([]string, error) {
	r.mu.Lock()
	r.listForkedThreadIDsCalls++
	r.mu.Unlock()
	return r.Repository.ListForkedThreadIDs(ctx, threadIDs)
}

func (r *countingRepo) GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*db.StepExecution, error) {
	r.mu.Lock()
	r.perWorkflowCalls++
	r.perWorkflowArgument = append(r.perWorkflowArgument, workflowID)
	r.mu.Unlock()
	return r.Repository.GetStepExecutionsByWorkflow(ctx, workflowID)
}

func (r *countingRepo) GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error) {
	r.mu.Lock()
	r.chatScopedCalls++
	r.mu.Unlock()
	return r.Repository.GetStepExecutionsForChat(ctx, chatID)
}

func (r *countingRepo) counts() (perWorkflow, chatScoped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.perWorkflowCalls, r.chatScopedCalls
}

// seedWorkflowWithSteps creates one workflow under chatID plus its thread, and
// records the given steps against it.
func seedWorkflowWithSteps(
	t *testing.T,
	repo *db.Repo,
	ctx context.Context,
	chatID string,
	workflowID string,
	parentID *string,
	steps []*db.StepExecution,
) {
	t.Helper()
	now := time.Now().UTC()

	threadID := workflowID
	if workflowID != chatID {
		threadID = "thread-" + workflowID
		seedThread(t, repo, ctx, &db.Thread{
			ID:        threadID,
			ChatID:    chatID,
			CreatedAt: now,
			Origin:    db.ThreadOriginSpawn,
			Status:    db.ThreadStatusRunning,
		})
	} else {
		seedThread(t, repo, ctx, &db.Thread{
			ID:        threadID,
			ChatID:    chatID,
			CreatedAt: now,
			Origin:    db.ThreadOriginMain,
			Status:    db.ThreadStatusRunning,
		})
	}

	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		ParentID:     parentID,
		WorkflowName: "builtin://agent",
		Thread:       threadID,
		Status:       db.Active(),
		CreatedAt:    now,
	}))

	for _, step := range steps {
		step.WorkflowID = workflowID
		require.NoError(t, repo.CreateStepExecution(ctx, step))
	}
}

func stepOutput(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}

func saveStep(stepID, messageID string) *db.StepExecution {
	return &db.StepExecution{
		ID:           uuid.NewString(),
		StepID:       stepID,
		ActivityName: "SaveMessage",
		OutputJSON:   stepOutput(`{"message":{"role":"assistant"},"message_id":"` + messageID + `","thread":"t"}`),
		CreatedAt:    time.Now().UTC(),
	}
}

// TestGetWorkflowExecutions_OneStepQueryRegardlessOfWorkflowCount is the
// regression guard for the N+1. It asserts the count, not a duration, because a
// timing assertion on a loop this cheap in tests would pass while the loop was
// still there.
func TestGetWorkflowExecutions_OneStepQueryRegardlessOfWorkflowCount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)

	// Root plus eleven children: enough that a per-workflow loop and a single
	// query cannot be confused for one another.
	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		saveStep("call_llm-save", "msg-root"),
	})
	const childCount = 11
	for i := 0; i < childCount; i++ {
		childID := uuid.NewString()
		parent := chatID
		seedWorkflowWithSteps(t, repo, ctx, chatID, childID, &parent, []*db.StepExecution{
			saveStep("call_llm-save", "msg-child"),
		})
	}

	counting := &countingRepo{Repository: repo}
	service := &ChatService{database: counting}

	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: chatID,
		View:   reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.RootWorkflow)

	perWorkflow, chatScoped := counting.counts()
	require.Equal(t, 1, chatScoped,
		"steps must be read in exactly one chat-scoped query")
	require.Zero(t, counting.basicCalls, "FULL view must not run the BASIC query")
	require.Zero(t, perWorkflow,
		"no per-workflow step query may run; the N+1 is back (called for: %v)",
		counting.perWorkflowArgument)

	// The batching must not have cost us the data: every workflow's steps are
	// still attached to the right workflow.
	require.Len(t, resp.Msg.RootWorkflow.Steps, 1)
	require.Len(t, resp.Msg.AllRootWorkflows, 1)
	require.Len(t, resp.Msg.RootWorkflow.Children, childCount)
	for _, child := range resp.Msg.RootWorkflow.Children {
		require.Len(t, child.Steps, 1,
			"child workflow %s lost its steps", child.Id)
	}
}

// TestGetWorkflowExecutions_SavedMessageIdWithoutOutputJson pins the payload
// shape: the saved message id arrives as its own field, and the megabytes of
// internal-activity output that used to carry it do not.
func TestGetWorkflowExecutions_SavedMessageIdWithoutOutputJson(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	const userID = "test-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	chatID := seedChatForThreadTree(t, repo, ctx, userID)

	// A payload big enough that shipping it would be obvious, on an internal
	// activity — which is what the old code did for every save step.
	bulk := strings.Repeat("x", 20000)

	userFacingOutput := `{"message":"compacted","tokens_saved":1234}`

	seedWorkflowWithSteps(t, repo, ctx, chatID, chatID, nil, []*db.StepExecution{
		// A save step: its message id must survive, its output must not.
		{
			ID:           uuid.NewString(),
			StepID:       "call_llm-save",
			ActivityName: "SaveMessage",
			OutputJSON:   stepOutput(`{"message":{"role":"assistant","text":"` + bulk + `"},"message_id":"msg-42"}`),
			CreatedAt:    time.Now().UTC(),
		},
		// An internal activity that saved nothing.
		{
			ID:           uuid.NewString(),
			StepID:       "call_llm",
			ActivityName: "CallLLM",
			OutputJSON:   stepOutput(`{"response_text":"` + bulk + `"}`),
			CreatedAt:    time.Now().UTC().Add(time.Millisecond),
		},
		// A user-facing activity: this one's output IS rendered, so it ships.
		{
			ID:           uuid.NewString(),
			StepID:       "compact",
			ActivityName: "Compact",
			OutputJSON:   stepOutput(userFacingOutput),
			CreatedAt:    time.Now().UTC().Add(2 * time.Millisecond),
		},
	})

	service := &ChatService{database: repo}
	resp, err := service.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: chatID,
		View:   reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.RootWorkflow)

	byStepID := map[string]*reliantv1.StepExecution{}
	for _, step := range resp.Msg.RootWorkflow.Steps {
		byStepID[step.StepId] = step
	}
	require.Len(t, byStepID, 3)

	save := byStepID["call_llm-save"]
	require.NotNil(t, save)
	require.Equal(t, "msg-42", save.GetSavedMessageId(),
		"the timeline reads this to tell a step that produced a message from one that needs an activity indicator")
	require.Empty(t, save.GetOutputJson(),
		"a save step's output_json is 37MB per chat in production and nothing reads it")

	internal := byStepID["call_llm"]
	require.NotNil(t, internal)
	require.Empty(t, internal.GetOutputJson(),
		"internal activities are %v and their output is never rendered", model.InternalActivities)
	require.Empty(t, internal.GetSavedMessageId(),
		"a step that saved no message must not claim one")

	// The exception that keeps the workflow viewer's Output panel working.
	userFacing := byStepID["compact"]
	require.NotNil(t, userFacing)
	require.Equal(t, userFacingOutput, userFacing.GetOutputJson(),
		"user-facing activity output is what ActivityIndicator renders; it is ~23kB per chat, not 82MB")
}
