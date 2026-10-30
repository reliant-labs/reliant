package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

func latestCapacityFixture(t *testing.T, key string) (*fireFixture, *describedStarter) {
	f := newFireFixture(t, nil)
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), starter, noopRunRecorder{}, "test-queue")
	resolver := capacityResolver{wf: &reliantv1.Workflow{Concurrency: &reliantv1.WorkflowConcurrency{
		MaxRunning: 1, Waiting: reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST, Key: key,
	}}}
	f.firer = NewFirer(f.repo, launcher).WithWorkflows(resolver).WithCapacityDescriptions(starter)
	return f, starter
}

func (f *fireFixture) eventOf(t *testing.T, day int) *core.TriggerEvent {
	t.Helper()
	ev, err := f.repo.GetTriggerEventByDedupe(context.Background(), core.TriggerEventKindSchedule, f.fireAt(day).FireWorkflowID)
	require.NoError(t, err)
	return ev
}

// waiting: latest keeps only the newest waiting firing of a key: the older
// one settles skipped as superseded (not failed), the running run is never
// touched, and the survivor launches when the slot frees.
func TestCapacityWaitingLatestSupersedesOlderWaitingFiring(t *testing.T) {
	f, starter := latestCapacityFixture(t, "{{ trigger.payload.trigger_name }}")
	ctx := context.Background()

	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	second, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), second.Outcome)
	third, err := f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), third.Outcome)

	superseded := f.eventOf(t, 3)
	require.Equal(t, core.TriggerEventSkipped, superseded.Outcome)
	require.Equal(t, store.TriggerCapacitySuperseded, superseded.OutcomeDetail)
	require.Equal(t, core.TriggerEventLaunched, f.eventOf(t, 2).Outcome, "a running run is never superseded")

	// A redelivered fire of the superseded event reports its record; it
	// neither launches nor re-enters the queue.
	again, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventSkipped), again.Outcome)

	_, err = f.repo.ORM().Bun().NewRaw(`UPDATE trigger_events SET capacity_start_attempt_at=now()-interval '1 hour' WHERE id=?`, first.EventID).Exec(ctx)
	require.NoError(t, err)
	starter.mu.Lock()
	starter.descriptions[first.ChatID].WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	starter.mu.Unlock()
	third, err = f.firer.Fire(ctx, f.fireAt(4))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), third.Outcome, "the newest waiting firing is the FIFO head once the older one is superseded")
}

// The key only decides which waiting firings replace each other: firings of
// different keys all wait, and max_running still bounds the whole workflow.
func TestCapacityWaitingLatestKeepsOtherKeysWaiting(t *testing.T) {
	f, _ := latestCapacityFixture(t, "{{ trigger.payload.scheduled_for }}")
	ctx := context.Background()

	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	for _, day := range []int{3, 4} {
		out, err := f.firer.Fire(ctx, f.fireAt(day))
		require.NoError(t, err)
		require.Equal(t, string(core.TriggerEventPending), out.Outcome)
	}
	require.Equal(t, core.TriggerEventPending, f.eventOf(t, 3).Outcome)
	require.Equal(t, core.TriggerEventPending, f.eventOf(t, 4).Outcome)
}
