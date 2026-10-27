package triggers

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enums "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
)

type capacityResolver struct{ wf *reliantv1.Workflow }

func (r capacityResolver) ResolveRunWorkflow(context.Context, string, string, string) (*reliantv1.Workflow, error) {
	return r.wf, nil
}

type describedStarter struct {
	*flakyStarter
	mu           sync.Mutex
	descriptions map[string]*workflowservice.DescribeWorkflowExecutionResponse
}

func (s *describedStarter) ExecuteWorkflow(ctx context.Context, o client.StartWorkflowOptions, w interface{}, args ...interface{}) (client.WorkflowRun, error) {
	run, err := s.flakyStarter.ExecuteWorkflow(ctx, o, w, args...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.descriptions[o.ID]; !ok {
		memo, err := converter.GetDefaultDataConverter().ToPayload(o.Memo["trigger_event_id"])
		if err != nil {
			return nil, err
		}
		s.descriptions[o.ID] = &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: o.ID, RunId: run.GetRunID()}, FirstRunId: run.GetRunID(), Status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING, Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{"trigger_event_id": memo}}}}
	}
	return run, nil
}
func (s *describedStarter) DescribeWorkflowExecution(_ context.Context, id, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.descriptions[id], nil
}

// Exercises the real Firer, Launcher transaction and PostgreSQL FIFO; only the
// external Temporal transport is deterministic. No chat status releases a slot.
func TestCapacityFirerLaunchFIFOAndTemporalRelease(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), starter, noopRunRecorder{}, "test-queue")
	resolver := capacityResolver{wf: &reliantv1.Workflow{Concurrency: &reliantv1.WorkflowConcurrency{MaxRunning: 1}}}
	f.firer = NewFirer(f.repo, launcher).WithWorkflows(resolver).WithCapacityDescriptions(starter)
	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	second, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), second.Outcome)
	third, err := f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), third.Outcome)
	// A best-effort UI terminal stamp does not free a still-running Temporal chain.
	_, err = f.repo.ORM().Bun().NewRaw(`UPDATE trigger_events SET run_status = 'completed' WHERE id = ?`, first.EventID).Exec(ctx)
	require.NoError(t, err)
	second, err = f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), second.Outcome)
	starter.mu.Lock()
	starter.descriptions[first.ChatID].WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	starter.mu.Unlock()
	// A later event cannot jump the older queued event even after capacity frees.
	third, err = f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), third.Outcome)
	second, err = f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), second.Outcome)
	third, err = f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), third.Outcome)
}
