// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestNestedRepoClones walks project_daemon_repos through a set-up: planned
// with the root clone, claimed once, settled, read back, and planned again.
func TestNestedRepoClones(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user := "u-" + uuid.NewString()
	remote := "git@github.com:acme/forge.git"
	p := &core.Project{ID: uuid.NewString(), Name: "acme", Path: "/src/acme-" + uuid.NewString(), UserID: user, CreatedAt: now, UpdatedAt: now, LastActive: now}
	if err := env.Repo.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	forge := &core.Repo{ID: uuid.NewString(), ProjectID: p.ID, Name: "forge", RelativePath: "forge", RemoteURL: &remote, CreatedAt: now, UpdatedAt: now}
	web := &core.Repo{ID: uuid.NewString(), ProjectID: p.ID, Name: "web", RelativePath: "web", RemoteURL: &remote, CreatedAt: now, UpdatedAt: now}
	for _, r := range []*core.Repo{forge, web} {
		if err := env.Repo.CreateRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Repo.UpsertQueuedProjectDaemon(ctx, p.ID, "d1", "/srv/acme", nil, "clone:"+p.ID, []string{forge.ID, web.ID}); err != nil {
		t.Fatal(err)
	}

	ids, err := store.ListProjectsWithUnsettledDaemonRepos(ctx, env.ORM, "d1")
	if err != nil || len(ids) != 1 || ids[0] != p.ID {
		t.Fatalf("unsettled projects = %v, %v; want [%s]", ids, err, p.ID)
	}
	claimed, err := store.ClaimProjectDaemonRepos(ctx, env.ORM, p.ID, "d1", now.Add(-time.Hour))
	if err != nil || len(claimed) != 2 || claimed[0].Name != "forge" || claimed[0].RemoteURL != remote || claimed[0].State != core.NestedRepoCloning {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	// A second gateway claims nothing while the claim is fresh.
	if again, err := store.ClaimProjectDaemonRepos(ctx, env.ORM, p.ID, "d1", time.Now().Add(-time.Hour)); err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, %v; want none", again, err)
	}
	// One whose gateway went away is taken again once the claim is stale.
	if stale, err := store.ClaimProjectDaemonRepos(ctx, env.ORM, p.ID, "d1", time.Now().Add(time.Minute)); err != nil || len(stale) != 2 {
		t.Fatalf("stale claim = %+v, %v; want both", stale, err)
	}

	if err := store.SettleProjectDaemonRepo(ctx, env.ORM, p.ID, "d1", forge.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleProjectDaemonRepo(ctx, env.ORM, p.ID, "d1", web.ID, "git clone failed: denied"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListProjectDaemonReposForPair(ctx, env.ORM, p.ID, "d1")
	if err != nil || len(rows) != 2 || rows[0].State != core.NestedRepoInstalled || rows[1].State != core.NestedRepoFailed || rows[1].Error != "git clone failed: denied" {
		t.Fatalf("settled = %+v, %v", rows, err)
	}
	if ids, _ := store.ListProjectsWithUnsettledDaemonRepos(ctx, env.ORM, "d1"); len(ids) != 0 {
		t.Fatalf("unsettled after settling = %v", ids)
	}

	// Set up again with only forge: web's row goes, forge is pending again.
	if err := env.Repo.UpsertQueuedProjectDaemon(ctx, p.ID, "d1", "/srv/acme", nil, "clone:"+p.ID, []string{forge.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListProjectDaemonReposForProjects(ctx, env.ORM, []string{p.ID})
	if err != nil || len(rows) != 1 || rows[0].RepoID != forge.ID || rows[0].State != core.NestedRepoPending {
		t.Fatalf("replanned = %+v, %v", rows, err)
	}

	// The checkout's row going (a detach) takes them with it.
	if err := env.Repo.DeleteProjectDaemon(ctx, p.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := store.ListProjectDaemonReposForPair(ctx, env.ORM, p.ID, "d1"); len(rows) != 0 {
		t.Fatalf("rows after the checkout went = %+v", rows)
	}
}

// TestGetProjectBySameRemoteAndUserInScope finds a project by any spelling of
// its GitHub remote, and only that repository.
func TestGetProjectBySameRemoteAndUserInScope(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user := "u-" + uuid.NewString()
	ssh := "git@github.com:Acme/shop.git"
	p := &core.Project{ID: uuid.NewString(), Name: "shop", Path: "/src/shop", UserID: user, RemoteURL: &ssh, CreatedAt: now, UpdatedAt: now, LastActive: now}
	if err := env.Repo.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	other := "https://github.com/acme/shop-api"
	q := &core.Project{ID: uuid.NewString(), Name: "shop-api", Path: "/src/shop-api", UserID: user, RemoteURL: &other, CreatedAt: now, UpdatedAt: now, LastActive: now}
	if err := env.Repo.CreateProject(ctx, q); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{
		"https://github.com/acme/shop", "https://github.com/acme/shop.git", "https://tok@GitHub.com/Acme/shop.git/",
		"ssh://git@github.com/acme/shop.git", "git@github.com:acme/shop",
	} {
		got, err := store.GetProjectBySameRemoteAndUserInScope(ctx, env.ORM, spelling, user, nil)
		if err != nil || got.ID != p.ID {
			t.Errorf("%s: got %+v, %v; want %s", spelling, got, err, p.ID)
		}
	}
	for _, miss := range []string{"https://github.com/acme/sho", "https://gitlab.com/acme/shop", "/local/shop"} {
		if got, err := store.GetProjectBySameRemoteAndUserInScope(ctx, env.ORM, miss, user, nil); err == nil {
			t.Errorf("%s matched %s", miss, got.ID)
		}
	}
	if _, err := store.GetProjectBySameRemoteAndUserInScope(ctx, env.ORM, "https://github.com/acme/shop", "someone-else", nil); err == nil {
		t.Error("another user's project matched")
	}
}
