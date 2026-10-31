package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

type pinRecordingStarter struct {
	flakyStarter
	inputs []runtime.WorkflowInput
}

func (s *pinRecordingStarter) ExecuteWorkflow(ctx context.Context, opts client.StartWorkflowOptions, wf interface{}, args ...interface{}) (client.WorkflowRun, error) {
	if in, ok := args[0].(runtime.WorkflowInput); ok {
		s.inputs = append(s.inputs, in)
	}
	return s.flakyStarter.ExecuteWorkflow(ctx, opts, wf, args...)
}

// Drive both firers through the real launcher and database: the first start
// commits admission then fails at Temporal; a broken sync must not change or
// prevent recovery of that already-admitted firing.
func TestRecordedFireRetryKeepsAdmittedCatalogAfterBrokenPush(t *testing.T) {
	for _, inbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "schedule", true: "inbound"}[inbound], func(t *testing.T) {
			p := newProvenanceProject(t)
			now := time.Now().UTC()
			require.NoError(t, p.repo.CreateWorktree(p.ctx, &db.Worktree{ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main", ProjectID: p.projectID, Status: int32(pb.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))
			starter := &pinRecordingStarter{}
			starter.setDown(true)
			launcher := launch.NewLauncher(p.repo, threads.NewService(p.repo), starter, noopRunRecorder{}, "test-q")
			key := "retry-pin-" + uuid.NewString()
			kind := core.TriggerEventKindSchedule
			fire := func() (*FireOutput, error) {
				return NewFirer(p.repo, launcher).WithWorkflows(p.workflows()).Fire(p.ctx, FireRequest{TriggerID: p.schedule.ID, FireWorkflowID: key, ScheduledAt: now})
			}
			if inbound {
				kind = core.TriggerEventKindWebhook
				created, err := p.repo.CreateTriggerEvent(p.ctx, &core.TriggerEvent{ID: uuid.NewString(), UserID: p.userID, TriggerID: &p.webhook.ID, Kind: kind, DedupeKey: key, OccurredAt: now, CreatedAt: now, Payload: map[string]any{}, Outcome: core.TriggerEventPending})
				require.NoError(t, err)
				require.True(t, created)
				fire = func() (*FireOutput, error) {
					return NewEventFirer(p.repo, launcher).WithWorkflows(p.workflows()).Fire(p.ctx, EventFireInput{TriggerID: p.webhook.ID, Kind: kind, DedupeKey: key, Manual: true})
				}
			}
			_, err := fire()
			require.Error(t, err)
			recorded, err := p.repo.GetTriggerEventByDedupe(p.ctx, kind, key)
			require.NoError(t, err)
			require.Equal(t, core.TriggerEventLaunched, recorded.Outcome)
			require.NotEmpty(t, recorded.Definition.CatalogPin)
			original := recorded.Definition.CatalogPin
			p.push(t, 2000, "name: release\nentry: [broken\n")
			starter.setDown(false)
			out, err := fire()
			require.NoError(t, err)
			require.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
			require.GreaterOrEqual(t, len(starter.inputs), 2)
			require.Equal(t, original, starter.inputs[len(starter.inputs)-1].ExecContext.CatalogPin)
			after, err := p.repo.GetTriggerEventByDedupe(p.ctx, kind, key)
			require.NoError(t, err)
			require.Equal(t, recorded.Definition, after.Definition)
		})
	}
}
