package tools_daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
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
	require.NoError(t, f.repo.DetachProjectFromDaemon(f.ctx, f.project.ID, f.laptop, f.project.Path))
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
	require.NoError(t, f.repo.DetachProjectFromDaemon(f.ctx, f.project.ID, f.laptop, f.localPath))
	svc := newTestService(f.repo)

	_, err := svc.ensureOwnedProjectForPath(f.ctx, newTestConn(f.userID, f.laptop, nil), f.localPath, nil)
	assert.True(t, errors.Is(err, errProjectDetached), "got %v", err)
	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: f.userID})
	require.NoError(t, err)
	assert.Len(t, projects, 1)
}
