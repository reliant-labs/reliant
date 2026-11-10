// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
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
	// onDescribe, likewise for every Describe: the settle phase reads only
	// Temporal's state, so a run that moves while it is settling moves here.
	onDescribe func(n int)
	describes  int
}

func (c *cancellingTemporal) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.describes++
	if c.onDescribe != nil {
		c.onDescribe(c.describes)
	}
	return c.fakeTemporal.DescribeWorkflowExecution(ctx, workflowID, runID)
}

// fakeClock is the service's clock in these tests: sleeping advances it, so
// no assertion depends on wall time and every wait ends at once.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time                           { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) { c.t = c.t.Add(d) }

// waited is how much clock time the waits consumed.
func (c *fakeClock) waited() time.Duration { return c.t.Sub(clockEpoch) }

var clockEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

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

// Budgets of the cancel waits in these tests, on the fake clock.
const (
	testGrace  = 15 * time.Second
	testSettle = 30 * time.Second
	testPoll   = time.Second
)

func cancelFixture(t *testing.T) (*fakeRepo, *cancellingTemporal, *Service, *fakeClock) {
	t.Helper()
	repo, temporal, pause, _ := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	c := &cancellingTemporal{fakeTemporal: temporal}
	svc := NewService(repo, c, pause)
	clock := &fakeClock{t: clockEpoch}
	svc.cancelGrace, svc.cancelSettle, svc.cancelPoll = testGrace, testSettle, testPoll
	svc.now, svc.sleep = clock.now, clock.sleep
	return repo, c, svc, clock
}

// The ordinary cancel: the run acknowledges, writes its own cancelled row
// (status and cascade in one commit) and closes. It is cancelled, not
// terminated, its row is not written over, and Terminate returns as soon as
// the execution closes, not at the end of the settle budget.
func TestTerminate_RunThatEndsWritesItsRowAndClosesIsCancelledNotTerminated(t *testing.T) {
	repo, temporal, svc, clock := cancelFixture(t)
	temporal.endingAfter = 1
	temporal.onDescribe = func(n int) {
		// Describe 1 is Terminate's own liveness read; the run commits its
		// row and closes two settle polls later.
		if n == 3 {
			repo.workflow.Status = db.Cancelled()
			temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
		}
	}

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated, "a run that is ending on its own must not be terminated: that cuts off its cleanup")
	assert.Empty(t, repo.casCalls, "the run wrote its own cancelled status")
	assert.Empty(t, repo.updatedStatuses, "and Terminate did not write over it")
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason, "the cascade is re-asserted at the row's own reason")
	assert.Less(t, clock.waited(), testSettle, "settled when the execution closed, not when the budget ran out")
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints, "the next message must still start a fresh run")
	assert.Equal(t, []string{"chat-1|"}, repo.clearedUsageLimit)
}

// A terminal row is not settlement: the run writes its row before it stops
// executing. A completion that raced the cancel commits `completed` while it
// still runs; Terminate keeps waiting for the execution to close, and then
// lets the completion stand — the cancel never overwrites how the run ended —
// with its cascade re-asserted at the completed reason.
func TestTerminate_CompletionThatRacedTheCancelStandsAndIsWaitedOut(t *testing.T) {
	repo, temporal, svc, _ := cancelFixture(t)
	temporal.endingAfter = 1
	// The completion's terminal write lands before the cancel is even
	// acknowledged; the execution keeps running its bookkeeping until
	// Describe 6.
	repo.workflow.Status = db.Completed()
	temporal.onDescribe = func(n int) {
		if n == 6 {
			temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
		}
	}

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.GreaterOrEqual(t, temporal.describes, 6, "a terminal row while the execution is open is not settled: Terminate waits for the close")
	assert.Empty(t, temporal.terminated)
	assert.Empty(t, repo.casCalls, "the completion is the run's verdict")
	assert.Empty(t, repo.updatedStatuses, "and the cancel does not overwrite it")
	assert.Equal(t, db.StopReasonCompleted, repo.cascadedReason, "its cascade is re-asserted at its own reason, not as a cancel")
	assert.Equal(t, db.StopReasonCompleted, repo.cascadedThread)
}

// A terminal row whose execution never closes within the budget is still
// executing: it is not settled, it is not terminated, and nothing is written
// over it. Terminate gives it the whole settle budget, then leaves it.
func TestTerminate_TerminalRowWhileTheExecutionStaysOpenIsNotSettled(t *testing.T) {
	repo, temporal, svc, clock := cancelFixture(t)
	temporal.endingAfter = 1
	repo.workflow.Status = db.Completed()

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.GreaterOrEqual(t, clock.waited(), testSettle, "it waited the settle budget out rather than taking the row's word")
	assert.Empty(t, temporal.terminated)
	assert.Empty(t, repo.updatedStatuses)
	assert.Zero(t, repo.cascadedReason, "nothing is reconciled for a run still executing")
}

// The review's case: the run says it is ending — the query is answered the
// moment its completion defer starts — and then its execution CLOSES without
// its row having been written (it completed concurrently with the cancel, or
// its status write failed). Terminate must not return as though the database
// agreed: it reconciles the row, as it does for a run it found closed.
func TestTerminate_RunThatAcknowledgesThenClosesWithItsRowLiveIsReconciled(t *testing.T) {
	repo, temporal, svc, _ := cancelFixture(t)
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
	repo, temporal, svc, clock := cancelFixture(t)
	temporal.endingAfter = 1

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Empty(t, temporal.terminated)
	assert.Empty(t, repo.casCalls, "the run, still executing, owns its terminal write")
	assert.Empty(t, repo.updatedStatuses, "and nothing writes over its row")
	assert.GreaterOrEqual(t, clock.waited(), testSettle)
}

// A worker that does not know the query still lets Terminate see the run
// close, and the row it left is reconciled.
func TestTerminate_RunThatClosesWithoutAnsweringIsNotTerminated(t *testing.T) {
	repo, temporal, svc, _ := cancelFixture(t)
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
	repo, temporal, svc, _ := cancelFixture(t)

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Equal(t, []string{"wf-1"}, temporal.terminated)
	require.Len(t, repo.casCalls, 1)
	assert.Equal(t, db.Cancelled(), repo.casCalls[0].NewStatus)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
}

func TestTerminate_CancelRequestThatFailsFallsBackToTerminate(t *testing.T) {
	_, temporal, svc, _ := cancelFixture(t)
	temporal.cancelErr = errors.New("unavailable")

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.terminated)
	assert.Zero(t, temporal.queries, "a cancel that was never delivered is not waited on")
}

// A second cancel of a run already running its cleanup must not terminate it:
// an agent that retries control_run cancel would otherwise cut off the very
// cleanup the first cancel started.
func TestTerminate_SecondCancelDuringCleanupDoesNotTerminate(t *testing.T) {
	_, temporal, svc, _ := cancelFixture(t)
	temporal.endingAfter = 1

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))
	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1", "wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
}

// A run Temporal already reports closed is reconciled as before; nothing is
// cancelled.
func TestTerminate_ClosedRunIsNotCancelled(t *testing.T) {
	_, temporal, svc, _ := cancelFixture(t)
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Empty(t, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
}
