// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// machineCatalogCountingRepo is the real store, counting reads of a
// machine's synced workflow catalog.
type machineCatalogCountingRepo struct {
	*db.Repo
	machineReads atomic.Int64
}

func (r *machineCatalogCountingRepo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	r.machineReads.Add(1)
	return r.Repo.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
}

// The reconciler checks every activation's projection every minute. Its
// per-pass cache memoized each workflow, but not the reads under it: each
// distinct workflow re-read its machine's whole catalog row, hundreds of KB.
// A pass over five workflows of one machine read that row five times; now
// once, however many workflows the machine's activations run.
func TestReconcilePassReadsEachMachineCatalogOnce(t *testing.T) {
	p := newProvenanceProject(t)
	const workflows = 4
	stored := map[string]string{}
	for i := range workflows {
		slug := fmt.Sprintf("release-%d", i)
		stored[slug] = strings.Replace(provenanceWorkflow, "name: release\n", "name: "+slug+"\n", 1)
	}
	p.pushCatalog(t, 2000, stored)
	nightly := "nightly"
	cfg, err := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	require.NoError(t, err)
	now := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	for i := range workflows {
		require.NoError(t, p.repo.CreateTrigger(p.ctx, &core.Trigger{
			ID: uuid.NewString(), UserID: p.userID, ProjectID: p.projectID, Name: fmt.Sprintf("nightly %d", i),
			Kind: core.TriggerKindSchedule, Enabled: true, Workflow: fmt.Sprintf("release-%d", i), WorkflowTrigger: &nightly,
			DaemonID: p.daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	repo := &machineCatalogCountingRepo{Repo: p.repo}
	report, err := NewReconciler(NewStores(p.repo), LaunchWorkflows{Repo: repo}, nil).ReconcileAll(p.ctx)
	require.NoError(t, err)
	// The fixture's own "release" activation, which this catalog no longer
	// carries, is checked too (and found broken).
	require.Equal(t, workflows+1, report.Checked)
	require.Equal(t, int64(1), repo.machineReads.Load(), "one read of the machine's catalog for the whole pass")
}
