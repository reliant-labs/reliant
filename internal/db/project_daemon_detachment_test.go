package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/store"
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

	require.NoError(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil))

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

// The detach's guard runs inside its transaction, after the catalog lock: a
// detach queued behind another judges its preconditions against what the
// first one committed, not against what it read before the first began.
func TestDetachProjectFromDaemon_GuardRunsUnderTheCatalogLock(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	for _, id := range []string{p.defaultID, p.cicdID} {
		require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, id, "/work/"+id, nil))
	}

	locked, release, held := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		held <- p.repo.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
			if err := store.LockProjectCatalog(txCtx, p.repo.ORM(), p.projectID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	guarded, detached := make(chan struct{}), make(chan error, 1)
	go func() {
		detached <- p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, func(context.Context) error {
			close(guarded)
			return nil
		})
	}()
	select {
	case <-guarded:
		t.Fatal("the guard ran while another transaction held the project's catalog lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-held)
	<-guarded
	require.NoError(t, <-detached)
}

// A guard's ErrNothingToDetach ends the detach as a success that records
// nothing; any other guard error aborts it unchanged.
func TestDetachProjectFromDaemon_GuardOutcomes(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil))

	refused := errors.New("refused")
	require.ErrorIs(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, func(context.Context) error { return refused }), refused)
	require.NoError(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, func(context.Context) error { return ErrNothingToDetach }))

	rows, err := p.repo.ListProjectDaemonsForProject(ctx, p.projectID)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "neither aborted detach removed the checkout")
	detached, err := p.repo.ListProjectDaemonDetachmentsForDaemon(ctx, p.defaultID)
	require.NoError(t, err)
	assert.Empty(t, detached)
}

// A checkout the platform finds is never recorded over a detachment, and
// finding it never ends the detachment; the user's own attach does.
func TestRecordObservedProjectDaemon_LeavesADetachedPairAlone(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil))
	require.NoError(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil))

	recorded, err := p.repo.RecordObservedProjectDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil)
	require.NoError(t, err)
	assert.False(t, recorded)
	rows, err := p.repo.ListProjectDaemonsForProject(ctx, p.projectID)
	require.NoError(t, err)
	assert.Empty(t, rows)
	detached, err := p.repo.ListProjectDaemonDetachmentsForDaemon(ctx, p.defaultID)
	require.NoError(t, err)
	assert.Len(t, detached, 1, "the detachment stands")

	recorded, err = p.repo.RecordObservedProjectDaemon(ctx, p.projectID, p.cicdID, "/work/"+p.cicdID, nil)
	require.NoError(t, err)
	assert.True(t, recorded, "another machine's checkout is recorded")
}

// A snapshot resolved before a detach committed and persisted after it is
// dropped: it does not recreate the machine's catalog or its union share.
func TestSyncProjectConfigSnapshot_DropsADetachedMachinesSnapshot(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	for _, id := range []string{p.defaultID, p.cicdID} {
		require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, id, "/work/"+id, nil))
	}
	require.NoError(t, p.repo.DetachProjectFromDaemon(ctx, p.projectID, p.defaultID, "/work/"+p.defaultID, nil))

	assert.False(t, p.push(t, p.defaultID, 3000, []config.StoredWorkflow{release}, nil), "the detached machine's snapshot is dropped")
	_, synced, err := p.repo.GetProjectMachineWorkflowsJSON(ctx, p.projectID, p.defaultID)
	require.NoError(t, err)
	assert.False(t, synced, "no catalog was recreated for the detached machine")
	assert.True(t, p.push(t, p.cicdID, 3000, nil, nil), "an attached machine's snapshot is stored")
	assert.Empty(t, p.unionWorkflows(t), "the union carries nothing of the detached machine's")
}
