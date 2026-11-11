// Copyright (c) 2025 Reliant Labs
package inbox

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

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// countingRepo counts the reads a workflow resolution makes.
type countingRepo struct {
	*db.Repo
	catalogs, drafts atomic.Int64
}

func (r *countingRepo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	r.catalogs.Add(1)
	return r.Repo.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
}

func (r *countingRepo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.drafts.Add(1)
	return r.Repo.GetUsableWorkflowBySlug(ctx, userID, slug)
}

func (r *countingRepo) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.drafts.Add(1)
	return r.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
}

// Prod (2026-11-05): ListInbox for a user with 22 activations read machine
// catalogs 13 times and account drafts 24 times a call, p50 516ms, and the web
// calls it more than any other slow rpc. Every activation resolved its
// workflow and its children through the repository again. One ListInbox reads
// each catalog once, and lists what it listed before.
func TestListInbox_ReadsEachCatalogOnce(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := uuid.NewString()
	host := "default"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: &host}))

	// Each workflow runs a shared child, so resolution reads beyond the root.
	child := "name: shared-step\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n"
	stored := []config.StoredWorkflow{{Slug: "shared-step", Name: "shared-step", RelativePath: ".reliant/workflows/shared-step.yaml", YAMLContent: child}}
	const workflows = 3
	for i := 0; i < workflows; i++ {
		slug := fmt.Sprintf("release-%d", i)
		yaml := strings.Replace(releaseWorkflow, "name: release\n", "name: "+slug+"\n", 1)
		yaml = strings.Replace(yaml, "    type: approval\n    args: {title: ok}\n", "    type: workflow\n    ref: shared-step\n", 1)
		stored = append(stored, config.StoredWorkflow{Slug: slug, Name: slug, RelativePath: ".reliant/workflows/" + slug + ".yaml", YAMLContent: yaml})
	}
	wj, err := config.EncodeCatalogJSON(stored)
	require.NoError(t, err)
	_, err = f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
		ProjectID: f.project(), DaemonID: daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, false)
	require.NoError(t, err)

	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < workflows; i++ {
		for j := 0; j < 2; j++ {
			name := "merged"
			require.NoError(t, f.repo.CreateTrigger(f.ctx, &core.Trigger{
				ID: fmt.Sprintf("act-%d-%d", i, j), UserID: f.userID, ProjectID: f.project(), Name: fmt.Sprintf("Release %d/%d", i, j),
				Kind: core.TriggerKindSchedule, Enabled: true, Workflow: fmt.Sprintf("release-%d", i), WorkflowTrigger: &name,
				DaemonID: daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
				CreatedAt: now, UpdatedAt: now,
			}))
		}
	}
	direct := f.list(t, nil)

	counting := &countingRepo{Repo: f.repo}
	f.inbox = NewTestInbox(t, WithInboxDeps(Deps{Repo: counting}))
	got := f.list(t, nil)
	t.Logf("reads: catalogs=%d drafts=%d", counting.catalogs.Load(), counting.drafts.Load())
	require.EqualValues(t, 1, counting.catalogs.Load(),
		"every activation resolves against the one machine catalog; one ListInbox must read it once")
	require.LessOrEqual(t, counting.drafts.Load(), int64(2*(workflows+1)),
		"each account-draft lookup (one per workflow name and method) is read once")
	require.Equal(t, len(direct.Items), len(got.Items), "reading once changes nothing about what is listed")
}
