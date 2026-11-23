package triggers

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/store"

	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/threads"
)

func policyCapacityFixture(t *testing.T, policy *reliantv1.WorkflowConcurrency) (*fireFixture, *describedStarter, *recordingStarter) {
	f := newFireFixture(t, nil)
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	launcher := launch.New(launch.Deps{Store: f.repo, Projects: projectaccess.NewFromORM(f.repo.ORM()), Selections: store.NewChatSelectionExtStore(f.repo.ORM()), Threads: threads.New(threads.Deps{Repo: f.repo, Chats: chataccess.NewFromORM(f.repo.ORM()), Messages: store.NewMessageExtStore(f.repo.ORM()), Senders: store.NewThreadExtStore(f.repo.ORM())}), Temporal: starter, Runs: noopRunRecorder{}, TaskQueue: "test-queue"})
	wake := &recordingStarter{}
	f.firer = NewFirer(NewStores(f.repo), launcher).WithWorkflows(capacityResolver{wf: &reliantv1.Workflow{Concurrency: policy}}).
		WithCapacityDescriptions(starter).WithCapacityWake(wake, "test-queue")
	return f, starter, wake
}

func (s *describedStarter) complete(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.descriptions[chatID].WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
}

// concurrency.priority is evaluated per firing and fixed on its event; a
// freed slot goes to the highest-priority waiting firing, not the oldest. A
// fire of a lower-priority event (the redriver's, or a retry) that finds a
// higher one ahead declines and hands that one its fire at once.
func TestCapacityPriorityAdmitsHighestWaitingFiringFirst(t *testing.T) {
	f, starter, wake := policyCapacityFixture(t, &reliantv1.WorkflowConcurrency{
		MaxRunning: 1, Priority: "trigger.payload.scheduled_for.contains('-05T') ? 10 : 0",
	})
	ctx := context.Background()

	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	for _, day := range []int{3, 4, 5} {
		out, err := f.firer.Fire(ctx, f.fireAt(day))
		require.NoError(t, err)
		require.Equal(t, string(core.TriggerEventPending), out.Outcome)
	}
	require.Equal(t, int64(0), *f.eventOf(t, 3).CapacityPriority)
	urgent := f.eventOf(t, 5)
	require.NotNil(t, urgent.CapacityPriority)
	require.Equal(t, int64(10), *urgent.CapacityPriority)

	starter.complete(first.ChatID)
	before := len(wake.snapshot())
	older, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), older.Outcome, "an older, lower-priority firing waits behind a higher one")
	starts := wake.snapshot()[before:]
	require.Len(t, starts, 1, "the declined fire hands its turn to the firing the scope admits next")
	require.True(t, strings.HasPrefix(starts[0].ID, "capacity-redrive:"+urgent.ID+":"), "woke %q", starts[0].ID)

	out, err := f.firer.Fire(ctx, f.fireAt(5))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	require.Equal(t, core.TriggerEventPending, f.eventOf(t, 3).Outcome)
}

// max_running_per_key caps the running runs of one key, and a firing whose
// key is at its cap does not hold back a firing of another key behind it.
// Under FIFO the key supersedes nothing.
func TestCapacityPerKeyCapHoldsOnlyItsOwnKey(t *testing.T) {
	f, starter, _ := policyCapacityFixture(t, &reliantv1.WorkflowConcurrency{
		MaxRunning: 3, MaxRunningPerKey: 1,
		Key: "{{ trigger.payload.scheduled_for.contains('-04T') ? 'b' : 'a' }}",
	})
	ctx := context.Background()

	first, err := f.firer.Fire(ctx, f.fireAt(2)) // key a
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	held, err := f.firer.Fire(ctx, f.fireAt(3)) // key a: at its cap
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventPending), held.Outcome, "one running run per key")
	other, err := f.firer.Fire(ctx, f.fireAt(4)) // key b
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), other.Outcome, "another key is not held behind a capped one")

	waiting := f.eventOf(t, 3)
	require.Nil(t, waiting.CapacityKey, "FIFO: the key supersedes nothing")
	require.NotNil(t, waiting.CapacityRunningKey)
	require.Equal(t, *f.eventOf(t, 2).CapacityRunningKey, *waiting.CapacityRunningKey)
	require.NotEqual(t, *f.eventOf(t, 4).CapacityRunningKey, *waiting.CapacityRunningKey)

	starter.complete(first.ChatID)
	again, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), again.Outcome, "the key's run finished, so its waiting firing runs")
}
