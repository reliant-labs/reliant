// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A list describes each workflow once, however many of its activations it
// renders: a description resolves, and parses, the workflow's whole YAML. It
// reads through the store it is given, so the count of catalog reads under a
// describer with no request memo is the count of descriptions.
func TestWorkflowDescriberDescribesEachWorkflowOnce(t *testing.T) {
	env := setupTriggerTest(t)
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
		{Slug: "triage", Name: "triage", RelativePath: ".reliant/workflows/triage.yaml", YAMLContent: declaringWorkflowYAML},
	})
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, env.repo, env.projectID, env.daemonID)
	_, err = env.repo.SyncProjectConfigSnapshot(env.ctx, &db.ProjectConfigRecord{
		ProjectID: env.projectID, DaemonID: env.daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true, nil)
	require.NoError(t, err)

	counting := &countingRepo{Repo: env.repo}
	sources := newWorkflowDescriber(counting)
	activation := func(id, workflow string) *core.Trigger {
		return &core.Trigger{ID: id, UserID: env.userID, ProjectID: env.projectID, DaemonID: env.daemonID, Workflow: workflow}
	}
	first := definitionSource(env.ctx, sources, activation("a", "triage"))
	require.NotNil(t, first)
	require.Equal(t, "triage.yaml", first.GetPath())
	for _, id := range []string{"b", "c"} {
		require.Equal(t, first.GetPath(), definitionSource(env.ctx, sources, activation(id, "triage")).GetPath())
	}
	require.EqualValues(t, 1, counting.catalogs.Load(), "three activations of one workflow are described once")

	require.Nil(t, definitionSource(env.ctx, sources, activation("d", "missing")), "a workflow that does not resolve has no source")
	require.EqualValues(t, 2, counting.catalogs.Load(), "another workflow is described on its own")
}
