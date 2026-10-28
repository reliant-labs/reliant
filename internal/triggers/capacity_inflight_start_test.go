package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// absentStarter answers Describe as Temporal does for an execution it has not
// accepted: NotFound, not an empty description.
type absentStarter struct{ *describedStarter }

func (s *absentStarter) DescribeWorkflowExecution(ctx context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	desc, err := s.describedStarter.DescribeWorkflowExecution(ctx, id, run)
	if err == nil && desc == nil {
		return nil, serviceerror.NewNotFound("workflow execution not found")
	}
	return desc, err
}

// A root leaves PENDING without waiting for a start already past its claim:
// runs.Service.Terminate finds no execution yet and its reconcileStoppedRun
// writes CANCELLED unconditionally. That must not let capacity recovery
// release the slot while the start can still be accepted, or the accepted run
// executes with no slot counted (review B4-a). Deleting the chat is the other
// path the review names; the root row survives it (workflows has no foreign
// key to chats), so it is pinned here as a guard.
func TestCapacityInFlightStartHoldsSlotWhenRootLeavesPending(t *testing.T) {
	for _, how := range []string{"terminated", "chat-deleted"} {
		t.Run(how, func(t *testing.T) {
			f := newFireFixture(t, nil)
			ctx := context.Background()
			starter := &absentStarter{&describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}}
			launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), starter, noopRunRecorder{}, "test-queue")
			resolver := capacityResolver{wf: &reliantv1.Workflow{Concurrency: &reliantv1.WorkflowConcurrency{MaxRunning: 1}}}
			f.firer = NewFirer(f.repo, launcher).WithWorkflows(resolver).WithCapacityDescriptions(starter)
			scope := store.TriggerCapacityScope{UserID: f.trigger.UserID, ProjectID: f.trigger.ProjectID, WorkflowRef: workflowref.RefKey(f.trigger.Workflow)}

			done := make(chan struct{})
			admit := f.starter.holdNextStart(t, func() {
				defer close(done)
				_, _ = f.firer.Fire(ctx, f.fireAt(2))
			})
			occupied, err := f.repo.ListTriggerCapacityExecutions(ctx, scope)
			require.NoError(t, err)
			require.Len(t, occupied, 1)
			held := occupied[0]
			require.NotEmpty(t, held.WorkflowID)
			require.Empty(t, held.FirstRunID, "the start is in flight, so nothing is bound yet")

			switch how {
			case "terminated":
				require.NoError(t, f.repo.UpdateWorkflowStatus(ctx, held.WorkflowID, core.Cancelled()))
			case "chat-deleted":
				require.NoError(t, f.repo.DeleteChat(ctx, f.chatOfFire(t, f.fireAt(2))))
			}

			// The fresh database attempt must hold before any external Describe can authorize release.
			require.NoError(t, reconcileCapacityExecutions(ctx, f.repo, starter, occupied))
			after, err := f.repo.ListTriggerCapacityExecutions(ctx, scope)
			require.NoError(t, err)
			require.Len(t, after, 1, "an in-flight start holds its slot however its root left PENDING")

			// Temporal now accepts the held start; the run it admitted must be counted.
			admit()
			<-done
			bound, err := f.repo.ListTriggerCapacityExecutions(ctx, scope)
			require.NoError(t, err)
			require.Len(t, bound, 1)
			require.NotEmpty(t, bound[0].FirstRunID, "the accepted start is bound to its slot")
		})
	}
}
