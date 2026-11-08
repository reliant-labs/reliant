package tools_daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// detachedFixture is a user's project created at its path on "cloud", whose
// checkout on "laptop" is at another path and was then detached.
type detachedFixture struct {
	repo          *db.Repo
	ctx           context.Context
	userID        string
	cloud, laptop string
	project       *db.Project
	localPath     string
}

func newDetachedFixture(t *testing.T) *detachedFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := &detachedFixture{
		repo:      repo,
		ctx:       context.Background(),
		userID:    "detached-user-" + uuid.NewString(),
		cloud:     "cloud-" + uuid.NewString(),
		laptop:    "laptop-" + uuid.NewString(),
		localPath: "/Users/me/props-" + uuid.NewString(),
	}
	for _, id := range []string{f.cloud, f.laptop} {
		require.NoError(t, repo.UpsertDaemon(f.ctx, &db.Daemon{ID: id, UserID: f.userID}))
	}
	now := time.Now().UTC()
	f.project = &db.Project{ID: uuid.NewString(), UserID: f.userID, Name: "props",
		Path: "/home/workspace/props-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(f.ctx, f.project))
	require.NoError(t, repo.UpsertProjectDaemon(f.ctx, f.project.ID, f.cloud, f.project.Path, nil))
	return f
}

// A machine's snapshot of its own checkout at a path other than the
// project's creation path is that project's, not a new one.
func TestEnsureOwnedProjectForPath_ResolvesTheMachinesOwnCheckoutPath(t *testing.T) {
	f := newDetachedFixture(t)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.project.ID, f.laptop, f.localPath, nil))
	svc := newTestService(f.repo)

	got, err := svc.ensureOwnedProjectForPath(f.ctx, newTestConn(f.userID, f.laptop, nil), f.localPath, nil)
	require.NoError(t, err)
	assert.Equal(t, f.project.ID, got.ID)

	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: f.userID})
	require.NoError(t, err)
	assert.Len(t, projects, 1, "no duplicate project is created for the machine's checkout path")
}

// After a detach, the machine's discovery and snapshots of the folder are
// dropped — neither recorded as the project nor read as a new one — and its
// connect reconcile does not record the folder again.
func TestDetachedProject_StaysOffTheMachine(t *testing.T) {
	f := newDetachedFixture(t)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.project.ID, f.laptop, f.project.Path, nil))
	require.NoError(t, f.repo.DetachProjectFromDaemon(f.ctx, f.project.ID, f.laptop, f.project.Path, nil))
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.laptop, nil)

	_, err := svc.ensureOwnedProjectForPath(f.ctx, conn, f.project.Path, nil)
	assert.True(t, errors.Is(err, errProjectDetached), "got %v", err)

	// The folder is at the creation path on the laptop: the reconcile would
	// record it, but the user detached it.
	everythingExists := func(context.Context, string) (projectPathState, error) {
		return projectPathState{Exists: true}, nil
	}
	missing, added, err := reconcileProjectDaemons(f.ctx, f.repo, f.laptop, f.userID, everythingExists)
	require.NoError(t, err)
	assert.Empty(t, added)
	assert.Empty(t, missing, "a detached project is not missing either: nothing restores it")
	rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, f.project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, f.cloud, rows[0].DaemonID)

	// Another machine is unaffected.
	_, added, err = reconcileProjectDaemons(f.ctx, f.repo, f.cloud, f.userID, everythingExists)
	require.NoError(t, err)
	assert.Empty(t, added, "the cloud machine already had its row")
}

// A detach at a path other than the creation path drops that path too: read
// by creation path alone, it would have become a new project.
func TestDetachedProject_ItsOtherPathIsNotANewProject(t *testing.T) {
	f := newDetachedFixture(t)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.project.ID, f.laptop, f.localPath, nil))
	require.NoError(t, f.repo.DetachProjectFromDaemon(f.ctx, f.project.ID, f.laptop, f.localPath, nil))
	svc := newTestService(f.repo)

	_, err := svc.ensureOwnedProjectForPath(f.ctx, newTestConn(f.userID, f.laptop, nil), f.localPath, nil)
	assert.True(t, errors.Is(err, errProjectDetached), "got %v", err)
	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: f.userID})
	require.NoError(t, err)
	assert.Len(t, projects, 1)
}

// detachDuringIntakeRepo starts a detach right after the path resolution has
// read the machine's detachments, before it reads the machine's checkout.
type detachDuringIntakeRepo struct {
	*db.Repo
	once   sync.Once
	detach func()
}

func (r *detachDuringIntakeRepo) ListProjectDaemonDetachmentsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemonDetachment, error) {
	rows, err := r.Repo.ListProjectDaemonDetachmentsForDaemon(ctx, daemonID)
	r.once.Do(r.detach)
	return rows, err
}

// A detach of the machine's checkout at a path other than the creation path,
// committing between the intake's detachment read and its checkout read, is
// seen whole: the intake resolves the project the detach is still waiting to
// detach, never a new one for the folder, and the detach then stands. Without
// the machine's checkout lock both reads came back empty and the folder came
// back as a duplicate project.
func TestEnsureOwnedProjectForPath_DetachBetweenItsReadsCreatesNoProject(t *testing.T) {
	f := newDetachedFixture(t)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.project.ID, f.laptop, f.localPath, nil))

	detachDone := make(chan error, 1)
	repo := &detachDuringIntakeRepo{Repo: f.repo}
	repo.detach = func() {
		go func() {
			detachDone <- f.repo.DetachProjectFromDaemon(f.ctx, f.project.ID, f.laptop, f.localPath, nil)
		}()
		// Hand control back once the detach either finished (no lock held it
		// off) or is waiting on an advisory lock in this test's database.
		for {
			select {
			case err := <-detachDone:
				detachDone <- err
				return
			default:
			}
			var waiting bool
			require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
				AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))`).Scan(f.ctx, &waiting))
			if waiting {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	svc := newTestService(repo)
	conn := newTestConn(f.userID, f.laptop, nil)

	got, err := svc.ensureOwnedProjectForPath(f.ctx, conn, f.localPath, nil)
	require.NoError(t, err)
	assert.Equal(t, f.project.ID, got.ID, "the intake resolved the checkout the detach had not yet removed")
	require.NoError(t, <-detachDone)

	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: f.userID})
	require.NoError(t, err)
	assert.Len(t, projects, 1, "no project is created for the detached folder")

	// The detach stands: the folder's next report is dropped, and a snapshot
	// of the project from the machine writes no catalog.
	_, err = svc.ensureOwnedProjectForPath(f.ctx, conn, f.localPath, nil)
	assert.True(t, errors.Is(err, errProjectDetached), "got %v", err)
	synced, err := f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
		ProjectID: f.project.ID, DaemonID: f.laptop, PushedAt: time.Unix(1_800_000_000, 0).UTC(),
	}, true)
	require.NoError(t, err)
	assert.False(t, synced)
	_, err = f.repo.GetProjectMachineCatalogPushedAt(f.ctx, f.project.ID, f.laptop)
	assert.True(t, svcerr.IsNotFound(err), "no catalog for the detached machine, got %v", err)
}

// A detach that commits while the connect reconcile is probing the machine
// (after it read the machine's detachments, before it writes) is not undone:
// the write re-checks the detachment under the lock the detach held.
func TestReconcile_DetachDuringTheProbeStaysDetached(t *testing.T) {
	f := newDetachedFixture(t)

	// The laptop has neither a row nor a detachment when the pass reads them;
	// the user attaches and then detaches it while the pass probes.
	detachDuringProbe := func(ctx context.Context, path string) (projectPathState, error) {
		require.NoError(t, f.repo.UpsertProjectDaemon(ctx, f.project.ID, f.laptop, path, nil))
		require.NoError(t, f.repo.DetachProjectFromDaemon(ctx, f.project.ID, f.laptop, path, nil))
		return projectPathState{Exists: true}, nil
	}
	_, added, err := reconcileProjectDaemons(f.ctx, f.repo, f.laptop, f.userID, detachDuringProbe)
	require.NoError(t, err)
	assert.Empty(t, added)

	rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, f.project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, f.cloud, rows[0].DaemonID, "the laptop's checkout was not recorded again")
	detached, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.laptop)
	require.NoError(t, err)
	assert.Len(t, detached, 1, "the detachment stands")
}
