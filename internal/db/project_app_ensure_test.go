package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

func ensureInput(org, user string) EnsureProjectForAppInput {
	return EnsureProjectForAppInput{OrgID: org, UserID: user, AppName: "shop", Remote: "github.com/acme/shop", Subpath: "apps/web"}
}

func countOrgProjects(t *testing.T, r *Repo, org string) int {
	t.Helper()
	var n int
	require.NoError(t, r.ORM().Bun().NewRaw(`SELECT count(*) FROM projects WHERE org_id = ?0`, org).Scan(context.Background(), &n))
	return n
}

func forgeApps(t *testing.T, r *Repo, projectID string) []string {
	t.Helper()
	apps, err := store.ListRepoForgeAppsByProject(context.Background(), r.ORM(), projectID)
	require.NoError(t, err)
	var out []string
	for _, a := range apps {
		out = append(out, a.Subpath+"="+a.Name)
	}
	return out
}

// A first call creates a remote-only project with its root repo and app row;
// a second returns the same project.
func TestEnsureProjectForApp_CreatesOnceThenReuses(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	first, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput("org-a", "u1"))
	require.NoError(t, err)
	assert.True(t, first.Created)
	p, err := r.GetProject(ctx, first.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, "shop", p.Name)
	assert.Equal(t, "u1", p.UserID)
	require.NotNil(t, p.OrgID)
	assert.Equal(t, "org-a", *p.OrgID)
	require.NotNil(t, p.RemoteURL)
	assert.Equal(t, "https://github.com/acme/shop", *p.RemoteURL)
	assert.False(t, p.CreatedAt.IsZero())
	daemons, err := r.ListProjectDaemonsForProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Empty(t, daemons)
	assert.Equal(t, []string{"apps/web=shop"}, forgeApps(t, r, p.ID))

	again, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput("org-a", "u1"))
	require.NoError(t, err)
	assert.Equal(t, first.ProjectID, again.ProjectID)
	assert.False(t, again.Created)
	assert.Equal(t, 1, countOrgProjects(t, r, "org-a"))
}

func TestEnsureProjectForApp_ConcurrentCallsCreateOne(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	const n = 6
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput("org-a", "u1"))
			ids[i], errs[i] = res.ProjectID, err
		}()
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, ids[0], ids[i])
	}
	assert.Equal(t, 1, countOrgProjects(t, r, "org-a"))
}

// Another member's project in the org with the same remote (any spelling)
// is the app's project; the app is recorded on it.
func TestEnsureProjectForApp_ReusesAnotherUsersProjectInTheOrg(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	org, remote, now := "org-a", "git@github.com:acme/shop.git", time.Now().UTC()
	require.NoError(t, r.CreateProject(ctx, &Project{ID: "theirs", Name: "Shop", Path: "/src/shop", UserID: "u2",
		OrgID: &org, RemoteURL: &remote, IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))

	res, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput(org, "u1"))
	require.NoError(t, err)
	assert.Equal(t, "theirs", res.ProjectID)
	assert.False(t, res.Created)
	assert.Equal(t, []string{"apps/web=shop"}, forgeApps(t, r, "theirs"))
	assert.Equal(t, 1, countOrgProjects(t, r, org))
}

// A project in another org is never matched, even the same user's: the app
// gets its own in the right org.
func TestEnsureProjectForApp_NeverMatchesAnotherOrg(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	other, remote, now := "org-b", "https://github.com/acme/shop", time.Now().UTC()
	require.NoError(t, r.CreateProject(ctx, &Project{ID: "elsewhere", Name: "shop", Path: "/home/workspace/projects/shop", UserID: "u1",
		OrgID: &other, RemoteURL: &remote, IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))

	res, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput("org-a", "u1"))
	require.NoError(t, err)
	assert.True(t, res.Created)
	assert.NotEqual(t, "elsewhere", res.ProjectID)
	p, err := r.GetProject(ctx, res.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, "org-a", *p.OrgID)
	assert.Equal(t, "shop (app)", p.Name, "the user's path for \"shop\" is taken across orgs")
	assert.Empty(t, forgeApps(t, r, "elsewhere"))
}

// Every spelling gitremote.Canonical equates is the same remote: userinfo, a
// port, an upper-case host, a .git suffix and a trailing slash still find the
// live project rather than creating a duplicate.
func TestEnsureProjectForApp_MatchesAnySpellingOfTheRemote(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	org, remote, now := "org-a", "https://deploy:tok@GitHub.com:443/acme/shop.git/", time.Now().UTC()
	require.NoError(t, r.CreateProject(ctx, &Project{ID: "spelled", Name: "Shop", Path: "/src/shop", UserID: "u2",
		OrgID: &org, RemoteURL: &remote, IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))

	res, err := store.EnsureProjectForApp(ctx, r.ORM(), ensureInput(org, "u1"))
	require.NoError(t, err)
	assert.Equal(t, "spelled", res.ProjectID)
	assert.False(t, res.Created)
	assert.Equal(t, 1, countOrgProjects(t, r, org))
}

// Concurrent ensures of different remotes for one user and app name each get
// a project of their own, under distinct free names, and none fails: their
// name choice is serialized (LockUserProjectNames), so they never race for
// the same name or placeholder path.
func TestEnsureProjectForApp_ConcurrentRemotesGetDistinctNames(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	const n = 6
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := ensureInput("org-a", "u1")
			in.Remote = fmt.Sprintf("github.com/acme/shop-%d", i)
			res, err := store.EnsureProjectForApp(ctx, r.ORM(), in)
			ids[i], errs[i] = res.ProjectID, err
		}()
	}
	wg.Wait()
	names := map[string]bool{}
	for i := range n {
		require.NoError(t, errs[i])
		p, err := r.GetProject(ctx, ids[i])
		require.NoError(t, err)
		assert.False(t, names[p.Name], "name %q chosen twice", p.Name)
		names[p.Name] = true
	}
	assert.Equal(t, n, countOrgProjects(t, r, "org-a"))
}
