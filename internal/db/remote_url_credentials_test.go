// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The token below is a fixture, not a credential.
const (
	leakedRemote = "https://x-access-token:ghs_FIXTURE0TOKEN@github.com/acme/app.git"
	cleanRemote  = "https://github.com/acme/app.git"
)

func rawRemote(t *testing.T, raw *sql.DB, table, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT remote_url FROM `+table+` WHERE id = $1`, id).Scan(&v)) //nolint:gosec // table is a literal from this file
	return v
}

func newRemoteProject(id, user, path string, remote *string) *core.Project {
	now := time.Now().UTC()
	return &core.Project{ID: id, UserID: user, Name: id, Path: path, IsGitRepo: true,
		RemoteURL: remote, CreatedAt: now, UpdatedAt: now, LastActive: now}
}

// The store is the one place every writer passes through, so a token in a
// remote never reaches the column — whichever caller handed it over.
func TestStoreNeverPersistsRemoteCredentials(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	leaked := leakedRemote

	p := newRemoteProject("rc-proj", "rc-user", "/tmp/rc-proj", &leaked)
	require.NoError(t, repo.CreateProject(ctx, p))
	assert.Equal(t, cleanRemote, rawRemote(t, raw, "projects", p.ID).String, "CreateProject")
	assert.Equal(t, leakedRemote, leaked, "the caller's value is not mutated")

	p2 := newRemoteProject("rc-proj-2", "rc-user", "/tmp/rc-proj-2", nil)
	require.NoError(t, repo.CreateProject(ctx, p2))
	other := "https://oauth2:glpat-FIXTURE@gitlab.com/g/p.git"
	p2.RemoteURL = &other
	require.NoError(t, repo.UpdateProject(ctx, p2, "rc-user"))
	assert.Equal(t, "https://gitlab.com/g/p.git", rawRemote(t, raw, "projects", p2.ID).String, "UpdateProject")

	now := time.Now().UTC()
	r := &core.Repo{ID: "rc-repo", ProjectID: p.ID, Name: "app", RelativePath: ".", RemoteURL: &leaked, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.CreateRepo(ctx, r))
	assert.Equal(t, cleanRemote, rawRemote(t, raw, "repos", r.ID).String, "CreateRepo")

	r.RemoteURL = &other
	require.NoError(t, store.ReplaceRepo(ctx, repo.ORM(), r))
	assert.Equal(t, "https://gitlab.com/g/p.git", rawRemote(t, raw, "repos", r.ID).String, "ReplaceRepo")

	// A lookup by the leaked form finds the clean row: a caller that still
	// holds a token URL does not create a duplicate project.
	got, err := repo.GetProjectByRemoteURLAndUser(ctx, leakedRemote, "rc-user")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)

	// Reads strip too: a row written before the stores did is never served
	// with its token, even before the migration has repaired it.
	_, err = raw.Exec(`UPDATE projects SET remote_url = $1 WHERE id = $2`, leakedRemote, p.ID)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE repos SET remote_url = $1 WHERE id = $2`, leakedRemote, r.ID)
	require.NoError(t, err)
	gp, err := repo.GetProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, cleanRemote, *gp.RemoteURL, "GetProject")
	gr, err := repo.GetRepo(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, cleanRemote, *gr.RemoteURL, "GetRepo")
	lr, err := repo.ListReposByProject(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, lr, 1)
	assert.Equal(t, cleanRemote, *lr[0].RemoteURL, "ListReposByProject")
}

const stripRemoteCredentialsMigration = "migrations/20261019172550_strip_remote_url_credentials.up.sql"

// The shipped migration, run over rows shaped like prod's: tokens in the
// userinfo, a collision with a clean sibling under projects_user_remote_url_uniq,
// and values that must not change.
func TestStripRemoteURLCredentialsMigration(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	sqlText, err := forgedb.MigrationsFS.ReadFile(stripRemoteCredentialsMigration)
	require.NoError(t, err)
	exec := func(stmt string, args ...any) {
		t.Helper()
		_, err := raw.Exec(stmt, args...)
		require.NoError(t, err, stmt)
	}

	proj := func(id, user, remote string) {
		var rv any
		if remote != "" {
			rv = remote
		}
		exec(`INSERT INTO projects (id, user_id, name, path, remote_url, created_at, updated_at, last_active)
			VALUES ($1, $2, $1, '/tmp/' || $1, $3, now(), now(), now())`, id, user, rv)
	}
	rp := func(id, remote string) {
		var rv any
		if remote != "" {
			rv = remote
		}
		exec(`INSERT INTO repos (id, project_id, name, relative_path, remote_url, created_at, updated_at)
			VALUES ($1, 'test-project', $1, $1, $2, now(), now())`, id, rv)
	}

	proj("m-token", "u1", leakedRemote)
	proj("m-clean", "u1", "https://github.com/acme/other.git")
	proj("m-ssh", "u1", "git@github.com:acme/ssh.git")
	proj("m-none", "u1", "")
	proj("m-path", "u1", "/home/me/repo")
	proj("m-ssh-pw", "u1", "ssh://git:hunter2@host.example/o/r.git")
	proj("m-ssh-user", "u5", "ssh://git@host.example/o/r.git")
	// Collisions: the same repo added once with a token and once clean, and
	// twice with different tokens.
	proj("m-dup-clean", "u2", cleanRemote)
	proj("m-dup-token", "u2", leakedRemote)
	proj("m-dup-a", "u3", "https://x-access-token:AAA@github.com/acme/dup.git")
	proj("m-dup-b", "u3", "https://x-access-token:BBB@github.com/acme/dup.git")
	proj("m-other-user", "u4", leakedRemote)

	rp("r-token", leakedRemote)
	rp("r-bare", "https://ghp_FIXTURE@github.com/acme/app.git")
	rp("r-clean", cleanRemote)
	rp("r-none", "")
	rp("r-file", "file:///srv/git/repo.git")

	check := func(label string) {
		t.Helper()
		want := map[string]string{
			"m-token": cleanRemote, "m-clean": "https://github.com/acme/other.git",
			"m-ssh": "git@github.com:acme/ssh.git", "m-path": "/home/me/repo",
			"m-ssh-pw": "ssh://git@host.example/o/r.git", "m-ssh-user": "ssh://git@host.example/o/r.git",
			"m-dup-clean": cleanRemote, "m-other-user": cleanRemote,
		}
		for id, w := range want {
			assert.Equal(t, w, rawRemote(t, raw, "projects", id).String, label+" "+id)
		}
		assert.False(t, rawRemote(t, raw, "projects", "m-none").Valid, label+" m-none stays NULL")
		assert.False(t, rawRemote(t, raw, "projects", "m-dup-token").Valid, label+" duplicate of a clean sibling gives up its URL")
		a, b := rawRemote(t, raw, "projects", "m-dup-a"), rawRemote(t, raw, "projects", "m-dup-b")
		assert.True(t, a.Valid && a.String == "https://github.com/acme/dup.git", label+" first duplicate keeps the clean URL")
		assert.False(t, b.Valid, label+" second duplicate gives up its URL")

		rwant := map[string]string{
			"r-token": cleanRemote, "r-bare": cleanRemote, "r-clean": cleanRemote,
			"r-file": "file:///srv/git/repo.git",
		}
		for id, w := range rwant {
			assert.Equal(t, w, rawRemote(t, raw, "repos", id).String, label+" "+id)
		}
		assert.False(t, rawRemote(t, raw, "repos", "r-none").Valid, label+" r-none stays NULL")

		var leaked int
		require.NoError(t, raw.QueryRow(`SELECT (SELECT count(*) FROM projects WHERE remote_url ~ '^[a-zA-Z][a-zA-Z0-9+.-]*://[^/@]+:[^/@]*@')
			+ (SELECT count(*) FROM repos WHERE remote_url ~ '^[a-zA-Z][a-zA-Z0-9+.-]*://[^/@]+:[^/@]*@')`).Scan(&leaked))
		assert.Zero(t, leaked, label+": no credential-bearing remote_url remains")
	}

	exec(string(sqlText))
	check("first run")
	exec(string(sqlText))
	check("second run (idempotent)")
}
