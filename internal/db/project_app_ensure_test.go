package db

import (
	"context"
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

	first, err := r.EnsureProjectForApp(ctx, ensureInput("org-a", "u1"))
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

	again, err := r.EnsureProjectForApp(ctx, ensureInput("org-a", "u1"))
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
			res, err := r.EnsureProjectForApp(ctx, ensureInput("org-a", "u1"))
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

	res, err := r.EnsureProjectForApp(ctx, ensureInput(org, "u1"))
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

	res, err := r.EnsureProjectForApp(ctx, ensureInput("org-a", "u1"))
	require.NoError(t, err)
	assert.True(t, res.Created)
	assert.NotEqual(t, "elsewhere", res.ProjectID)
	p, err := r.GetProject(ctx, res.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, "org-a", *p.OrgID)
	assert.Equal(t, "shop (app)", p.Name, "the user's path for \"shop\" is taken across orgs")
	assert.Empty(t, forgeApps(t, r, "elsewhere"))
}
