// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancellingTemporal is a Temporal client that can cancel cooperatively. Each
// query answers from endingAfter: the run reports ending from that query on (0
// = never); queryErr makes every query fail, as a worker too old to know the
// query would.
type cancellingTemporal struct {
	*fakeTemporal
	cancelled   []string
	cancelErr   error
	queries     int
	endingAfter int
	queryErr    error
	// closeOnQuery, when > 0, flips Describe to a closed status from that
	// query on, for a run that closes without answering.
	closeOnQuery int
	// onQuery, when set, runs on every query with its 1-based number: a run
	// whose completion code writes its row as it goes.
	onQuery func(n int)
}

func (c *cancellingTemporal) CancelWorkflow(_ context.Context, workflowID, _ string) error {
	c.cancelled = append(c.cancelled, workflowID)
	return c.cancelErr
}

func (c *cancellingTemporal) QueryWorkflow(_ context.Context, _, _, queryType string, _ ...interface{}) (converter.EncodedValue, error) {
	c.queries++
	if c.onQuery != nil {
		c.onQuery(c.queries)
	}
	if c.closeOnQuery > 0 && c.queries >= c.closeOnQuery {
		c.status = enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
	}
	if queryType != model.RunStopQuery {
		return nil, errors.New("unknown query " + queryType)
	}
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return stopValue{model.RunStopState{Ending: c.endingAfter > 0 && c.queries >= c.endingAfter}}, nil
}

// stopValue is a query result holding a RunStopState.
type stopValue struct{ state model.RunStopState }

func (v stopValue) HasValue() bool { return true }

func (v stopValue) Get(valuePtr interface{}) error {
	ptr, ok := valuePtr.(*model.RunStopState)
	if !ok {
		return errors.New("stopValue holds a RunStopState")
	}
	*ptr = v.state
	return nil
}

func cancelFixture(t *testing.T) (*fakeRepo, *cancellingTemporal, *Service) {
	t.Helper()
	repo, temporal, pause, _ := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	c := &cancellingTemporal{fakeTemporal: temporal}
	svc := NewService(repo, c, pause)
	svc.cancelGrace, svc.cancelSettle, svc.cancelPoll = 50*time.Millisecond, 50*time.Millisecond, time.Millisecond
	return repo, c, svc
}

// A run that answers it is ending, then writes its own terminal row, stops
// itself: it is cancelled, not terminated, and Terminate writes neither its
// status nor its cascade (which would race the run's own write).
func TestTerminate_RunThatEndsAndWritesItsRowIsCancelledNotTerminated(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)
	temporal.endingAfter = 1
	// The run's completion code has committed its cancelled status by the
	// time Terminate looks at the row.
	temporal.onQuery = func(int) { repo.workflow.Status = db.Cancelled() }
	// A settle wait far longer than the test may take: Terminate must see the
	// written row and return at once, not wait the budget out.
	svc.cancelSettle = time.Minute

	start := time.Now()
	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))
	assert.Less(t, time.Since(start), 20*time.Second, "a run whose row is terminal has settled; Terminate must not wait out the settle budget")

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated, "a run that is ending on its own must not be terminated: that cuts off its cleanup")
	assert.Empty(t, repo.casCalls, "the run wrote its own cancelled status")
	assert.Empty(t, repo.updatedStatuses, "and Terminate did not write over it")
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints, "the next message must still start a fresh run")
	assert.Equal(t, []string{"chat-1|"}, repo.clearedUsageLimit)
}

// The review's case: the run says it is ending — the query is answered the
// moment its completion defer starts — and then its execution CLOSES without
// its row having been written (it completed concurrently with the cancel, or
// its status write failed). Terminate must not return as though the database
// agreed: it reconciles the row, as it does for a run it found closed.
func TestTerminate_RunThatAcknowledgesThenClosesWithItsRowLiveIsReconciled(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)
	temporal.endingAfter = 1
	temporal.closeOnQuery = 1

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Empty(t, temporal.terminated, "the execution is closed: there is nothing to terminate")
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"], "the row the run left live is moved to cancelled")
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason, "with its cascade")
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
}

// A run still running its finally handler when the settle wait ends is live
// in Temporal and live in the database, which agree: it is left to finish its
// cleanup and write its own status, never terminated mid-handler, and its row
// is not written over by the caller.
func TestTerminate_RunStillCleaningUpIsLeftToFinish(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)
	temporal.endingAfter = 1

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Empty(t, temporal.terminated)
	assert.Empty(t, repo.casCalls, "the run, still executing, owns its terminal write")
	assert.Empty(t, repo.updatedStatuses, "and nothing writes over its row")
}

// A worker that does not know the query still lets Terminate see the run
// close, and the row it left is reconciled.
func TestTerminate_RunThatClosesWithoutAnsweringIsNotTerminated(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)
	temporal.queryErr = errors.New("unknown queryType get_run_stop")
	temporal.closeOnQuery = 2

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
}

// A run that never observes the cancel is terminated after the grace, with the
// bookkeeping a terminate needs.
func TestTerminate_RunThatIgnoresTheCancelIsTerminatedAfterTheGrace(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Equal(t, []string{"wf-1"}, temporal.terminated)
	require.Len(t, repo.casCalls, 1)
	assert.Equal(t, db.Cancelled(), repo.casCalls[0].NewStatus)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
}

func TestTerminate_CancelRequestThatFailsFallsBackToTerminate(t *testing.T) {
	_, temporal, svc := cancelFixture(t)
	temporal.cancelErr = errors.New("unavailable")

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.terminated)
	assert.Zero(t, temporal.queries, "a cancel that was never delivered is not waited on")
}

// A second cancel of a run already running its cleanup must not terminate it:
// an agent that retries control_run cancel would otherwise cut off the very
// cleanup the first cancel started.
func TestTerminate_SecondCancelDuringCleanupDoesNotTerminate(t *testing.T) {
	_, temporal, svc := cancelFixture(t)
	temporal.endingAfter = 1

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))
	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1", "wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
}

// A run Temporal already reports closed is reconciled as before; nothing is
// cancelled.
func TestTerminate_ClosedRunIsNotCancelled(t *testing.T) {
	_, temporal, svc := cancelFixture(t)
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Empty(t, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
}
