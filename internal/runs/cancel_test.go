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
}

func (c *cancellingTemporal) CancelWorkflow(_ context.Context, workflowID, _ string) error {
	c.cancelled = append(c.cancelled, workflowID)
	return c.cancelErr
}

func (c *cancellingTemporal) QueryWorkflow(_ context.Context, _, _, queryType string, _ ...interface{}) (converter.EncodedValue, error) {
	c.queries++
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
	svc.cancelGrace, svc.cancelPoll = 50*time.Millisecond, time.Millisecond
	return repo, c, svc
}

// A run that answers it is ending stops itself: it is cancelled, not
// terminated, and its own completion code writes its status and cascade, so
// Terminate writes neither (which would race the run's own write).
func TestTerminate_RunThatEndsIsCancelledNotTerminated(t *testing.T) {
	repo, temporal, svc := cancelFixture(t)
	temporal.endingAfter = 2

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated, "a run that is ending on its own must not be terminated: that cuts off its cleanup")
	assert.Empty(t, repo.casCalls, "the run writes its own cancelled status")
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints, "the next message must still start a fresh run")
	assert.Equal(t, []string{"chat-1|"}, repo.clearedUsageLimit)
}

// A worker that does not know the query still lets Terminate see the run
// close.
func TestTerminate_RunThatClosesWithoutAnsweringIsNotTerminated(t *testing.T) {
	_, temporal, svc := cancelFixture(t)
	temporal.queryErr = errors.New("unknown queryType get_run_stop")
	temporal.closeOnQuery = 2

	require.NoError(t, svc.Terminate(context.Background(), "chat-1"))

	assert.Equal(t, []string{"wf-1"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
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
