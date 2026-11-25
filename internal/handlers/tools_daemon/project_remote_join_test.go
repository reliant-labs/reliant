package tools_daemon

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// remoteJoinFixture is one user's laptop and cloud machine, each with the
// same repository checked out at its own path, and the remote forms each
// reports of it.
type remoteJoinFixture struct {
	repo          *db.Repo
	svc           *Service
	ctx           context.Context
	userID        string
	laptop, cloud string
	laptopPath    string
	cloudPath     string
}

func newRemoteJoinFixture(t *testing.T) *remoteJoinFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := &remoteJoinFixture{
		repo:       repo,
		ctx:        context.Background(),
		userID:     "remote-user-" + uuid.NewString(),
		laptop:     "laptop-" + uuid.NewString(),
		cloud:      "cloud-" + uuid.NewString(),
		laptopPath: "/Users/sean/src/props",
		cloudPath:  "/home/workspace/projects/props",
	}
	for _, id := range []string{f.laptop, f.cloud} {
		require.NoError(t, repo.UpsertDaemon(f.ctx, &db.Daemon{ID: id, UserID: f.userID}))
	}
	f.svc = newTestService(repo)
	return f
}

// discover is the machine's discovery of the folder at path, reporting remote.
func (f *remoteJoinFixture) discover(t *testing.T, userID, machine, path, remote string) *db.Project {
	t.Helper()
	got, err := f.svc.ensureOwnedProjectForPath(f.ctx, newTestConn(userID, machine, nil), path,
		&reliantv1.DiscoveredProject{Path: path, Name: "props", IsGitRepo: true, RemoteUrl: remote})
	require.NoError(t, err)
	require.NotNil(t, got)
	return got
}

func (f *remoteJoinFixture) projects(t *testing.T, userID string) []*db.Project {
	t.Helper()
	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	return projects
}

// checkouts is "<machine>=<path>" of every checkout of the project, sorted.
func (f *remoteJoinFixture) checkouts(t *testing.T, projectID string) []string {
	t.Helper()
	rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, projectID)
	require.NoError(t, err)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.DaemonID+"="+r.Path)
	}
	sort.Strings(out)
	return out
}

func sortedPair(a, b string) []string {
	out := []string{a, b}
	sort.Strings(out)
	return out
}

// The laptop reports the repository's SSH remote, the cloud machine its
// HTTPS one, each at its own path: one project, with a checkout on each
// machine at that machine's path — in either order.
func TestDiscovery_ARepositoryOnASecondMachineJoinsItsProject(t *testing.T) {
	for name, remotes := range map[string][2]string{
		"ssh first":   {"git@github.com:Acme/props.git", "https://github.com/acme/props"},
		"https first": {"https://github.com/acme/props.git", "ssh://git@github.com/acme/props"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRemoteJoinFixture(t)
			first := f.discover(t, f.userID, f.laptop, f.laptopPath, remotes[0])
			require.NotNil(t, first.RemoteURL, "a project created for a discovered checkout records its remote")
			assert.Equal(t, remotes[0], *first.RemoteURL)

			second := f.discover(t, f.userID, f.cloud, f.cloudPath, remotes[1])
			assert.Equal(t, first.ID, second.ID, "the second machine's checkout is the same project")
			require.Len(t, f.projects(t, f.userID), 1, "no second project for the same repository")
			assert.Equal(t, sortedPair(f.laptop+"="+f.laptopPath, f.cloud+"="+f.cloudPath), f.checkouts(t, first.ID))

			// Reported again (a reconnect), each machine resolves its own
			// checkout: still one project, the paths unchanged.
			assert.Equal(t, first.ID, f.discover(t, f.userID, f.cloud, f.cloudPath, remotes[1]).ID)
			assert.Equal(t, first.ID, f.discover(t, f.userID, f.laptop, f.laptopPath, remotes[0]).ID)
			assert.Len(t, f.projects(t, f.userID), 1)
			assert.Equal(t, sortedPair(f.laptop+"="+f.laptopPath, f.cloud+"="+f.cloudPath), f.checkouts(t, first.ID))
		})
	}
}

// A folder with no remote (a local-only repository, or a daemon older than
// DiscoveredProject.remote_url) is resolved by path alone, as before: at
// another path it is another project; at the same path, the same one.
func TestDiscovery_NoRemoteKeepsThePathRules(t *testing.T) {
	f := newRemoteJoinFixture(t)
	first := f.discover(t, f.userID, f.laptop, f.laptopPath, "")
	assert.Nil(t, first.RemoteURL)
	other := f.discover(t, f.userID, f.cloud, f.cloudPath, "")
	assert.NotEqual(t, first.ID, other.ID, "no remote: a folder at another path is another project")
	same := f.discover(t, f.userID, f.cloud, f.laptopPath, "")
	assert.Equal(t, first.ID, same.ID, "no remote: a folder at the project's path is that project")

	// A remote that names no host (a local path) is no identity either.
	f2 := newRemoteJoinFixture(t)
	a := f2.discover(t, f2.userID, f2.laptop, f2.laptopPath, "/srv/git/props.git")
	b := f2.discover(t, f2.userID, f2.cloud, f2.cloudPath, "/srv/git/props.git")
	assert.NotEqual(t, a.ID, b.ID, "a local-path remote matched across machines")
}

// Another user's project of the same repository is never joined.
func TestDiscovery_AnotherUsersRepositoryIsNeverJoined(t *testing.T) {
	f := newRemoteJoinFixture(t)
	theirs := f.discover(t, f.userID, f.laptop, f.laptopPath, "git@github.com:acme/props.git")

	otherUser := "other-user-" + uuid.NewString()
	otherMachine := "their-machine-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: otherMachine, UserID: otherUser}))
	mine := f.discover(t, otherUser, otherMachine, f.cloudPath, "https://github.com/acme/props")
	assert.NotEqual(t, theirs.ID, mine.ID)
	assert.Equal(t, otherUser, mine.UserID)
	assert.Equal(t, []string{f.laptop + "=" + f.laptopPath}, f.checkouts(t, theirs.ID), "the other user's project gained no checkout")
}

// A second clone of a repository on a machine that already holds its project
// at another path does not move that checkout: the folder stays a project of
// its own, as before, without the repository's identity.
func TestDiscovery_ASecondCloneOnTheSameMachineDoesNotMoveItsCheckout(t *testing.T) {
	f := newRemoteJoinFixture(t)
	first := f.discover(t, f.userID, f.laptop, f.laptopPath, "git@github.com:acme/props.git")
	second := f.discover(t, f.userID, f.laptop, "/Users/sean/scratch/props-copy", "https://github.com/acme/props.git")
	assert.NotEqual(t, first.ID, second.ID)
	assert.Nil(t, second.RemoteURL, "the copy is not given the repository's remote: it is not the repository's project")
	assert.Equal(t, []string{f.laptop + "=" + f.laptopPath}, f.checkouts(t, first.ID))
}

// A project the user detached from a machine is not joined there by another
// folder of its repository; one scheduled for deletion is not joined at all.
func TestDiscovery_DetachedOrDeletedProjectsAreNotJoined(t *testing.T) {
	f := newRemoteJoinFixture(t)
	first := f.discover(t, f.userID, f.laptop, f.laptopPath, "git@github.com:acme/props.git")
	require.NoError(t, store.DetachProjectFromDaemon(f.ctx, f.repo.ORM(), first.ID, f.laptop, f.laptopPath, nil))
	again := f.discover(t, f.userID, f.laptop, "/Users/sean/elsewhere/props", "https://github.com/acme/props")
	assert.NotEqual(t, first.ID, again.ID, "a detached project was joined again on the machine it was detached from")

	g := newRemoteJoinFixture(t)
	dying := g.discover(t, g.userID, g.laptop, g.laptopPath, "git@github.com:acme/props.git")
	_, err := g.repo.ORM().Bun().NewRaw("UPDATE projects SET delete_after = ? WHERE id = ?", time.Now().Add(time.Hour), dying.ID).Exec(g.ctx)
	require.NoError(t, err)
	fresh := g.discover(t, g.userID, g.cloud, g.cloudPath, "https://github.com/acme/props")
	assert.NotEqual(t, dying.ID, fresh.ID, "a project scheduled for deletion was joined")
}
