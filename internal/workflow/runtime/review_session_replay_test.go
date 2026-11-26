// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A review node's session is derived from workflow state that a replay
// rebuilds (reviewSession), so replaying a recorded run derives the threads
// the run used. The probe re-enters one review node over two rounds of a
// loop and twice at one position, hands each derived thread to an activity
// (what the run recorded), and reports what it derived; the replay must
// derive the same threads, in the same order.

const reviewSessionProbeName = "review-session-probe"

func reviewSessionProbe(t *testing.T, derived *[]string) func(workflow.Context) ([]string, error) {
	review, _ := loweredReviewNode(t)
	return func(ctx workflow.Context) ([]string, error) {
		*derived = nil
		ac := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		tracker := &ChildWorkflowTracker{}
		root := NewExecutionContext(workflow.GetInfo(ctx).WorkflowExecution.ID, "chat", "probe", "thread-root")
		parents := []*ExecutionContext{
			root.ForIteration(0, true).WithLoop("rounds", 0),
			root.ForIteration(1, true).WithLoop("rounds", 1),
			root,
			root,
		}
		var recorded []string
		for _, parent := range parents {
			session := workflowNodeContext(ctx, tracker, parent, review, "rounds.review", model.ThreadModeNew, model.ReviewAgentRef, true)
			*derived = append(*derived, session.Thread)
			var thread string
			if err := workflow.ExecuteActivity(ac, "ReviewBrief", session.Thread).Get(ctx, &thread); err != nil {
				return nil, err
			}
			recorded = append(recorded, thread)
		}
		return recorded, nil
	}
}

func TestReviewSession_ReplayDerivesTheSameThreads(t *testing.T) {
	s := startCANProbeServer(t)
	var live []string
	w := worker.New(s.client, "review-session", temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(reviewSessionProbe(t, &live), workflow.RegisterOptions{Name: reviewSessionProbeName})
	w.RegisterActivityWithOptions(func(_ context.Context, thread string) (string, error) { return thread, nil },
		activity.RegisterOptions{Name: "ReviewBrief"})
	require.NoError(t, w.Start())
	defer w.Stop()
	run, err := s.client.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: "review-session", TaskQueue: "review-session", WorkflowExecutionTimeout: 2 * time.Minute,
	}, reviewSessionProbeName)
	require.NoError(t, err)
	var recorded []string
	require.NoError(t, run.Get(context.Background(), &recorded))
	require.Len(t, recorded, 4)
	seen := map[string]bool{}
	for _, thread := range recorded {
		assert.False(t, seen[thread], "every execution has its own session: %v", recorded)
		seen[thread] = true
	}

	var replayed []string
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflowWithOptions(reviewSessionProbe(t, &replayed), workflow.RegisterOptions{Name: reviewSessionProbeName})
	// As a worker replays it: under the run's own workflow id (the replayer's
	// default is a placeholder, which no production replay sees).
	require.NoError(t, r.ReplayWorkflowHistoryWithOptions(
		tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		s.history(run.GetID(), run.GetRunID()),
		worker.ReplayWorkflowHistoryOptions{OriginalExecution: workflow.Execution{ID: run.GetID(), RunID: run.GetRunID()}},
	))
	assert.Equal(t, recorded, replayed, "a replay derives the threads the run recorded")
}
