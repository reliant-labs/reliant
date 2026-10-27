package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
)

type realCapacityRepository struct {
	*capacityBoundary
	id, first string
}

func (r *realCapacityRepository) ListTriggerCapacityExecutions(context.Context, store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error) {
	return []store.TriggerCapacityExecution{{EventID: "occupied", WorkflowID: r.id, FirstRunID: r.first}}, nil
}

// The real server decides FirstRunId and current-chain status, including CAN.
func capacityChainWorkflow(ctx workflow.Context, continued bool) error {
	if !continued {
		return workflow.NewContinueAsNewError(ctx, capacityChainWorkflow, true)
	}
	var finish bool
	workflow.GetSignalChannel(ctx, "finish").Receive(ctx, &finish)
	return nil
}

func TestCapacityRealTemporalContinuedChainRetainsSlot(t *testing.T) {
	c := temporalForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue := "capacity-" + uuid.NewString()
	id := "capacity-chain-" + uuid.NewString()
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(capacityChainWorkflow)
	require.NoError(t, w.Start())
	defer w.Stop()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: queue}, capacityChainWorkflow, false)
	require.NoError(t, err)
	initial := run.GetRunID()
	var current string
	require.Eventually(t, func() bool {
		d, e := c.DescribeWorkflowExecution(ctx, id, "")
		if e != nil {
			return false
		}
		current = d.GetWorkflowExecutionInfo().GetExecution().GetRunId()
		return current != "" && current != initial
	}, 5*time.Second, 10*time.Millisecond)
	boundary := &capacityBoundary{event: &core.TriggerEvent{ID: "waiting", Outcome: core.TriggerEventPending}}
	// Use the actual server, substituting only the database boundary in this test.
	spec := launch.Spec{}
	adapter := &realCapacityRepository{capacityBoundary: boundary, id: id, first: initial}
	require.NoError(t, prepareCapacity(ctx, adapter, c, &core.Trigger{UserID: "user", ProjectID: "project", Workflow: "ref"}, boundary.event, 1, &spec))
	require.False(t, boundary.released, "live CAN successor retains the original slot")
	require.NoError(t, c.SignalWorkflow(ctx, id, "", "finish", true))
	require.NoError(t, run.Get(ctx, nil))
	require.NoError(t, prepareCapacity(ctx, adapter, c, &core.Trigger{UserID: "user", ProjectID: "project", Workflow: "ref"}, boundary.event, 1, &spec))
	require.True(t, boundary.released, "terminal matching CAN chain releases capacity")
}
