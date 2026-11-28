// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// A project is unique per (org, user, path) and (org, user, remote_url): the
// same folder, or repository, may be a project in two orgs, but not twice in
// one, and rows with no org yet are one bucket of their own, unique among
// themselves as every row was before orgs. The scoped lookups pick each
// org's own.
func TestProjectKeys_ArePerOrg(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	user := storetest.SeedUserID
	remote := "https://github.com/acme/props.git"
	project := func(id, path string, org *string, remote *string) *core.Project {
		return &core.Project{ID: id, UserID: user, Name: id, Path: path, OrgID: org, RemoteURL: remote,
			CreatedAt: at, UpdatedAt: at, LastActive: at}
	}
	create := func(p *core.Project) error { return env.Repo.CreateProject(ctx, p) }

	if err := create(project("p-acme", "/home/workspace/projects/props", orgp("acme"), &remote)); err != nil {
		t.Fatalf("acme's project: %v", err)
	}
	// The same path and repository in two orgs: both are projects.
	if err := create(project("p-globex", "/home/workspace/projects/props", orgp("globex"), &remote)); err != nil {
		t.Fatalf("the same path and repository in another org must be creatable: %v", err)
	}
	// And once with no org yet: its own bucket.
	if err := create(project("p-none", "/home/workspace/projects/props", nil, &remote)); err != nil {
		t.Fatalf("the same path with no org yet: %v", err)
	}

	// Twice in one org, or twice with no org: refused, by the new keys.
	for name, tc := range map[string]struct {
		p    *core.Project
		want string
	}{
		"same org, same path":       {project("p-acme-2", "/home/workspace/projects/props", orgp("acme"), nil), store.ProjectConstraintOrgUserPathKey},
		"same org, same repository": {project("p-acme-3", "/home/workspace/projects/other", orgp("acme"), &remote), store.ProjectConstraintOrgUserRemoteURLKey},
		"no org, same path":         {project("p-none-2", "/home/workspace/projects/props", nil, nil), store.ProjectConstraintOrgUserPathKey},
	} {
		err := create(tc.p)
		if err == nil {
			t.Fatalf("%s: created a duplicate", name)
		}
		if got := orm.ConstraintName(err); got != tc.want {
			t.Fatalf("%s: violated %q (%v), want %q", name, got, err, tc.want)
		}
	}

	// A lookup in one org finds that org's project; with no scope, the
	// oldest of them, deterministically.
	got, err := store.GetProjectByPathAndUserInScope(ctx, env.Repo.ORM(), "/home/workspace/projects/props", user, &core.OrgScope{OrgID: "globex"})
	if err != nil || got.ID != "p-globex" {
		t.Fatalf("in globex: %v, %v; want p-globex", got, err)
	}
	got, err = store.GetProjectByRemoteURLAndUserInScope(ctx, env.Repo.ORM(), remote, user, &core.OrgScope{OrgID: "acme"})
	if err != nil || got.ID != "p-acme" {
		t.Fatalf("acme's repository: %v, %v; want p-acme", got, err)
	}
	if _, err := store.GetProjectByPathAndUserInScope(ctx, env.Repo.ORM(), "/home/workspace/projects/props", user, &core.OrgScope{OrgID: "initech"}); err == nil {
		t.Fatal("found a project in an org that has none at the path")
	}
	got, err = store.GetProjectByPathAndUserInScope(ctx, env.Repo.ORM(), "/home/workspace/projects/props", user, &core.OrgScope{OrgID: "initech", IncludeUnassigned: true})
	if err != nil || got.ID != "p-none" {
		t.Fatalf("the default org's scope includes the unassigned row: %v, %v; want p-none", got, err)
	}
}

// The lazy backfill leaves unassigned a project whose path or repository the
// default org already holds, rather than stamp it into a collision (which
// would fail the whole batch), and still stamps the rest.
func TestAssignUnassignedOrg_SkipsAProjectTheDefaultOrgAlreadyHolds(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	user := storetest.SeedUserID
	remote := "https://github.com/acme/props.git"
	for _, p := range []*core.Project{
		{ID: "p-home", UserID: user, Name: "props", Path: "/srv/props", OrgID: orgp("home"), RemoteURL: &remote},
		{ID: "p-dup-path", UserID: user, Name: "props", Path: "/srv/props"},
		{ID: "p-dup-remote", UserID: user, Name: "props2", Path: "/srv/props2", RemoteURL: &remote},
		{ID: "p-free", UserID: user, Name: "free", Path: "/srv/free"},
	} {
		p.CreatedAt, p.UpdatedAt, p.LastActive = at, at, at
		if err := env.Repo.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", p.ID, err)
		}
	}

	for range 2 { // idempotent: the second pass changes nothing
		if err := env.Repo.AssignUnassignedOrg(ctx, user, "home"); err != nil {
			t.Fatalf("AssignUnassignedOrg: %v", err)
		}
	}
	for id, want := range map[string]*string{"p-home": orgp("home"), "p-dup-path": nil, "p-dup-remote": nil, "p-free": orgp("home")} {
		got, err := env.Repo.GetProject(ctx, id)
		if err != nil {
			t.Fatalf("GetProject(%s): %v", id, err)
		}
		if (got.OrgID == nil) != (want == nil) || (want != nil && *got.OrgID != *want) {
			t.Fatalf("%s org = %v, want %v", id, got.OrgID, want)
		}
	}
}
