// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const releaseWorkflow = `name: release
triggers:
  - name: merged
    schedule: {cron: "0 9 * * *"}
    prompt: release
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// The incident: project workflow `release` lives in control-plane's
// .reliant/workflows, checked out on machine "default" only. Machine "cicd"
// syncs the project without it, after default. The activation pointed at
// cicd is BROKEN and its inbox item says where the workflow is; the one on
// default is fine — the union of both machines still has the workflow.
func TestListInbox_ActivationOnAMachineWithoutItsWorkflowIsBroken(t *testing.T) {
	f := newInboxFixture(t)
	defaultID, cicdID := uuid.NewString(), uuid.NewString()
	for id, host := range map[string]string{defaultID: "default", cicdID: "cicd"} {
		h := host
		require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: id, UserID: f.userID, Hostname: &h}))
	}
	push := func(daemonID string, at int64, workflows []config.StoredWorkflow) {
		t.Helper()
		wj, err := config.EncodeCatalogJSON(workflows)
		require.NoError(t, err)
		_, err = f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
			ProjectID: f.project(), DaemonID: daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.UnixMilli(at).UTC(),
		}, false)
		require.NoError(t, err)
	}
	const releasePath = "control-plane/.reliant/workflows/release.yaml"
	push(defaultID, 1000, []config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: releasePath, YAMLContent: releaseWorkflow}})
	push(cicdID, 2000, nil)

	activate := func(id, daemonID string) {
		t.Helper()
		name := "merged"
		cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
		now := time.Now().UTC().Truncate(time.Microsecond)
		require.NoError(t, f.repo.CreateTrigger(f.ctx, &core.Trigger{
			ID: id, UserID: f.userID, ProjectID: f.project(), Name: "Release " + id,
			Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "release", WorkflowTrigger: &name,
			DaemonID: daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	activate("on-cicd", cicdID)
	activate("on-default", defaultID)

	items := brokenItems(f.list(t, nil).Items)
	require.Len(t, items, 1, "only the activation whose machine lacks the workflow")
	assert.Equal(t, "on-cicd", items[0].TriggerId)
	detail := items[0].GetAutomationFailing().GetHealth().GetLastFailureDetail()
	assert.Contains(t, detail, `declared trigger "merged" of workflow "release"`)
	assert.Contains(t, detail, `machine "cicd" has no workflow "release"; it is defined on machine "default" at `+releasePath)

	// Checking the repository out on cicd fixes it on cicd's next push.
	push(cicdID, 3000, []config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: releasePath, YAMLContent: releaseWorkflow}})
	assert.Empty(t, brokenItems(f.list(t, nil).Items))
}
