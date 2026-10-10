// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reconciler's ReapOrphanedThreads runs every pass, on its own
// connection, and closes any thread still running under a terminal workflow,
// logging the repair as a missed cascade. So a status write that moves the
// workflow and its thread in two separate commits is a race with it: a pass
// that lands between the two either re-closes the thread of a run that is
// starting, or reaps — and pages about — a cascade that was one statement
// away from happening.
//
// reapingRepo puts a reconciler pass between every pair of writes the
// activity makes: before and after each one it runs ReapOrphanedThreads on
// context.Background() — never the activity's transaction, exactly as the
// reconciler would — and keeps whatever that pass reaped.
type reapingRepo struct {
	db.Repository
	t      *testing.T
	reaped []db.ReapedThread
}

func (r *reapingRepo) reconcilerPass() {
	reaped, err := r.Repository.ReapOrphanedThreads(context.Background())
	require.NoError(r.t, err)
	r.reaped = append(r.reaped, reaped...)
}

func (r *reapingRepo) around(write func() error) error {
	r.reconcilerPass()
	err := write()
	r.reconcilerPass()
	return err
}

func (r *reapingRepo) UpdateWorkflowStatus(ctx context.Context, id string, status db.WorkflowStatus) error {
	return r.around(func() error { return r.Repository.UpdateWorkflowStatus(ctx, id, status) })
}

func (r *reapingRepo) CascadeTerminalStatusToDescendants(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error {
	return r.around(func() error { return r.Repository.CascadeTerminalStatusToDescendants(ctx, workflowID, reason) })
}

func (r *reapingRepo) CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error {
	return r.around(func() error { return r.Repository.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, reason) })
}

func (r *reapingRepo) ReviveThread(ctx context.Context, threadID string) (int64, error) {
	var revived int64
	err := r.around(func() error {
		var err error
		revived, err = r.Repository.ReviveThread(ctx, threadID)
		return err
	})
	return revived, err
}

// reapedIn is what the interleaved passes reaped in chatID.
func (r *reapingRepo) reapedIn(chatID string) []db.ReapedThread {
	var out []db.ReapedThread
	for _, thread := range r.reaped {
		if thread.ChatID == chatID {
			out = append(out, thread)
		}
	}
	return out
}

// A chat's main thread ends turn N with its workflow. Turn N+1's "started"
// must bring both back without a reconciler pass in between re-closing the
// thread: before the fix the thread was revived FIRST, a pass in the gap saw
// "running thread, completed workflow", closed the thread again, and the run
// then executed behind a thread reading completed — the state ReviveThread
// exists to prevent — while the reap paged as a missed cascade.
func TestWorkflowStatus_StartedNeverExposesARevivedThreadUnderATerminalWorkflow(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	turn := func(status string) WorkflowStatusInput {
		return WorkflowStatusInput{
			ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://chat",
			Status: status, Thread: chatID,
		}
	}

	// Turn 1 runs and completes; both halves end terminal.
	plain := NewWorkflowStatusActivity(h.Repo())
	var output WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(plain.Execute, turn("started"), &output))
	require.NoError(t, h.ExecuteActivity(plain.Execute, turn("completed"), &output))
	thread, err := h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	require.Equal(t, db.ThreadStatusCompleted, thread.Status, "precondition: turn 1 closed the thread")

	// Turn 2 starts with a reconciler pass between every write.
	repo := &reapingRepo{Repository: h.Repo(), t: t}
	interleaved := NewWorkflowStatusActivity(repo)
	require.NoError(t, h.ExecuteActivity(interleaved.Execute, turn("started"), &output))

	assert.Empty(t, repo.reapedIn(chatID),
		"no pass may ever see the starting run's thread running under the previous turn's terminal workflow")
	thread, err = h.Repo().GetThread(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.ThreadStatusRunning, thread.Status,
		"the thread of a run that is starting must read running, not the status a reap stamped on it")
	workflow, err := h.Repo().GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), workflow.Status)
}

// Every terminal arm that runs code ends the workflow, its descendants and
// their threads in ONE commit. Before the fix each arm committed the workflow
// status and then cascaded in later statements, so a pass between them saw a
// terminal workflow over running threads and reaped them as a missed cascade
// — an ERROR that paged for a run whose cascade was one statement away.
func TestWorkflowStatus_TerminalArmsCascadeInTheStatusCommit(t *testing.T) {
	for _, tc := range []struct {
		status     string
		wantThread int32
		wantRun    db.WorkflowStatus
	}{
		{status: "completed", wantThread: db.ThreadStatusCompleted, wantRun: db.Completed()},
		{status: "failed", wantThread: db.ThreadStatusFailed, wantRun: db.Failed()},
		{status: "cancelled", wantThread: db.ThreadStatusCancelled, wantRun: db.Cancelled()},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()

			ctx := context.Background()
			userID := uuid.New().String()
			projectID := uuid.New().String()
			chatID := uuid.New().String()
			h.CreateTestProject(ctx, projectID, userID)
			h.CreateTestChat(ctx, chatID, projectID, userID)

			input := WorkflowStatusInput{
				ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://chat",
				Status: "started", Thread: chatID,
			}
			plain := NewWorkflowStatusActivity(h.Repo())
			var output WorkflowStatusOutput
			require.NoError(t, h.ExecuteActivity(plain.Execute, input, &output))

			// A sub-agent running under the run: its workflow row and its
			// own thread must end with the run, in the same commit.
			childWorkflowID := uuid.New().String()
			parentID := chatID
			require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
				ID: childWorkflowID, ParentID: &parentID, ChatID: chatID,
				WorkflowName: "builtin://agent", Thread: childWorkflowID,
				Status: db.Active(), CreatedAt: time.Now(),
			}))
			_, err := h.Repo().CreateThread(ctx, &db.Thread{
				ID: childWorkflowID, ChatID: chatID, WorkflowID: &childWorkflowID,
				ParentThreadID: &parentID, CreatedAt: time.Now(), Status: db.ThreadStatusRunning,
			})
			require.NoError(t, err)

			repo := &reapingRepo{Repository: h.Repo(), t: t}
			interleaved := NewWorkflowStatusActivity(repo)
			input.Status = tc.status
			require.NoError(t, h.ExecuteActivity(interleaved.Execute, input, &output))

			assert.Empty(t, repo.reapedIn(chatID),
				"a pass between the status write and the cascade must find nothing to reap")
			for _, threadID := range []string{chatID, childWorkflowID} {
				thread, err := h.Repo().GetThread(ctx, threadID)
				require.NoError(t, err)
				assert.Equal(t, tc.wantThread, thread.Status, "thread %s ends with its run", threadID)
			}
			workflow, err := h.Repo().GetWorkflow(ctx, chatID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantRun, workflow.Status)
			child, err := h.Repo().GetWorkflow(ctx, childWorkflowID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantRun.StopReason, child.Status.StopReason, "the sub-agent's row ends with its run")
		})
	}
}
