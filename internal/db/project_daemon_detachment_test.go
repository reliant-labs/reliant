package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// Detaching a machine drops its checkout row and its synced catalog, and the
// project's union no longer carries what only it provided; recording the
// checkout again ends the detachment.
func TestDetachProjectFromDaemon_DropsTheMachinesRowAndCatalog(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	for _, id := range []string{p.defaultID, p.cicdID} {
		require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, id, "/work/"+id, nil))
	}
	require.True(t, p.push(t, p.defaultID, 1000, []config.StoredWorkflow{release}, nil))
	require.True(t, p.push(t, p.cicdID, 2000, nil, nil))
	require.Len(t, p.unionWorkflows(t), 1)

	require.NoError(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID))

	rows, err := p.repo.ListProjectDaemonsForProject(ctx, p.projectID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, p.cicdID, rows[0].DaemonID)
	_, synced, err := p.repo.GetProjectMachineWorkflowsJSON(ctx, p.projectID, p.defaultID)
	require.NoError(t, err)
	assert.False(t, synced, "the detached machine's catalog is gone")
	assert.Empty(t, p.unionWorkflows(t), "only the detached machine provided the workflow")

	detached, err := p.repo.ListProjectDaemonDetachmentsForDaemon(ctx, p.defaultID)
	require.NoError(t, err)
	require.Len(t, detached, 1)
	assert.Equal(t, "/work/"+p.defaultID, detached[0].Path)

	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, p.defaultID, "/elsewhere", nil))
	detached, err = p.repo.ListProjectDaemonDetachmentsForDaemon(ctx, p.defaultID)
	require.NoError(t, err)
	assert.Empty(t, detached, "recording the checkout again ends the detachment")
}
