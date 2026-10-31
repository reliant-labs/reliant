package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

// When slots free, a capped launch hands the scope's next waiting event its
// fire at once. Before, a waiting event was fired again only by the redriver,
// one queue head per scope per pass: freed slots filled one a minute, so a
// queue of fast runs (prod's pr-review, 2026-10-30) drained slower than events
// arrived and every new event waited behind a backlog that never shrank.
func TestCapacityLaunchWakesTheNextWaitingEvent(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), starter, noopRunRecorder{}, "test-queue")
	resolver := capacityResolver{wf: &reliantv1.Workflow{Concurrency: &reliantv1.WorkflowConcurrency{MaxRunning: 2}}}
	wake := &recordingStarter{}
	f.firer = NewFirer(f.repo, launcher).WithWorkflows(resolver).WithCapacityDescriptions(starter).WithCapacityWake(wake, "test-queue")

	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	second, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), second.Outcome)
	for _, day := range []int{4, 5} {
		out, err := f.firer.Fire(ctx, f.fireAt(day))
		require.NoError(t, err)
		require.Equal(t, string(core.TriggerEventPending), out.Outcome)
	}
	require.Empty(t, wake.snapshot(), "nothing waited when the first two launched")

	// Both runs finish: two slots are free and two events wait.
	starter.mu.Lock()
	starter.descriptions[first.ChatID].WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	starter.descriptions[second.ChatID].WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	starter.mu.Unlock()

	third, err := f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), third.Outcome)

	next := f.eventOf(t, 5)
	require.Equal(t, core.TriggerEventPending, next.Outcome)
	starts := wake.snapshot()
	require.Len(t, starts, 1, "the launch fires the next waiting event rather than leaving the free slot to the redriver")
	require.True(t, strings.HasPrefix(starts[0].ID, "capacity-redrive:"+next.ID+":"), "woke %q", starts[0].ID)
	request, ok := starts[0].Input.(FireRequest)
	require.True(t, ok)
	require.Equal(t, f.fireAt(5).FireWorkflowID, request.FireWorkflowID, "the woken fire is the waiting event's own identity")

	// The woken fire takes the second free slot, and with the queue now
	// empty wakes nothing further.
	fourth, err := f.firer.Fire(ctx, request)
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), fourth.Outcome)
	require.Len(t, wake.snapshot(), 1)
}

type orderedCapacityRepo struct {
	capacityRedriveRepo
	capacityRepository
	calls []string
}

func (r *orderedCapacityRepo) ListStalePendingTriggerEvents(ctx context.Context, before time.Time, limit int) ([]*core.TriggerEvent, error) {
	r.calls = append(r.calls, "list-waiting")
	return r.capacityRedriveRepo.ListStalePendingTriggerEvents(ctx, before, limit)
}

func (r *orderedCapacityRepo) ListTriggerCapacityExecutionBatch(context.Context, int) ([]store.TriggerCapacityExecution, error) {
	r.calls = append(r.calls, "reconcile-occupied")
	return nil, nil
}

func (r *orderedCapacityRepo) GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, key string) (*core.TriggerEvent, error) {
	return r.capacityRedriveRepo.GetTriggerEventByDedupe(ctx, kind, key)
}

type describingRecorder struct{ *recordingStarter }

func (describingRecorder) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{}, nil
}

// A pass reconciles occupied slots before it reads the queue heads, so a slot
// whose run finished is handed out by the same pass, not a minute later.
func TestRedriveReconcilesCapacityBeforeFiringWaitingHeads(t *testing.T) {
	repo := &orderedCapacityRepo{}
	redriver := NewRedriver(repo, describingRecorder{&recordingStarter{}}, "")
	require.NoError(t, redriver.RedriveOnce(context.Background()))
	require.Equal(t, []string{"reconcile-occupied", "list-waiting"}, repo.calls)
}
