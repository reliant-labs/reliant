// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// switchableStarter stands in for Temporal's start: down refuses root starts.
type switchableStarter struct {
	mu     sync.Mutex
	down   bool
	starts int
}

func (s *switchableStarter) setDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func (s *switchableStarter) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot {
		if s.down {
			return nil, errors.New("temporal unavailable")
		}
		s.starts++
	}
	return &startedRun{id: options.ID}, nil
}

type startedRun struct {
	client.WorkflowRun
	id string
}

func (r *startedRun) GetID() string    { return r.id }
func (r *startedRun) GetRunID() string { return "run-" + r.id }

type discardRunRecorder struct{}

func (discardRunRecorder) RecordRun(context.Context, string, string, string) {}

// A fire whose launch committed but whose starter died before it reached
// Temporal — and so before it could record the failure — leaves its root
// PENDING with no failed start, which reads as a start in flight: the
// trigger's next fires skip behind it. The reconciler is what ends that: once
// the start has been missing from Temporal for its bound, it records the
// failure, and the next fire launches.
func TestReconcilerRecoversAFireStuckPending(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a database; skipped under -short")
	}
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	userID := "stuck-user-" + uuid.NewString()
	projectID := "stuck-project-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Stuck Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	config, err := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}, Timezone: "UTC"})
	require.NoError(t, err)
	trigger := &core.Trigger{
		ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Name: "nightly audit",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Params:   map[string]any{"model": map[string]any{"id": "mock"}},
		Message:  "Audit the dependency tree.",
		DaemonID: daemonID, Config: config, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateTrigger(ctx, trigger))

	starter := &switchableStarter{}
	firer := triggers.NewFirer(repo, launch.NewLauncher(repo, threads.NewService(repo), starter, discardRunRecorder{}, "test-queue"))
	fireAt := func(day int) triggers.FireRequest {
		scheduled := time.Date(2026, 1, day, 9, 0, 0, 0, time.UTC)
		return triggers.FireRequest{
			TriggerID:      trigger.ID,
			FireWorkflowID: triggers.FireWorkflowID(trigger.ID) + "-" + scheduled.Format(time.RFC3339),
			ScheduledAt:    scheduled,
		}
	}

	// The stuck fire: committed, never started, and no failure recorded. A
	// failed start with the record cleared again is exactly that row.
	starter.setDown(true)
	stuck := fireAt(2)
	_, err = firer.Fire(ctx, stuck)
	require.Error(t, err)
	starter.setDown(false)
	ev, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, stuck.FireWorkflowID)
	require.NoError(t, err)
	require.NotNil(t, ev.ChatID)
	stuckChat := *ev.ChatID
	stillPending, err := repo.ClaimWorkflowStart(ctx, stuckChat)
	require.NoError(t, err)
	require.True(t, stillPending)

	skipped, err := firer.Fire(ctx, fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventSkipped), skipped.Outcome,
		"inside the bound the stuck fire reads as a start in flight: %+v", skipped)

	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	// Inside the bound the reconciler leaves it alone: it may yet start.
	_, _ = reconciler.ReconcileRunningWorkflows(ctx)
	roots, err := repo.ListRootWorkflows(ctx, stuckChat)
	require.NoError(t, err)
	require.Nil(t, roots[0].StartFailedAt, "a launch younger than the bound must not be judged stranded")

	// Past the bound, with Temporal holding no run for it, it is stranded.
	backdate := time.Now().Add(-2 * strandedStartBound)
	_, err = raw.ExecContext(ctx, `UPDATE workflows SET created_at = $1 WHERE chat_id = $2`, backdate, stuckChat)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `UPDATE trigger_events SET created_at = $1 WHERE chat_id = $2`, backdate, stuckChat)
	require.NoError(t, err)
	_, _ = reconciler.ReconcileRunningWorkflows(ctx)
	roots, err = repo.ListRootWorkflows(ctx, stuckChat)
	require.NoError(t, err)
	require.Equal(t, core.Pending(), roots[0].Status)
	require.NotNil(t, roots[0].StartFailedAt, "the reconciler must record the stuck start as failed")

	recovered, err := firer.Fire(ctx, fireAt(4))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), recovered.Outcome, "%+v", recovered)
	assert.Equal(t, 1, starter.starts)
	roots, err = repo.ListRootWorkflows(ctx, stuckChat)
	require.NoError(t, err)
	assert.Equal(t, core.Failed(), roots[0].Status, "the fire that launched must abandon the stuck one")
}
