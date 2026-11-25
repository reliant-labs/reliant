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

// nestedFixtureTime is every fixture row's timestamp: nothing here depends
// on the wall clock (leases are moved with SQL against the database's own).
var nestedFixtureTime = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// nestedFixture is a project with nested repositories forge/, forge/plugin/
// and web/, set up on machine d1.
type nestedFixture struct {
	env                *storetest.Env
	ctx                context.Context
	project            *core.Project
	forge, plugin, web *core.Repo
}

func newNestedFixture(t *testing.T) *nestedFixture {
	t.Helper()
	env := storetest.New(t)
	ctx := context.Background()
	now := nestedFixtureTime
	remote := "git@github.com:acme/forge.git"
	p := &core.Project{ID: uuid.NewString(), Name: "acme", Path: "/src/acme-" + uuid.NewString(), UserID: "u-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	if err := env.Repo.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	repo := func(name, rel string) *core.Repo {
		r := &core.Repo{ID: uuid.NewString(), ProjectID: p.ID, Name: name, RelativePath: rel, RemoteURL: &remote, CreatedAt: now, UpdatedAt: now}
		if err := env.Repo.CreateRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	f := &nestedFixture{env: env, ctx: ctx, project: p, forge: repo("forge", "forge"), plugin: repo("plugin", "forge/plugin"), web: repo("web", "web")}
	if err := env.Repo.UpsertQueuedProjectDaemon(ctx, p.ID, "d1", "/srv/acme", nil, "clone:"+p.ID, []string{f.forge.ID, f.plugin.ID, f.web.ID}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nestedFixture) claim(t *testing.T, claimID string) *core.NestedRepoClaim {
	t.Helper()
	c, err := store.ClaimNextProjectDaemonRepo(f.ctx, f.env.ORM, f.project.ID, "d1", claimID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *nestedFixture) claimedName(t *testing.T, claimID string) string {
	t.Helper()
	if c := f.claim(t, claimID); c != nil {
		return c.Repo.Name
	}
	return ""
}

// expireLease runs the repository's lease out, by the database's clock.
func (f *nestedFixture) expireLease(t *testing.T, repoID string) {
	t.Helper()
	if _, err := f.env.ORM.Bun().NewRaw(`UPDATE project_daemon_repos SET lease_until = now() - interval '1 second'
WHERE project_id = ? AND daemon_id = 'd1' AND repo_id = ?`, f.project.ID, repoID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *nestedFixture) settle(t *testing.T, repoID, claimID, installErr string) bool {
	t.Helper()
	ok, err := store.SettleProjectDaemonRepo(f.ctx, f.env.ORM, f.project.ID, "d1", repoID, claimID, installErr)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (f *nestedFixture) installState(t *testing.T) (core.ProjectInstallState, string) {
	t.Helper()
	rows, err := store.ListProjectDaemonsForProject(f.ctx, f.env.ORM, f.project.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("checkout rows = %+v, %v", rows, err)
	}
	return rows[0].InstallState, rows[0].InstallError
}

// TestNestedRepoClones walks project_daemon_repos through a set-up: planned
// with the root clone, claimed one at a time (shallowest first, never inside
// a repository still to clone), settled under the claim, read back, and
// planned again.
func TestNestedRepoClones(t *testing.T) {
	f := newNestedFixture(t)
	ids, err := store.ListProjectsWithUnsettledDaemonRepos(f.ctx, f.env.ORM, "d1")
	if err != nil || len(ids) != 1 || ids[0] != f.project.ID {
		t.Fatalf("unsettled projects = %v, %v; want [%s]", ids, err, f.project.ID)
	}

	// One at a time, shallowest first. forge/plugin is not ready while
	// forge, the repository it sits in, is still to clone.
	first := f.claim(t, "pass-a")
	if first == nil || first.Repo.Name != "forge" || first.Repo.State != core.NestedRepoCloning || first.Repo.LeaseUntil == nil || first.InsideFailed {
		t.Fatalf("first claim = %+v", first)
	}
	if got := f.claimedName(t, "pass-b"); got != "web" {
		t.Fatalf("second claim = %q, want web (plugin waits for forge)", got)
	}
	if got := f.claimedName(t, "pass-b"); got != "" {
		t.Fatalf("third claim = %q, want none while forge is cloning", got)
	}

	// Only the claim that holds a row settles it.
	if f.settle(t, f.forge.ID, "pass-b", "") {
		t.Fatal("pass-b settled forge, which pass-a holds")
	}
	if !f.settle(t, f.forge.ID, "pass-a", "") {
		t.Fatal("pass-a could not settle its own claim")
	}
	if got := f.claimedName(t, "pass-a"); got != "plugin" {
		t.Fatalf("claim after forge landed = %q, want plugin", got)
	}
	if !f.settle(t, f.plugin.ID, "pass-a", "") || !f.settle(t, f.web.ID, "pass-b", "git clone failed: denied") {
		t.Fatal("settle refused")
	}
	rows, err := store.ListProjectDaemonReposForPair(f.ctx, f.env.ORM, f.project.ID, "d1")
	if err != nil || len(rows) != 3 || rows[0].State != core.NestedRepoInstalled || rows[2].State != core.NestedRepoFailed || rows[2].Error != "git clone failed: denied" || rows[2].LeaseUntil != nil {
		t.Fatalf("settled = %+v, %v", rows, err)
	}
	if ids, _ := store.ListProjectsWithUnsettledDaemonRepos(f.ctx, f.env.ORM, "d1"); len(ids) != 0 {
		t.Fatalf("unsettled after settling = %v", ids)
	}

	// Set up again with only forge: the other rows go, forge is pending again.
	if err := f.env.Repo.UpsertQueuedProjectDaemon(f.ctx, f.project.ID, "d1", "/srv/acme", nil, "clone:"+f.project.ID, []string{f.forge.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListProjectDaemonReposForProjects(f.ctx, f.env.ORM, []string{f.project.ID})
	if err != nil || len(rows) != 1 || rows[0].RepoID != f.forge.ID || rows[0].State != core.NestedRepoPending {
		t.Fatalf("replanned = %+v, %v", rows, err)
	}

	// The checkout's row going (a detach) takes them with it.
	if err := f.env.Repo.DeleteProjectDaemon(f.ctx, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := store.ListProjectDaemonReposForPair(f.ctx, f.env.ORM, f.project.ID, "d1"); len(rows) != 0 {
		t.Fatalf("rows after the checkout went = %+v", rows)
	}
}

// A claim is taken over only once its lease ran out, and the pass it was
// taken from then records nothing; planning the set-up again drops every
// claim, so the old plan's pass records nothing either.
func TestNestedRepoClaimsAreLeasedAndFenced(t *testing.T) {
	f := newNestedFixture(t)
	if got := f.claimedName(t, "gone"); got != "forge" {
		t.Fatalf("claim = %q", got)
	}
	f.claimedName(t, "gone") // web
	if got := f.claimedName(t, "next"); got != "" {
		t.Fatalf("claimed %q while every lease holds", got)
	}

	f.expireLease(t, f.forge.ID)
	if got := f.claimedName(t, "next"); got != "forge" {
		t.Fatalf("claim after forge's lease ran out = %q, want forge", got)
	}
	if f.settle(t, f.forge.ID, "gone", "git clone failed: connection reset") {
		t.Fatal("the pass whose claim was taken over recorded its outcome")
	}
	if !f.settle(t, f.forge.ID, "next", "") {
		t.Fatal("the pass holding the claim could not record it")
	}

	// Planned again while "gone" still holds web: its settle is refused.
	if err := f.env.Repo.UpsertQueuedProjectDaemon(f.ctx, f.project.ID, "d1", "/srv/acme", nil, "clone:"+f.project.ID, []string{f.forge.ID, f.plugin.ID, f.web.ID}); err != nil {
		t.Fatal(err)
	}
	if f.settle(t, f.web.ID, "gone", "") {
		t.Fatal("a claim of the previous plan recorded an outcome on the new one")
	}
}

// A repository inside one that failed is claimed flagged, for the claimer to
// settle failed without cloning it.
func TestNestedRepoClaimInsideAFailedRepository(t *testing.T) {
	f := newNestedFixture(t)
	f.claimedName(t, "p") // forge
	if !f.settle(t, f.forge.ID, "p", "denied") {
		t.Fatal("settle refused")
	}
	for {
		c := f.claim(t, "p")
		if c == nil {
			t.Fatal("plugin was never claimed")
		}
		if c.Repo.Name == "plugin" {
			if !c.InsideFailed {
				t.Fatal("plugin, inside forge which failed, was not flagged")
			}
			return
		}
		if c.InsideFailed {
			t.Fatalf("%s was flagged inside a failed repository", c.Repo.Name)
		}
		f.settle(t, c.Repo.RepoID, "p", "")
	}
}

// The checkout settles from its nested repositories: never installed while
// one is still to clone or one failed, failed from installed too (an older
// gateway settled the root alone), and never from an outcome of a plan made
// again since.
func TestSettleProjectDaemonFromNestedRepos(t *testing.T) {
	f := newNestedFixture(t)
	settleAll := func(failWeb bool) {
		for c := f.claim(t, "p"); c != nil; c = f.claim(t, "p") {
			installErr := ""
			if failWeb && c.Repo.Name == "web" {
				installErr = "denied"
			}
			f.settle(t, c.Repo.RepoID, "p", installErr)
		}
	}

	// Still to clone: neither settle changes anything.
	if err := store.MarkProjectDaemonInstalledFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if failed, err := store.FailProjectDaemonFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1", "x"); err != nil || failed {
		t.Fatalf("failed with repositories still to clone: %v, %v", failed, err)
	}
	if state, _ := f.installState(t); state != core.ProjectInstallInstalling {
		t.Fatalf("state = %s, want installing", state)
	}

	// An older gateway marked the root installed; web then fails.
	if err := store.MarkProjectDaemonInstalled(f.ctx, f.env.ORM, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	settleAll(true)
	if err := store.MarkProjectDaemonInstalledFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if failed, err := store.FailProjectDaemonFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1", "web did not clone"); err != nil || !failed {
		t.Fatalf("an installed root with a failed nested repository was not failed: %v, %v", failed, err)
	}
	if state, reason := f.installState(t); state != core.ProjectInstallFailed || reason != "web did not clone" {
		t.Fatalf("state = %s %q, want failed naming web", state, reason)
	}
	// Repeated (a duplicate announcement): installed it never becomes.
	if err := store.MarkProjectDaemonInstalledFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.installState(t); state != core.ProjectInstallFailed {
		t.Fatalf("state after a repeated settle = %s, want failed", state)
	}

	// Planned again: the old plan's failure fails nothing.
	if err := f.env.Repo.UpsertQueuedProjectDaemon(f.ctx, f.project.ID, "d1", "/srv/acme", nil, "clone:"+f.project.ID, []string{f.forge.ID, f.plugin.ID, f.web.ID}); err != nil {
		t.Fatal(err)
	}
	if failed, _ := store.FailProjectDaemonFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1", "stale"); failed {
		t.Fatal("the previous plan's failure failed the new set-up")
	}
	settleAll(false)
	if err := store.MarkProjectDaemonInstalledFromNestedRepos(f.ctx, f.env.ORM, f.project.ID, "d1"); err != nil {
		t.Fatal(err)
	}
	if state, reason := f.installState(t); state != core.ProjectInstallInstalled || reason != "" {
		t.Fatalf("state = %s %q, want installed", state, reason)
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
