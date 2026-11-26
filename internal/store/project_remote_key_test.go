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

// git_remote_key, the one definition of which remotes name one repository.
// On github.com, gitlab.com and bitbucket.org every form of a repository's
// remote reduces to one key; elsewhere a key equates only what is the same
// server and path (default ports, trailing slashes, the scp-like form and its
// ssh:// spelling), so case-sensitive paths, other ports, transports and ssh
// users stay apart; and a remote that names no server has no key.
func TestGitRemoteKey(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	const props = "github.com/acme/props"
	for remote, want := range map[string]string{
		// Hosting providers: one repository, every form.
		"https://github.com/acme/props.git":                       props,
		"https://github.com/acme/props":                           props,
		"https://github.com/acme/props/":                          props,
		"https://github.com/acme/props.git/":                      props,
		"HTTPS://GitHub.com/Acme/Props.git":                       props,
		"  https://github.com/acme/props.git\n":                   props,
		"https://x-access-token:s3cr3t@github.com/acme/props.git": props,
		"https://github.com:443/acme/props":                       props,
		"http://github.com/acme/props":                            props,
		"git@github.com:acme/props.git":                           props,
		"git@github.com:Acme/Props":                               props,
		"github.com:acme/props.git":                               props,
		"git@github.com:/acme/props.git":                          props,
		"ssh://git@github.com/acme/props.git":                     props,
		"ssh://git@github.com:22/acme/props.git":                  props,
		"ssh://git:pw@github.com/acme/props":                      props,
		"git+ssh://git@github.com/acme/props.git":                 props,
		"git://github.com/acme/props.git":                         props,
		"https://gitlab.com/Grp/Sub/props":                        "gitlab.com/grp/sub/props",
		"git@gitlab.com:grp/sub/props.git":                        "gitlab.com/grp/sub/props",
		"git@bitbucket.org:team/props.git":                        "bitbucket.org/team/props",
		// Another port of a provider's host is another server.
		"https://github.com:8443/acme/props": "https://github.com:8443/acme/props",
		// Any other host: the same server and path only.
		"https://git.example.com/Team/Props.git":   "https://git.example.com/Team/Props.git",
		"https://GIT.example.com/Team/Props.git/":  "https://git.example.com/Team/Props.git",
		"https://user:tok@git.example.com:443/x/y": "https://git.example.com/x/y",
		"https://git.example.com:8443/x/y":         "https://git.example.com:8443/x/y",
		"http://git.example.com/x/y":               "http://git.example.com/x/y",
		"https://git.example.com/props":            "https://git.example.com/props",
		"ssh://git@10.0.0.5:2222/srv/props.git":    "ssh://git@10.0.0.5:2222/srv/props.git",
		"ssh://git@10.0.0.5:22/srv/props.git":      "ssh://git@10.0.0.5/srv/props.git",
		"git@10.0.0.5:/srv/props.git":              "ssh://git@10.0.0.5/srv/props.git",
		"git@github-work:acme/props.git":           "ssh://git@github-work/~/acme/props.git", // an ssh Host alias
		"ssh://git@github-work/~/acme/props.git":   "ssh://git@github-work/~/acme/props.git",
		"alice@host.example:props":                 "ssh://alice@host.example/~/props",
		"bob@host.example:props":                   "ssh://bob@host.example/~/props",
		"host.example:~alice/props":                "ssh://host.example/~alice/props",
		"ssh://[2001:db8::1]:2222/srv/props":       "ssh://[2001:db8::1]:2222/srv/props",
		"git://git.example.com/props.git":          "git://git.example.com/props.git",
		// No server: no key.
		"repos/props":                   "",
		"repos/props.git":               "",
		"/home/sean/src/props":          "",
		"/home/sean/src/props.git":      "",
		"file:///home/sean/src/props":   "",
		"../props":                      "",
		"./props":                       "",
		`C:\src\props`:                  "",
		"C:/src/props":                  "",
		`\\server\share\props`:          "",
		"props":                         "",
		"https://github.com":            "",
		"https://github.com/acme":       "",
		"https://git.example.com/":      "",
		"https://git.example.com:x/a/b": "",
		"https://git.example.com/a//b":  "",
		"ftp://git.example.com/a/b":     "",
		"https://git.example.com/a?b=c": "",
		"":                              "",
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
	t.Parallel()
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
