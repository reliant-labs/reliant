package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A trigger that names no workflow runs its owner's default workflow
// (`reliant trigger create` without --workflow). The firer read the workflow's
// concurrency policy by resolving the empty name, which fails, and returned
// that error to Temporal: the fire retried until its attempts ran out and
// recorded nothing, so the trigger fired silently: no event, no run, LAST
// never (north-star AW-18).
func TestFireOfATriggerWithNoWorkflowLaunchesTheDefault(t *testing.T) {
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Workflow = "" })
	f.firer.WithWorkflows(LaunchWorkflows{Repo: f.repo})

	req := f.fireAt(3)
	out, err := f.firer.Fire(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome, "%+v", out)
	ev, err := f.repo.GetTriggerEventByDedupe(context.Background(), core.TriggerEventKindSchedule, req.FireWorkflowID)
	require.NoError(t, err, "the fire is recorded")
	assert.Equal(t, core.TriggerEventLaunched, ev.Outcome)
}

// A fire whose workflow cannot be resolved is recorded failed, never retried
// out with no trace.
func TestFireOfATriggerWhoseWorkflowIsMissingIsRecorded(t *testing.T) {
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Workflow = "no-such-workflow" })
	f.firer.WithWorkflows(LaunchWorkflows{Repo: f.repo})

	req := f.fireAt(4)
	_, _ = f.firer.Fire(context.Background(), req)
	ev, err := f.repo.GetTriggerEventByDedupe(context.Background(), core.TriggerEventKindSchedule, req.FireWorkflowID)
	require.NoError(t, err, "the fire is recorded")
	assert.Equal(t, core.TriggerEventFailed, ev.Outcome)
	assert.Contains(t, ev.OutcomeDetail, "no-such-workflow")
}
