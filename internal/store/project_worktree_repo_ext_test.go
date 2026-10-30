// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestProjectQueries ports projects.sql: every project read and write
// through the store, interleaved with the Repository on the same database.
func TestProjectQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	// Written by the store, read by the Repository. The caller's timestamps
	// survive, and the remote URL is stored without credentials.
	older := &core.Project{
		ID: "proj-older", UserID: "user-a", Name: "older", Path: "/srv/older", IsGitRepo: true,
		RemoteURL: strp("https://tok:secret@github.com/acme/older.git"), DefaultBranch: strp("main"),
		CreatedAt: base, UpdatedAt: base.Add(time.Second), LastActive: base,
	}
	if err := store.InsertProject(ctx, env.ORM, older); err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	twin, err := env.Repo.GetProject(ctx, "proj-older")
	if err != nil {
		t.Fatalf("Repo.GetProject: %v", err)
	}
	if !twin.UpdatedAt.Equal(base.Add(time.Second)) || !twin.CreatedAt.Equal(base) || !twin.IsGitRepo ||
		*twin.RemoteURL != "https://github.com/acme/older.git" || *twin.DefaultBranch != "main" {
		t.Fatalf("Repo.GetProject = %+v, want the store's write", twin)
	}

	// Written by the Repository, read by the store.
	newer := &core.Project{
		ID: "proj-newer", UserID: "user-a", Name: "newer", Path: "/srv/newer", IsGitRepo: false,
		RemoteURL: strp("https://github.com/acme/newer.git"), IsForge: true, ForgeProjectName: strp("newer"),
		CreatedAt: base, UpdatedAt: base, LastActive: base.Add(time.Minute),
	}
	if err := env.Repo.CreateProject(ctx, newer); err != nil {
		t.Fatalf("Repo.CreateProject: %v", err)
	}
	got, err := store.GetProject(ctx, env.ORM, "proj-newer")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.IsGitRepo || !got.IsForge || *got.ForgeProjectName != "newer" || !got.LastActive.Equal(base.Add(time.Minute)) {
		t.Fatalf("GetProject = %+v, want the Repository's write", got)
	}
	if _, err := store.GetProject(ctx, env.ORM, "proj-missing"); err == nil || err.Error() != "project not found: proj-missing" {
		t.Fatalf("GetProject(missing) err = %v, want project not found", err)
	}

	if p, err := store.GetProjectByPath(ctx, env.ORM, "/srv/newer"); err != nil || p.ID != "proj-newer" {
		t.Fatalf("GetProjectByPath = %v, %v", p, err)
	}
	if _, err := store.GetProjectByPath(ctx, env.ORM, "/srv/none"); err == nil {
		t.Fatal("GetProjectByPath(missing) = nil error")
	}
	if p, err := store.GetProjectByPathAndUser(ctx, env.ORM, "/srv/older", "user-a"); err != nil || p.ID != "proj-older" {
		t.Fatalf("GetProjectByPathAndUser = %v, %v", p, err)
	}
	if _, err := store.GetProjectByPathAndUser(ctx, env.ORM, "/srv/older", "user-b"); err == nil {
		t.Fatal("GetProjectByPathAndUser(other user) = nil error")
	}
	// The lookup strips credentials from the URL it is given, as the write did.
	if p, err := store.GetProjectByRemoteURLAndUser(ctx, env.ORM, "https://x:y@github.com/acme/older.git", "user-a"); err != nil || p.ID != "proj-older" {
		t.Fatalf("GetProjectByRemoteURLAndUser = %v, %v", p, err)
	}
	if _, err := store.GetProjectWithUserCheck(ctx, env.ORM, "proj-older", "user-b"); !errors.Is(err, core.ErrProjectNotFound) {
		t.Fatalf("GetProjectWithUserCheck(other user) err = %v, want ErrProjectNotFound", err)
	}
	if p, err := store.GetProjectWithUserCheck(ctx, env.ORM, "proj-older", "user-a"); err != nil || p.ID != "proj-older" {
		t.Fatalf("GetProjectWithUserCheck = %v, %v", p, err)
	}

	// Most recently active first, with paging; the twin agrees.
	list, err := store.ListProjects(ctx, env.ORM, core.ProjectFilters{UserID: "user-a"})
	if err != nil || len(list) != 2 || list[0].ID != "proj-newer" || list[1].ID != "proj-older" {
		t.Fatalf("ListProjects = %v, %v; want [newer older]", list, err)
	}
	page, err := store.ListProjects(ctx, env.ORM, core.ProjectFilters{UserID: "user-a", Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != "proj-older" {
		t.Fatalf("ListProjects(page 2) = %v, %v", page, err)
	}
	twins, err := env.Repo.ListProjects(ctx, core.ProjectFilters{UserID: "user-a"})
	if err != nil || len(twins) != 2 || twins[0].ID != list[0].ID {
		t.Fatalf("Repo.ListProjects = %v, %v", twins, err)
	}

	// Replace: another user's write is a no-op; the owner's lands and stamps updated_at.
	edit := *got
	edit.Name, edit.Description, edit.IsGitRepo = "renamed", strp("desc"), true
	if err := store.ReplaceProject(ctx, env.ORM, &edit, "user-b"); err != nil {
		t.Fatalf("ReplaceProject(other user): %v", err)
	}
	if p, _ := env.Repo.GetProject(ctx, "proj-newer"); p.Name != "newer" {
		t.Fatalf("ReplaceProject by another user wrote: %+v", p)
	}
	if err := store.ReplaceProject(ctx, env.ORM, &edit, "user-a"); err != nil {
		t.Fatalf("ReplaceProject: %v", err)
	}
	if p, _ := env.Repo.GetProject(ctx, "proj-newer"); p.Name != "renamed" || *p.Description != "desc" || !p.IsGitRepo || !p.UpdatedAt.After(base) {
		t.Fatalf("after ReplaceProject = %+v", p)
	}

	// SetProjectForgeName writes once, then is a no-op for the same name.
	if wrote, err := store.SetProjectForgeName(ctx, env.ORM, "proj-older", "user-a", "forge-older"); err != nil || !wrote {
		t.Fatalf("SetProjectForgeName = %v, %v; want true", wrote, err)
	}
	if wrote, err := store.SetProjectForgeName(ctx, env.ORM, "proj-older", "user-a", "forge-older"); err != nil || wrote {
		t.Fatalf("SetProjectForgeName(again) = %v, %v; want false", wrote, err)
	}
	if wrote, err := env.Repo.SetProjectForgeName(ctx, "proj-older", "user-a", "forge-older"); err != nil || wrote {
		t.Fatalf("Repo.SetProjectForgeName(again) = %v, %v; want false", wrote, err)
	}

	if err := store.TouchProject(ctx, env.ORM, "proj-older", "user-a"); err != nil {
		t.Fatalf("TouchProject: %v", err)
	}
	if p, _ := env.Repo.GetProject(ctx, "proj-older"); !p.LastActive.After(base.Add(time.Minute)) || !p.IsForge || *p.ForgeProjectName != "forge-older" {
		t.Fatalf("after TouchProject = %+v", p)
	}

	// Delete: another user's is a no-op.
	if err := store.DeleteUserProject(ctx, env.ORM, "proj-older", "user-b"); err != nil {
		t.Fatalf("DeleteUserProject(other user): %v", err)
	}
	if _, err := env.Repo.GetProject(ctx, "proj-older"); err != nil {
		t.Fatalf("DeleteUserProject by another user deleted: %v", err)
	}
	if err := store.DeleteUserProject(ctx, env.ORM, "proj-older", "user-a"); err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	if _, err := env.Repo.GetProject(ctx, "proj-older"); err == nil {
		t.Fatal("project survived DeleteUserProject")
	}
}

// TestRepoQueries ports repos.sql.
func TestRepoQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	if err := store.InsertRepo(ctx, env.ORM, &core.Repo{
		ID: "repo-b", ProjectID: storetest.SeedProjectID, Name: "b", RelativePath: "b",
		RemoteURL: strp("https://u:p@github.com/acme/b.git"), CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("InsertRepo: %v", err)
	}
	twin, err := env.Repo.GetRepo(ctx, "repo-b")
	if err != nil || !twin.UpdatedAt.Equal(base) || *twin.RemoteURL != "https://github.com/acme/b.git" {
		t.Fatalf("Repo.GetRepo = %+v, %v", twin, err)
	}
	// Zero timestamps take the column DEFAULT.
	if err := env.Repo.CreateRepo(ctx, &core.Repo{ID: "repo-a", ProjectID: storetest.SeedProjectID, Name: "a", RelativePath: "a", CreatedAt: base, UpdatedAt: base}); err != nil {
		t.Fatalf("Repo.CreateRepo: %v", err)
	}
	if err := store.InsertRepo(ctx, env.ORM, &core.Repo{ID: "repo-root", ProjectID: storetest.SeedProjectID, Name: "root"}); err != nil {
		t.Fatalf("InsertRepo(zero times): %v", err)
	}
	root, err := store.GetRepo(ctx, env.ORM, "repo-root")
	if err != nil || root.CreatedAt.IsZero() || root.RelativePath != "" {
		t.Fatalf("GetRepo(root) = %+v, %v", root, err)
	}
	if _, err := store.GetRepo(ctx, env.ORM, "repo-missing"); err == nil || err.Error() != "repo not found: repo-missing" {
		t.Fatalf("GetRepo(missing) err = %v", err)
	}
	if r, err := store.GetRepoByProjectAndPath(ctx, env.ORM, storetest.SeedProjectID, "a"); err != nil || r.ID != "repo-a" {
		t.Fatalf("GetRepoByProjectAndPath = %v, %v", r, err)
	}
	if _, err := store.GetRepoByProjectAndPath(ctx, env.ORM, storetest.SeedProjectID, "zz"); err == nil {
		t.Fatal("GetRepoByProjectAndPath(missing) = nil error")
	}
	list, err := store.ListReposByProject(ctx, env.ORM, storetest.SeedProjectID)
	if err != nil || len(list) != 3 || list[0].ID != "repo-root" || list[1].ID != "repo-a" || list[2].ID != "repo-b" {
		t.Fatalf("ListReposByProject = %v, %v; want [root a b]", list, err)
	}

	if err := store.RemoveRepo(ctx, env.ORM, "repo-a"); err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	if err := store.RemoveRepo(ctx, env.ORM, "repo-a"); err != nil {
		t.Fatalf("RemoveRepo(missing): %v", err)
	}
	if _, err := env.Repo.GetRepo(ctx, "repo-a"); err == nil {
		t.Fatal("repo survived RemoveRepo")
	}
}

// TestProjectDaemonQueries ports project_daemons.sql.
func TestProjectDaemonQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	p := storetest.SeedProjectID

	// Queued through the store, read by the Repository.
	if err := store.RecordProjectDaemonQueued(ctx, env.ORM, p, "d1", "/will/be", strp("main"), "req-1"); err != nil {
		t.Fatalf("RecordProjectDaemonQueued: %v", err)
	}
	rows, err := env.Repo.ListProjectDaemonsForProject(ctx, p)
	if err != nil || len(rows) != 1 || rows[0].InstallState != core.ProjectInstallInstalling || rows[0].InstallRequestID != "req-1" || *rows[0].DefaultBranch != "main" {
		t.Fatalf("Repo.ListProjectDaemonsForProject = %+v, %v", rows, err)
	}
	clonedAt := rows[0].ClonedAt

	// A failure is matched by request id.
	if err := store.ResolveQueuedProjectDaemon(ctx, env.ORM, "req-1", "clone failed"); err != nil {
		t.Fatalf("ResolveQueuedProjectDaemon(failed): %v", err)
	}
	pd, err := store.GetProjectDaemonByPK(ctx, env.ORM, p, "d1")
	if err != nil || pd.InstallState != core.ProjectInstallFailed || pd.InstallError != "clone failed" {
		t.Fatalf("after failure = %+v, %v", pd, err)
	}
	// An empty request id never matches a row.
	if err := store.ResolveQueuedProjectDaemon(ctx, env.ORM, "", ""); err != nil {
		t.Fatalf("ResolveQueuedProjectDaemon(empty): %v", err)
	}

	// The completed clone corrects the row and keeps its cloned_at.
	time.Sleep(2 * time.Millisecond)
	if err := store.RecordProjectDaemonInstalled(ctx, env.ORM, p, "d1", "/srv/d1", nil); err != nil {
		t.Fatalf("RecordProjectDaemonInstalled: %v", err)
	}
	pd, _ = store.GetProjectDaemonByPK(ctx, env.ORM, p, "d1")
	if pd.InstallState != core.ProjectInstallInstalled || pd.InstallError != "" || pd.InstallRequestID != "" || pd.Path != "/srv/d1" || pd.DefaultBranch != nil || !pd.ClonedAt.Equal(clonedAt) {
		t.Fatalf("after install = %+v, want installed, cloned_at %v kept", pd, clonedAt)
	}

	// Written by the Repository, settled through the store by request and by announcement.
	if err := env.Repo.UpsertQueuedProjectDaemon(ctx, p, "d2", "/srv/d2", nil, "req-2"); err != nil {
		t.Fatalf("Repo.UpsertQueuedProjectDaemon: %v", err)
	}
	if err := store.ResolveQueuedProjectDaemon(ctx, env.ORM, "req-2", ""); err != nil {
		t.Fatalf("ResolveQueuedProjectDaemon(installed): %v", err)
	}
	if pd, _ := store.GetProjectDaemonByPK(ctx, env.ORM, p, "d2"); pd.InstallState != core.ProjectInstallInstalled {
		t.Fatalf("after resolve = %+v", pd)
	}
	if err := env.Repo.UpsertQueuedProjectDaemon(ctx, p, "d3", "/srv/d3", nil, "req-3"); err != nil {
		t.Fatalf("Repo.UpsertQueuedProjectDaemon: %v", err)
	}
	if err := store.MarkProjectDaemonInstalled(ctx, env.ORM, p, "d3"); err != nil {
		t.Fatalf("MarkProjectDaemonInstalled: %v", err)
	}
	if pd, _ := store.GetProjectDaemonByPK(ctx, env.ORM, p, "d3"); pd.InstallState != core.ProjectInstallInstalled {
		t.Fatalf("after mark = %+v", pd)
	}

	list, err := store.ListProjectDaemonsForProject(ctx, env.ORM, p)
	if err != nil || len(list) != 3 || list[0].DaemonID != "d1" {
		t.Fatalf("ListProjectDaemonsForProject = %+v, %v", list, err)
	}
	byDaemon, err := store.ListProjectDaemonsForDaemon(ctx, env.ORM, "d2")
	if err != nil || len(byDaemon) != 1 || byDaemon[0].Path != "/srv/d2" {
		t.Fatalf("ListProjectDaemonsForDaemon = %+v, %v", byDaemon, err)
	}

	if err := store.RemoveProjectDaemon(ctx, env.ORM, p, "d2"); err != nil {
		t.Fatalf("RemoveProjectDaemon: %v", err)
	}
	if err := store.RemoveProjectDaemon(ctx, env.ORM, p, "d2"); err != nil {
		t.Fatalf("RemoveProjectDaemon(missing): %v", err)
	}
	if left, _ := env.Repo.ListProjectDaemonsForDaemon(ctx, "d2"); len(left) != 0 {
		t.Fatalf("row survived RemoveProjectDaemon: %+v", left)
	}
}

// TestVisibilityAndFavoriteQueries ports visibility.sql's overrides and
// command_favorites.sql.
func TestVisibilityAndFavoriteQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	wf := reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW

	if v, err := store.GetVisibilityOverride(ctx, env.ORM, "u", wf, "s1"); err != nil || v != nil {
		t.Fatalf("GetVisibilityOverride(none) = %v, %v", v, err)
	}
	if err := store.SetVisibilityOverride(ctx, env.ORM, "u", wf, "s1", false); err != nil {
		t.Fatalf("SetVisibilityOverride: %v", err)
	}
	if err := store.SetVisibilityOverride(ctx, env.ORM, "u", wf, "s1", true); err != nil {
		t.Fatalf("SetVisibilityOverride(replace): %v", err)
	}
	if v, err := env.Repo.GetVisibilityOverride(ctx, "u", int32(wf), "s1"); err != nil || v == nil || !*v {
		t.Fatalf("Repo.GetVisibilityOverride = %v, %v; want true", v, err)
	}
	if err := env.Repo.SetVisibilityOverride(ctx, "u", int32(wf), "s0", false); err != nil {
		t.Fatalf("Repo.SetVisibilityOverride: %v", err)
	}
	if err := env.Repo.SetVisibilityOverride(ctx, "u", int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET), "s0", true); err != nil {
		t.Fatalf("Repo.SetVisibilityOverride(preset): %v", err)
	}
	m, err := store.ListVisibilityOverrides(ctx, env.ORM, "u", wf)
	if err != nil || len(m) != 2 || m["s0"] || !m["s1"] {
		t.Fatalf("ListVisibilityOverrides = %v, %v", m, err)
	}

	p := storetest.SeedProjectID
	for _, k := range []string{"build", "test", "build"} {
		if err := store.AddCommandFavorite(ctx, env.ORM, "u", p, k); err != nil {
			t.Fatalf("AddCommandFavorite(%s): %v", k, err)
		}
	}
	if err := env.Repo.AddCommandFavorite(ctx, "u", p, "lint"); err != nil {
		t.Fatalf("Repo.AddCommandFavorite: %v", err)
	}
	keys, err := store.ListCommandFavorites(ctx, env.ORM, "u", p)
	if err != nil || len(keys) != 3 || keys[0] != "build" || keys[1] != "test" || keys[2] != "lint" {
		t.Fatalf("ListCommandFavorites = %v, %v; want [build test lint]", keys, err)
	}
	if err := store.RemoveCommandFavorite(ctx, env.ORM, "u", p, "test"); err != nil {
		t.Fatalf("RemoveCommandFavorite: %v", err)
	}
	if twin, err := env.Repo.ListCommandFavorites(ctx, "u", p); err != nil || len(twin) != 2 {
		t.Fatalf("Repo.ListCommandFavorites = %v, %v", twin, err)
	}
}

func newWorktree(id, name, branch string, at time.Time) *core.Worktree {
	return &core.Worktree{
		ID: id, Name: name, Path: "/wt/" + id, Branch: branch, BaseBranch: "main",
		ProjectID: storetest.SeedProjectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}
}

// TestWorktreeQueries ports worktrees.sql and MergeWorktreeCleanupMetadata.
// Archived rows (deleted_at set) must stay addressable everywhere the original
// queries addressed them, despite forge projecting deleted_at as soft delete.
func TestWorktreeQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	// Written by the store, read by the Repository, every column round-tripped.
	a := newWorktree("wt-a", "alpha", "feat/a", base)
	a.BaseBranches = map[string]string{"repo-1": "develop"}
	a.ChatID, a.DaemonID, a.IdempotencyKey = strp("chat-1"), strp("daemon-1"), strp("key-a")
	a.UpdatedAt = base.Add(time.Second)
	if err := store.InsertWorktree(ctx, env.ORM, a); err != nil {
		t.Fatalf("InsertWorktree: %v", err)
	}
	twin, err := env.Repo.GetWorktree(ctx, "wt-a")
	if err != nil || twin.BaseBranches["repo-1"] != "develop" || *twin.ChatID != "chat-1" || *twin.DaemonID != "daemon-1" ||
		twin.Status != a.Status || !twin.UpdatedAt.Equal(base.Add(time.Second)) || twin.CleanupMetadata != nil {
		t.Fatalf("Repo.GetWorktree = %+v, %v", twin, err)
	}
	// A second live row with the name is ErrWorktreeNameTaken.
	if err := store.InsertWorktree(ctx, env.ORM, newWorktree("wt-dup", "alpha", "x", base)); !errors.Is(err, core.ErrWorktreeNameTaken) {
		t.Fatalf("InsertWorktree(dup name) err = %v, want ErrWorktreeNameTaken", err)
	}

	// Written by the Repository: a main row, a branch sibling, a CREATING row.
	main := newWorktree("wt-main", "main", "main", base.Add(-time.Minute))
	main.IsMain = true
	b := newWorktree("wt-b", "beta", "feat/a", base.Add(time.Minute))
	creating := newWorktree("wt-c", "gamma", "feat/c", base.Add(2*time.Minute))
	creating.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING)
	for _, w := range []*core.Worktree{main, b, creating} {
		if err := env.Repo.CreateWorktree(ctx, w); err != nil {
			t.Fatalf("Repo.CreateWorktree(%s): %v", w.ID, err)
		}
	}

	// Archive b: every by-id/by-key read still sees it.
	if err := store.ArchiveWorktree(ctx, env.ORM, "wt-b"); err != nil {
		t.Fatalf("ArchiveWorktree: %v", err)
	}
	gotB, err := store.GetWorktree(ctx, env.ORM, "wt-b")
	if err != nil || gotB.DeletedAt == nil {
		t.Fatalf("GetWorktree(archived) = %+v, %v", gotB, err)
	}
	if twinB, err := env.Repo.GetWorktree(ctx, "wt-b"); err != nil || twinB.DeletedAt == nil {
		t.Fatalf("Repo.GetWorktree(archived) = %+v, %v", twinB, err)
	}
	if _, err := store.GetWorktree(ctx, env.ORM, "wt-none"); !errors.Is(err, core.ErrWorktreeNotFound) {
		t.Fatalf("GetWorktree(missing) err = %v", err)
	}
	if w, err := store.GetWorktreeByPath(ctx, env.ORM, "/wt/wt-b"); err != nil || w.ID != "wt-b" {
		t.Fatalf("GetWorktreeByPath(archived) = %v, %v", w, err)
	}
	if _, err := store.GetWorktreeByPath(ctx, env.ORM, "/wt/none"); err == nil {
		t.Fatal("GetWorktreeByPath(missing) = nil error")
	}
	if w, err := store.GetWorktreeByIdempotencyKey(ctx, env.ORM, storetest.SeedProjectID, "key-a"); err != nil || w == nil || w.ID != "wt-a" {
		t.Fatalf("GetWorktreeByIdempotencyKey = %v, %v", w, err)
	}
	if w, err := store.GetWorktreeByIdempotencyKey(ctx, env.ORM, "other-project", "key-a"); err != nil || w != nil {
		t.Fatalf("GetWorktreeByIdempotencyKey(other project) = %v, %v", w, err)
	}
	if w, err := store.GetLiveWorktreeByName(ctx, env.ORM, storetest.SeedProjectID, "beta"); err != nil || w != nil {
		t.Fatalf("GetLiveWorktreeByName(archived) = %v, %v; want nil", w, err)
	}
	if w, err := store.GetLiveWorktreeByName(ctx, env.ORM, storetest.SeedProjectID, "alpha"); err != nil || w == nil || w.ID != "wt-a" {
		t.Fatalf("GetLiveWorktreeByName = %v, %v", w, err)
	}
	if ws, err := store.ListWorktreesByBranch(ctx, env.ORM, storetest.SeedProjectID, "feat/a"); err != nil || len(ws) != 2 {
		t.Fatalf("ListWorktreesByBranch = %v, %v; want a and archived b", ws, err)
	}
	if ws, err := store.ListWorktreesByIDs(ctx, env.ORM, []string{"wt-a", "wt-b", "wt-none"}); err != nil || len(ws) != 2 {
		t.Fatalf("ListWorktreesByIDs = %v, %v", ws, err)
	}
	if ws, err := store.ListWorktreesByIDs(ctx, env.ORM, nil); err != nil || ws != nil {
		t.Fatalf("ListWorktreesByIDs(nil) = %v, %v", ws, err)
	}

	// ListWorktrees, against its twin for every filter shape.
	ids := func(ws []*core.Worktree) []string {
		out := make([]string, len(ws))
		for i, w := range ws {
			out[i] = w.ID
		}
		return out
	}
	pid := storetest.SeedProjectID
	active := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	for name, f := range map[string]core.WorktreeFilters{
		"live":     {ProjectID: &pid},
		"all":      {ProjectID: &pid, IncludeArchived: true},
		"archived": {ProjectID: &pid, ArchivedOnly: true, IncludeArchived: true},
		"main":     {ProjectID: &pid, MainOnly: true},
		"status":   {Status: &active},
		"chat":     {ChatID: strp("chat-1")},
		"page":     {ProjectID: &pid, IncludeArchived: true, Limit: 2, Offset: 1},
	} {
		got, err := store.ListWorktrees(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListWorktrees(%s): %v", name, err)
		}
		want, err := env.Repo.ListWorktrees(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListWorktrees(%s): %v", name, err)
		}
		if g, w := ids(got), ids(want); len(g) != len(w) || len(g) == 0 {
			t.Fatalf("ListWorktrees(%s) = %v, twin %v", name, g, w)
		} else {
			for i := range g {
				if g[i] != w[i] {
					t.Fatalf("ListWorktrees(%s) = %v, twin %v", name, g, w)
				}
			}
		}
	}
	if all, _ := store.ListWorktrees(ctx, env.ORM, core.WorktreeFilters{ProjectID: &pid, IncludeArchived: true}); ids(all)[0] != "wt-main" || ids(all)[len(all)-1] != "wt-b" {
		t.Fatalf("ListWorktrees(all) order = %v, want main first and archived last", ids(all))
	}

	// Replace an archived row: path and failure reason land.
	gotB.Path, gotB.FailureReason, gotB.Status = "/wt/moved", "disk full", int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
	gotB.BaseBranches = nil
	if err := store.ReplaceWorktree(ctx, env.ORM, gotB); err != nil {
		t.Fatalf("ReplaceWorktree: %v", err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-b"); w.Path != "/wt/moved" || w.FailureReason != "disk full" || w.Status != gotB.Status || w.DeletedAt == nil {
		t.Fatalf("after ReplaceWorktree = %+v", w)
	}

	// Cleanup metadata: set, merge under the row lock, clear.
	if err := store.SetWorktreeCleanupMetadata(ctx, env.ORM, "wt-b", &core.CleanupMetadata{HeldReason: "dirty"}); err != nil {
		t.Fatalf("SetWorktreeCleanupMetadata: %v", err)
	}
	var sawArchived bool
	if err := store.MergeWorktreeCleanupMetadata(ctx, env.ORM, "wt-b", func(m *core.CleanupMetadata, archived bool) bool {
		sawArchived = archived
		m.DeleteBranch = true
		return m.HeldReason == "dirty"
	}); err != nil {
		t.Fatalf("MergeWorktreeCleanupMetadata: %v", err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-b"); !sawArchived || w.CleanupMetadata == nil || !w.CleanupMetadata.DeleteBranch || w.CleanupMetadata.HeldReason != "dirty" {
		t.Fatalf("after merge = %+v (archived %v)", w.CleanupMetadata, sawArchived)
	}
	if err := store.MergeWorktreeCleanupMetadata(ctx, env.ORM, "wt-b", func(m *core.CleanupMetadata, _ bool) bool {
		m.HeldReason = "ignored"
		return false
	}); err != nil {
		t.Fatalf("MergeWorktreeCleanupMetadata(no write): %v", err)
	}
	if w, _ := store.GetWorktree(ctx, env.ORM, "wt-b"); w.CleanupMetadata.HeldReason != "dirty" {
		t.Fatalf("a declined merge wrote: %+v", w.CleanupMetadata)
	}
	if err := store.MergeWorktreeCleanupMetadata(ctx, env.ORM, "wt-none", func(*core.CleanupMetadata, bool) bool { return true }); err == nil {
		t.Fatal("MergeWorktreeCleanupMetadata(missing) = nil error")
	}

	// Reclaim and held listings: b is archived and held (its status is now
	// FAILED, so reclaim skips it until restored); CREATING and main never list.
	gotB.Status = active
	if err := store.ReplaceWorktree(ctx, env.ORM, gotB); err != nil {
		t.Fatalf("ReplaceWorktree(restore status): %v", err)
	}
	reclaim, err := store.ListWorktreesForReclaim(ctx, env.ORM)
	if err != nil || len(reclaim) != 2 || reclaim[0].Worktree.ID != "wt-b" || reclaim[1].Worktree.ID != "wt-a" ||
		reclaim[0].OwnerUserID != storetest.SeedUserID || reclaim[0].ProjectPath == "" || reclaim[0].Worktree.CleanupMetadata == nil {
		t.Fatalf("ListWorktreesForReclaim = %+v, %v", reclaim, err)
	}
	twinReclaim, err := env.Repo.ListWorktreesForReclaim(ctx)
	if err != nil || len(twinReclaim) != len(reclaim) || twinReclaim[0].Worktree.ID != reclaim[0].Worktree.ID {
		t.Fatalf("Repo.ListWorktreesForReclaim = %+v, %v", twinReclaim, err)
	}
	held, err := store.ListHeldWorktreesForUser(ctx, env.ORM, storetest.SeedUserID)
	if err != nil || len(held) != 1 || held[0].Worktree.ID != "wt-b" || held[0].ProjectName == "" {
		t.Fatalf("ListHeldWorktreesForUser = %+v, %v", held, err)
	}
	if err := store.SetWorktreeCleanupMetadata(ctx, env.ORM, "wt-b", &core.CleanupMetadata{HeldReason: "dirty", DirectoryDeleted: true}); err != nil {
		t.Fatalf("SetWorktreeCleanupMetadata: %v", err)
	}
	if held, _ := store.ListHeldWorktreesForUser(ctx, env.ORM, storetest.SeedUserID); len(held) != 0 {
		t.Fatalf("a removed directory is still held: %+v", held)
	}
	if reclaim, _ := store.ListWorktreesForReclaim(ctx, env.ORM); len(reclaim) != 1 {
		t.Fatalf("a removed directory is still reclaimable: %+v", reclaim)
	}
	if err := store.SetWorktreeCleanupMetadata(ctx, env.ORM, "wt-b", nil); err != nil {
		t.Fatalf("SetWorktreeCleanupMetadata(nil): %v", err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-b"); w.CleanupMetadata != nil {
		t.Fatalf("cleanup metadata not cleared: %+v", w.CleanupMetadata)
	}

	// Adopt fills daemon_id once, and every caller learns the persisted
	// winner: a second resolver nominating another machine is told the first.
	if got, err := store.AdoptWorktreeDaemon(ctx, env.ORM, "wt-b", "daemon-2"); err != nil || got != "daemon-2" {
		t.Fatalf("AdoptWorktreeDaemon = %q, %v; want daemon-2", got, err)
	}
	if got, err := store.AdoptWorktreeDaemon(ctx, env.ORM, "wt-b", "daemon-3"); err != nil || got != "daemon-2" {
		t.Fatalf("AdoptWorktreeDaemon(again) = %q, %v; want the recorded daemon-2", got, err)
	}
	if got, err := store.AdoptWorktreeDaemon(ctx, env.ORM, "wt-missing", "daemon-3"); err != nil || got != "" {
		t.Fatalf("AdoptWorktreeDaemon(missing row) = %q, %v; want \"\"", got, err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-b"); w.DaemonID == nil || *w.DaemonID != "daemon-2" {
		t.Fatalf("after adopt = %v", w.DaemonID)
	}

	// Live paths: archived b is not one.
	paths, err := store.ListLiveWorktreePathsForUser(ctx, env.ORM, storetest.SeedUserID)
	if err != nil || len(paths) != 3 {
		t.Fatalf("ListLiveWorktreePathsForUser = %+v, %v; want a, main, creating", paths, err)
	}
	for _, p := range paths {
		if p.ID == "wt-b" || p.ProjectID != storetest.SeedProjectID || p.Path == "" {
			t.Fatalf("ListLiveWorktreePathsForUser row = %+v", p)
		}
	}
	if paths, _ := store.ListLiveWorktreePathsForUser(ctx, env.ORM, "other-user"); len(paths) != 0 {
		t.Fatalf("another user's paths = %+v", paths)
	}

	// Unarchive: refused while a live row holds the name, then allowed.
	if err := store.InsertWorktree(ctx, env.ORM, newWorktree("wt-b2", "beta", "feat/b2", base)); err != nil {
		t.Fatalf("InsertWorktree(beta again): %v", err)
	}
	if err := store.UnarchiveWorktree(ctx, env.ORM, "wt-b"); !errors.Is(err, core.ErrWorktreeNameTaken) {
		t.Fatalf("UnarchiveWorktree(name taken) err = %v", err)
	}
	if err := store.RemoveWorktree(ctx, env.ORM, "wt-b2"); err != nil {
		t.Fatalf("RemoveWorktree(live): %v", err)
	}
	if err := store.UnarchiveWorktree(ctx, env.ORM, "wt-b"); err != nil {
		t.Fatalf("UnarchiveWorktree: %v", err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-b"); w.DeletedAt != nil {
		t.Fatalf("still archived after unarchive: %v", w.DeletedAt)
	}

	// RemoveWorktree hard-deletes an archived row too.
	if err := store.ArchiveWorktree(ctx, env.ORM, "wt-b"); err != nil {
		t.Fatalf("ArchiveWorktree: %v", err)
	}
	if err := store.RemoveWorktree(ctx, env.ORM, "wt-b"); err != nil {
		t.Fatalf("RemoveWorktree(archived): %v", err)
	}
	if err := store.RemoveWorktree(ctx, env.ORM, "wt-b"); err != nil {
		t.Fatalf("RemoveWorktree(missing): %v", err)
	}
	if _, err := env.Repo.GetWorktree(ctx, "wt-b"); !errors.Is(err, core.ErrWorktreeNotFound) {
		t.Fatalf("archived row survived RemoveWorktree: %v", err)
	}
	var n int
	if err := env.ORM.QueryRow(ctx, `SELECT count(*) FROM worktrees WHERE id = 'wt-b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("wt-b rows = %d, %v; want a hard delete", n, err)
	}
}
