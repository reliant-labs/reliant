// Copyright (c) 2025 Reliant Labs
package projectfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeDiscoverDaemon answers repo.discover with a fixed payload and counts
// what it was asked.
type fakeDiscoverDaemon struct {
	answer   []byte
	err      error
	asked    int
	payloads []map[string]any
	daemons  []string
}

func (f *fakeDiscoverDaemon) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType != "repo.discover" {
		return nil, fmt.Errorf("unexpected command %s", commandType)
	}
	f.asked++
	f.daemons = append(f.daemons, daemonID)
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	f.payloads = append(f.payloads, p)
	return f.answer, f.err
}

// workspaceAnswer is a newer daemon's repo.discover for a project shaped like
// this workspace: three repos, each a forge app at its root, and one with a
// nested app too.
const workspaceAnswer = `{
  "discovered": [
    {"relative_path": "control-plane", "name": "control-plane", "forge_apps": [{"subpath": "", "name": "control-plane"}]},
    {"relative_path": "forge", "name": "forge", "forge_apps": [{"subpath": "", "name": "forge"}]},
    {"relative_path": "reliant", "name": "reliant", "forge_apps": [
      {"subpath": "", "name": "reliant"}, {"subpath": "frontends/site", "name": "reliant-site"}]}
  ],
  "has_forge": false,
  "forge_apps_scanned": true
}`

// oldDaemonAnswer is what a daemon older than the forge-app walk answers the
// same request with: the field it does not know is ignored.
const oldDaemonAnswer = `{
  "discovered": [
    {"relative_path": "control-plane", "name": "control-plane"},
    {"relative_path": "forge", "name": "forge"},
    {"relative_path": "reliant", "name": "reliant"}
  ],
  "has_forge": false
}`

// seedWorkspaceProject creates a project with the workspace's three repos
// registered, as CreateProject leaves it.
func seedWorkspaceProject(t *testing.T, repo *db.Repo, userID string) (*db.Project, map[string]*core.Repo) {
	t.Helper()
	ctx := context.Background()
	project := &db.Project{
		ID: uuid.NewString(), Name: "reliant-labs", UserID: userID, IsGitRepo: true,
		Path: "/home/workspace/projects/reliant-labs-" + uuid.NewString(),
	}
	require.NoError(t, repo.CreateProject(ctx, project))
	repos := map[string]*core.Repo{}
	for _, rel := range []string{"control-plane", "forge", "reliant"} {
		r := &core.Repo{ID: uuid.NewString(), ProjectID: project.ID, Name: rel, RelativePath: rel,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		require.NoError(t, repo.CreateRepo(ctx, r))
		repos[rel] = r
	}
	return project, repos
}

// appsByRepoPath reads back what is recorded, keyed by repo relative path,
// with each repo's scan stamp.
func appsByRepoPath(t *testing.T, repo *db.Repo, projectID string) (map[string][]string, map[string]*time.Time) {
	t.Helper()
	ctx := context.Background()
	repos, err := repo.ListReposByProject(ctx, projectID)
	require.NoError(t, err)
	pathOf := map[string]string{}
	stamps := map[string]*time.Time{}
	for _, r := range repos {
		pathOf[r.ID] = r.RelativePath
		stamps[r.RelativePath] = r.ForgeAppsScannedAt
	}
	apps, err := repo.ListRepoForgeAppsByProject(ctx, projectID)
	require.NoError(t, err)
	got := map[string][]string{}
	for _, app := range apps {
		got[pathOf[app.RepoID]] = append(got[pathOf[app.RepoID]], app.Subpath+"="+app.Name)
	}
	return got, stamps
}

func TestRefreshForgeApps_RecordsEveryAppInAWorkspace(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgeapps-" + uuid.NewString()
	project, _ := seedWorkspaceProject(t, repo, userID)
	daemonID := uuid.NewString()
	daemon := &fakeDiscoverDaemon{answer: []byte(workspaceAnswer)}

	RefreshForgeApps(context.Background(), daemon, repo, userID, daemonID, project.ID, project.Path, 0)

	require.Equal(t, 1, daemon.asked)
	assert.Equal(t, []string{daemonID}, daemon.daemons, "asked of the machine holding the checkout")
	assert.Equal(t, true, daemon.payloads[0]["forge_apps"], "the request asks for the walk")
	assert.Equal(t, project.Path, daemon.payloads[0]["path"])

	got, stamps := appsByRepoPath(t, repo, project.ID)
	assert.Equal(t, map[string][]string{
		"control-plane": {"=control-plane"},
		"forge":         {"=forge"},
		"reliant":       {"=reliant", "frontends/site=reliant-site"},
	}, got)
	for rel, stamp := range stamps {
		assert.NotNil(t, stamp, "%s had a complete scan", rel)
	}

	// The project row's root forge identity is a public surface, untouched.
	stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
	require.NoError(t, err)
	assert.False(t, stored.IsForge)
	assert.Nil(t, stored.ForgeProjectName)
}

// An old daemon ignores the request field: its answer says nothing about
// forge apps, which is UNKNOWN. Nothing is written, nothing errors, and what
// a newer daemon recorded before stays.
func TestRefreshForgeApps_AnOldDaemonReportsNothingAndBreaksNothing(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgeapps-" + uuid.NewString()
	project, _ := seedWorkspaceProject(t, repo, userID)

	old := &fakeDiscoverDaemon{answer: []byte(oldDaemonAnswer)}
	RefreshForgeApps(context.Background(), old, repo, userID, uuid.NewString(), project.ID, project.Path, 0)
	require.Equal(t, 1, old.asked)

	got, stamps := appsByRepoPath(t, repo, project.ID)
	assert.Empty(t, got)
	for rel, stamp := range stamps {
		assert.Nil(t, stamp, "%s: an old daemon's answer must leave the apps unknown, not \"none\"", rel)
	}

	// A newer daemon records them; an old one answering later keeps them.
	RefreshForgeApps(context.Background(), &fakeDiscoverDaemon{answer: []byte(workspaceAnswer)},
		repo, userID, uuid.NewString(), project.ID, project.Path, 0)
	RefreshForgeApps(context.Background(), old, repo, userID, uuid.NewString(), project.ID, project.Path, 0)
	got, _ = appsByRepoPath(t, repo, project.ID)
	assert.Len(t, got, 3, "an old daemon's silence erased nothing")

	// A daemon that dropped mid-read defers, it does not corrupt.
	RefreshForgeApps(context.Background(), &fakeDiscoverDaemon{err: fmt.Errorf("daemon disconnected")},
		repo, userID, uuid.NewString(), project.ID, project.Path, 0)
	got, _ = appsByRepoPath(t, repo, project.ID)
	assert.Len(t, got, 3)
}

func TestRecordForgeApps_CompleteReplacesPartialOnlyAdds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	userID := "user-forgeapps-" + uuid.NewString()
	project, _ := seedWorkspaceProject(t, repo, userID)
	scan, ok := ParseForgeApps([]byte(workspaceAnswer))
	require.True(t, ok)
	require.Equal(t, 3, RecordForgeApps(ctx, repo, project.ID, scan))

	// reliant's nested site was deleted and its root app renamed; the walk
	// covered the repo, so the set is replaced.
	scan, ok = ParseForgeApps([]byte(`{"forge_apps_scanned": true, "discovered": [
	  {"relative_path": "reliant", "forge_apps": [{"subpath": "", "name": "reliant-2"}]}]}`))
	require.True(t, ok)
	require.Equal(t, 1, RecordForgeApps(ctx, repo, project.ID, scan))
	got, _ := appsByRepoPath(t, repo, project.ID)
	assert.Equal(t, []string{"=reliant-2"}, got["reliant"])
	assert.Equal(t, []string{"=forge"}, got["forge"], "a repo the scan does not mention is left as it is")

	// A partial walk adds what it found and removes nothing it did not reach.
	before, stamps := appsByRepoPath(t, repo, project.ID)
	stampBefore := *stamps["forge"]
	scan, ok = ParseForgeApps([]byte(`{"forge_apps_scanned": true, "discovered": [
	  {"relative_path": "forge", "forge_apps_truncated": true, "forge_apps": [{"subpath": "docs/site", "name": "forge-docs"}]}]}`))
	require.True(t, ok)
	RecordForgeApps(ctx, repo, project.ID, scan)
	got, stamps = appsByRepoPath(t, repo, project.ID)
	assert.Equal(t, []string{"=forge", "docs/site=forge-docs"}, got["forge"])
	assert.Equal(t, before["reliant"], got["reliant"])
	assert.True(t, stamps["forge"].Equal(stampBefore), "a partial walk is not a complete scan")

	// A complete scan that finds nothing means the repo has no app.
	scan, ok = ParseForgeApps([]byte(`{"forge_apps_scanned": true, "discovered": [{"relative_path": "control-plane"}]}`))
	require.True(t, ok)
	RecordForgeApps(ctx, repo, project.ID, scan)
	got, stamps = appsByRepoPath(t, repo, project.ID)
	assert.Empty(t, got["control-plane"])
	assert.NotNil(t, stamps["control-plane"], "known, and none")
}

// A topology read refreshes at most every staleAfter: a project whose repos
// were all scanned since is not asked again.
func TestRefreshForgeApps_SkipsWhileFresh(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgeapps-" + uuid.NewString()
	project, _ := seedWorkspaceProject(t, repo, userID)
	daemon := &fakeDiscoverDaemon{answer: []byte(workspaceAnswer)}

	RefreshForgeApps(context.Background(), daemon, repo, userID, "d", project.ID, project.Path, time.Hour)
	require.Equal(t, 1, daemon.asked, "never scanned: asked")
	RefreshForgeApps(context.Background(), daemon, repo, userID, "d", project.ID, project.Path, time.Hour)
	assert.Equal(t, 1, daemon.asked, "scanned within the hour: not asked again")
	RefreshForgeApps(context.Background(), daemon, repo, userID, "d", project.ID, project.Path, 0)
	assert.Equal(t, 2, daemon.asked, "a clone or attach always asks")
}

func TestParseForgeApps(t *testing.T) {
	_, ok := ParseForgeApps([]byte(oldDaemonAnswer))
	assert.False(t, ok, "no forge_apps_scanned: unknown")
	_, ok = ParseForgeApps([]byte(`not json`))
	assert.False(t, ok)

	// A Windows daemon's relative paths read the same as a Linux one's, and
	// a nameless or duplicate entry is dropped.
	scan, ok := ParseForgeApps([]byte(`{"forge_apps_scanned": true, "discovered": [
	  {"relative_path": "apps\\web", "forge_apps": [
	    {"subpath": "", "name": " web "}, {"subpath": "", "name": "dup"}, {"subpath": "x", "name": ""}]},
	  {"relative_path": ".", "forge_apps": []}]}`))
	require.True(t, ok)
	assert.Equal(t, ForgeAppsScan{
		"apps/web": {Apps: []core.RepoForgeApp{{Subpath: "", Name: "web"}}, Complete: true},
		"":         {Complete: true},
	}, scan)
}
