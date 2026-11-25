// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// git_remote_key, the one definition of which remotes name one repository:
// every form a machine may report of github.com/acme/props reduces to the same
// key, and a remote that names no host has none.
func TestGitRemoteKey(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const props = "github.com/acme/props"
	for remote, want := range map[string]string{
		"https://github.com/acme/props.git":                       props,
		"https://github.com/acme/props":                           props,
		"https://github.com/acme/props/":                          props,
		"https://github.com/acme/props.git/":                      props,
		"HTTPS://GitHub.com/Acme/Props.git":                       props,
		"  https://github.com/acme/props.git\n":                   props,
		"https://x-access-token:s3cr3t@github.com/acme/props.git": props,
		"http://github.com:443/acme/props":                        props,
		"git@github.com:acme/props.git":                           props,
		"git@github.com:Acme/Props":                               props,
		"github.com:acme/props.git":                               props,
		"git@github.com:/acme/props.git":                          props,
		"ssh://git@github.com/acme/props.git":                     props,
		"ssh://git@github.com:22/acme/props.git":                  props,
		"ssh://git:pw@github.com/acme/props":                      props,
		"git+ssh://git@github.com/acme/props.git":                 props,
		"git://github.com/acme/props.git":                         props,
		"https://gitlab.example.com/grp/sub/props":                "gitlab.example.com/grp/sub/props",
		"git@gitlab.example.com:grp/sub/props.git":                "gitlab.example.com/grp/sub/props",
		"git@github-work:acme/props.git":                          "github-work/acme/props", // an ssh Host alias
		"ssh://git@10.0.0.5:2222/srv/props.git":                   "10.0.0.5/srv/props",
		// No host: no key.
		"/home/sean/src/props":        "",
		"/home/sean/src/props.git":    "",
		"file:///home/sean/src/props": "",
		"../props":                    "",
		"./props":                     "",
		`C:\src\props`:                "",
		"C:/src/props":                "",
		"props":                       "",
		"https://github.com":          "",
		"":                            "",
	} {
		var got sql.NullString
		if err := env.Repo.ORM().Bun().NewRaw("SELECT git_remote_key(?)", remote).Scan(ctx, &got); err != nil {
			t.Fatalf("git_remote_key(%q): %v", remote, err)
		}
		if got.String != want || got.Valid != (want != "") {
			t.Errorf("git_remote_key(%q) = %q (valid %v), want %q", remote, got.String, got.Valid, want)
		}
	}
}

// The lookup by repository finds the user's project of the repo whatever form
// of its remote either side holds, keeps to scope and owner, and finds
// nothing for a remote with no host. The column is the database's: a row
// written with a remote_url has its key with no help from the writer.
func TestGetProjectByRemoteKeyAndUserInScope(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	user := storetest.SeedUserID
	other := "other-user"
	https := "https://github.com/Acme/Props.git"
	local := "/srv/git/props"
	for _, p := range []*core.Project{
		{ID: "p-acme", UserID: user, Name: "props", Path: "/Users/sean/src/props", OrgID: orgp("acme"), RemoteURL: &https},
		{ID: "p-globex", UserID: user, Name: "props", Path: "/Users/sean/src/props", OrgID: orgp("globex"), RemoteURL: &https},
		{ID: "p-other-user", UserID: other, Name: "props", Path: "/Users/kim/props", OrgID: orgp("acme"), RemoteURL: &https},
		{ID: "p-local", UserID: user, Name: "local", Path: "/Users/sean/src/local", OrgID: orgp("acme"), RemoteURL: &local},
	} {
		p.CreatedAt, p.UpdatedAt, p.LastActive = at, at, at
		at = at.Add(time.Minute)
		if err := env.Repo.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", p.ID, err)
		}
	}

	got, err := env.Repo.GetProject(ctx, "p-acme")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.RemoteKey == nil || *got.RemoteKey != "github.com/acme/props" {
		t.Fatalf("remote_key = %v, want github.com/acme/props", got.RemoteKey)
	}
	if got, _ := env.Repo.GetProject(ctx, "p-local"); got.RemoteKey != nil {
		t.Fatalf("a local-path remote has key %q, want none", *got.RemoteKey)
	}

	acme := &core.OrgScope{OrgID: "acme"}
	for _, asked := range []string{"git@github.com:acme/props.git", "ssh://git@github.com/acme/props", https} {
		got, err := store.GetProjectByRemoteKeyAndUserInScope(ctx, env.Repo.ORM(), asked, user, acme)
		if err != nil || got.ID != "p-acme" {
			t.Fatalf("%s in acme: %v, %v; want p-acme", asked, got, err)
		}
	}
	// No scope: the oldest of the user's, never another user's.
	got, err = store.GetProjectByRemoteKeyAndUserInScope(ctx, env.Repo.ORM(), "git@github.com:acme/props.git", user, nil)
	if err != nil || got.ID != "p-acme" {
		t.Fatalf("any org: %v, %v; want p-acme", got, err)
	}
	got, err = store.GetProjectByRemoteKeyAndUserInScope(ctx, env.Repo.ORM(), "git@github.com:acme/props.git", other, nil)
	if err != nil || got.ID != "p-other-user" {
		t.Fatalf("the other user: %v, %v; want p-other-user", got, err)
	}
	if _, err := store.GetProjectByRemoteKeyAndUserInScope(ctx, env.Repo.ORM(), "git@github.com:acme/props.git", user, &core.OrgScope{OrgID: "initech"}); err == nil {
		t.Fatal("found a project in an org that has none of the repository")
	}
	// A remote naming no host matches nothing, not even its own raw value.
	if _, err := store.GetProjectByRemoteKeyAndUserInScope(ctx, env.Repo.ORM(), local, user, acme); err == nil {
		t.Fatal("a local-path remote matched by key")
	}
}
