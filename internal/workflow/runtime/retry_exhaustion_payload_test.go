// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// exhaustedActivityError returns the error a workflow REALLY observes when an
// activity's retry ladder runs out, produced by running a failing activity in
// the Temporal test environment rather than hand-built.
//
// It has to be the genuine article: the whole fix turns on reaching the
// activity id Temporal buries inside *ActivityError, and a synthetic stand-in
// would prove only that the test and the code agree with each other. The SDK
// does not re-export NewActivityError, which makes the real path the only path.
//
// `activities` is how many activities the workflow dispatches; the returned
// errors are in dispatch order, and Temporal assigns each a distinct id.
func exhaustedActivityError(t *testing.T, activities int) []error {
	t.Helper()

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	failing := func(context.Context) error {
		return temporal.NewApplicationError("429 Too Many Requests", "RateLimitError")
	}
	env.RegisterActivityWithOptions(failing, activity.RegisterOptions{Name: "AlwaysFails"})

	var captured []error
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Millisecond,
				BackoffCoefficient: 1.0,
				MaximumAttempts:    2,
			},
		})
		futures := make([]workflow.Future, activities)
		for i := range futures {
			futures[i] = workflow.ExecuteActivity(actCtx, "AlwaysFails")
		}
		for _, f := range futures {
			captured = append(captured, f.Get(ctx, nil))
		}
		return nil
	})
	require.True(t, env.IsWorkflowCompleted())

	for i, err := range captured {
		var activityErr *temporal.ActivityError
		require.True(t, errors.As(err, &activityErr),
			"activity %d must fail with *ActivityError, got %T", i, err)
		require.NotEmpty(t, activityErr.ActivityID(),
			"Temporal must assign the activity an id for the fix to key on")
	}
	return captured
}

// temporalActivityIDOf reads the id Temporal assigned, for asserting against.
func temporalActivityIDOf(t *testing.T, err error) string {
	t.Helper()
	var activityErr *temporal.ActivityError
	require.True(t, errors.As(err, &activityErr))
	return activityErr.ActivityID()
}

// Bug A: the exhaustion error must land on the SAME chat_updates row the
// failing activity's retry series already occupies.
//
// Both dedup paths — the frontend's dedup-by-id and the server's
// GetLatestNonMessageUpdatesPerEntity ("last write per entity wins") — collapse
// on the entity id, so an exhaustion row under a fresh uuid could not merge
// with the series it duplicates. That is what rendered one failure as two
// banners 19ms apart.
func TestExhaustionErrorReplacesActivityErrorRow(t *testing.T) {
	t.Parallel()

	// Two real failing activities, so ids come from Temporal, not from us.
	failures := exhaustedActivityError(t, 2)
	firstErr, secondErr := failures[0], failures[1]

	t.Run("exhaustion row reuses the wrapper's series id", func(t *testing.T) {
		const workflowID = "wf-1"
		temporalActivityID := temporalActivityIDOf(t, firstErr)

		// What the wrapper wrote for every attempt of this activity.
		seriesID := activityErrorEventID(workflowID, temporalActivityID)

		// What the exhaustion handler writes once the ladder is spent.
		exhaustion := retryExhaustionError{
			ChatID:     "chat-1",
			WorkflowID: workflowID,
			Message:    "429 Too Many Requests",
			Err:        firstErr,
		}

		assert.Equal(t, seriesID, exhaustion.payload()["error_id"],
			"exhaustion must REPLACE the activity's error row, not add a second one")
	})

	t.Run("different activities keep different ids", func(t *testing.T) {
		const workflowID = "wf-1"
		first := retryExhaustionError{WorkflowID: workflowID, Err: firstErr}
		second := retryExhaustionError{WorkflowID: workflowID, Err: secondErr}

		require.NotEqual(t, temporalActivityIDOf(t, firstErr), temporalActivityIDOf(t, secondErr),
			"precondition: Temporal gives each activity its own id")
		assert.NotEqual(t, first.payload()["error_id"], second.payload()["error_id"],
			"two genuinely distinct failures must not collapse into one row")
	})

	t.Run("same activity id in a different run stays distinct", func(t *testing.T) {
		// Activity ids restart at 1 each run (including a continue-as-new
		// successor), so the workflow id has to be part of the key or a later
		// run would overwrite an earlier run's recorded error.
		first := retryExhaustionError{WorkflowID: "wf-1", Err: firstErr}
		second := retryExhaustionError{WorkflowID: "wf-2", Err: firstErr}

		assert.NotEqual(t, first.payload()["error_id"], second.payload()["error_id"])
	})

	t.Run("non-activity failure falls back to a minted uuid", func(t *testing.T) {
		// The reconciler's case: a hard Temporal termination has no failing
		// activity, so there is no series row to replace. Omitting the field
		// lets WriteWorkflowError mint one, which is right for an error that is
		// its own event.
		exhaustion := retryExhaustionError{
			WorkflowID: "wf-1",
			Err:        errors.New("workflow terminated"),
		}

		_, ok := exhaustion.payload()["error_id"]
		assert.False(t, ok, "with no activity to key on, the id must be left to WriteWorkflowError")
	})

	t.Run("id is the TEMPORAL activity id, not the step name", func(t *testing.T) {
		// RunningStep.ActivityID is set to node.GetId() for backwards compat,
		// so keying on it would produce a step-named id that no wrapper row
		// ever used — and the duplicate banner would survive the fix.
		const workflowID = "wf-1"
		const stepName = "call_llm_agent" // what RunningStep.ActivityID holds
		temporalActivityID := temporalActivityIDOf(t, firstErr)

		require.NotEqual(t, stepName, temporalActivityID,
			"precondition: Temporal's id is not the step name")

		got := retryExhaustionError{WorkflowID: workflowID, Err: firstErr}.payload()["error_id"]
		assert.Equal(t, activityErrorEventID(workflowID, temporalActivityID), got)
		assert.NotEqual(t, activityErrorEventID(workflowID, stepName), got,
			"keying on the step name would not match any row the wrapper wrote")
	})
}

// Bug C: an exhaustion error that KNOWS its thread must say so.
//
// InterleavedTimeline shows a thread-less error in EVERY thread of the chat
// (deliberate, for legacy rows that predate thread attribution). So a new error
// that omits a thread it actually knows renders everywhere.
func TestExhaustionErrorCarriesThread(t *testing.T) {
	t.Parallel()
	threadTestErr := exhaustedActivityError(t, 1)[0]

	t.Run("known thread is attached", func(t *testing.T) {
		exhaustion := retryExhaustionError{
			ChatID: "chat-1",
			Thread: "thread-abc",
			Err:    threadTestErr,
		}
		assert.Equal(t, "thread-abc", exhaustion.payload()["thread"])
	})

	t.Run("unknown thread is omitted, never guessed from the chat id", func(t *testing.T) {
		exhaustion := retryExhaustionError{
			ChatID: "chat-1",
			Thread: "",
			Err:    threadTestErr,
		}

		p := exhaustion.payload()
		_, ok := p["thread"]
		require.False(t, ok, "an empty thread must be omitted, not sent as \"\"")
		assert.NotEqual(t, "chat-1", p["thread"],
			"defaulting to the chat id would assert a thread nobody reported")
	})
}

// Bug B: max_attempts must be reported for an activity on the step ladder, so
// the UI can render "Retrying (Attempt 4/5)" instead of a terminal red error.
//
// ActivityInfo.RetryPolicy is documented nil-able and IS nil for these
// activities in practice, which is why max_attempts was never written and
// is_retrying short-circuited to false on 582 of 595 error rows.
func TestResolveMaxAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info activity.Info
		want int32
	}{
		{
			name: "server policy wins when present",
			info: activity.Info{RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 7}},
			want: 7,
		},
		{
			name: "nil policy on a step dispatch falls back to the configured ladder",
			info: activity.Info{HeartbeatTimeout: activityHeartbeatTimeout, StartToCloseTimeout: 30 * 24 * time.Hour},
			want: stepActivityMaxAttempts,
		},
		{
			name: "a node timeout override is still a step dispatch",
			info: activity.Info{HeartbeatTimeout: activityHeartbeatTimeout, StartToCloseTimeout: time.Hour},
			want: stepActivityMaxAttempts,
		},
		{
			name: "infrastructure dispatch reports unknown rather than a wrong denominator",
			info: activity.Info{StartToCloseTimeout: 30 * time.Second},
			want: 0,
		},
		{
			name: "router dispatch is excluded despite sharing the heartbeat",
			info: activity.Info{HeartbeatTimeout: activityHeartbeatTimeout, StartToCloseTimeout: routerActivityStartToClose},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveMaxAttempts(tt.info))
		})
	}
}

// The invariant the badge depends on: mid-ladder is retrying, exhausted is not.
func TestIsRetryingAcrossTheLadder(t *testing.T) {
	t.Parallel()

	// The real dispatch shape of a graph step: no server policy, step heartbeat.
	stepInfo := activity.Info{
		HeartbeatTimeout:    activityHeartbeatTimeout,
		StartToCloseTimeout: 30 * 24 * time.Hour,
	}
	maxAttempts := resolveMaxAttempts(stepInfo)
	require.Equal(t, stepActivityMaxAttempts, maxAttempts,
		"a step with attempts remaining must know how many it has")

	transient := errors.New("429 Too Many Requests")
	// Calls the production predicate rather than restating it: a copy of the
	// formula here would keep passing after writeErrorEvent changed, which is
	// precisely the regression this test exists to catch.
	isRetrying := func(attempt int, err error) bool {
		return activityIsRetrying(attempt, maxAttempts, err)
	}

	for attempt := 1; attempt < int(stepActivityMaxAttempts); attempt++ {
		assert.True(t, isRetrying(attempt, transient),
			"attempt %d of %d has retries left and must render as retrying", attempt, stepActivityMaxAttempts)
	}

	assert.False(t, isRetrying(int(stepActivityMaxAttempts), transient),
		"the exhausted attempt must stay terminal-red")

	terminal := temporal.NewApplicationError("bad request", "TerminalError")
	assert.False(t, isRetrying(1, terminal),
		"a terminal error must not render as retrying however many attempts remain")
}
