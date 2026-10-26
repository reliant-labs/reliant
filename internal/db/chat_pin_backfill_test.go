// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// The backfill pins existing chats from evidence only (research/
// CHAT_MACHINE_PLACEMENT.md): the worktree's owner, else the project's single
// installed machine, and only a machine the chat's own user owns. Everything
// else stays NULL for the next run to pin. The migration file itself is run
// over seeded rows, so what ships is what is tested.
func TestPinUnpinnedChatsMigration(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	exec := func(q string, args ...any) {
		t.Helper()
		_, err := raw.Exec(q, args...)
		require.NoError(t, err, q)
	}
	project := func(id string) {
		exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
			VALUES ($1, 'u1', $1, '/p/'||$1, now(), now(), now())`, id)
	}
	daemon := func(id, user string) {
		exec(`INSERT INTO daemons (id, user_id) VALUES ($1, $2)`, id, user)
	}
	install := func(proj, d, state string) {
		exec(`INSERT INTO project_daemons (project_id, daemon_id, path, install_state) VALUES ($1, $2, '/p', $3)`, proj, d, state)
	}
	worktree := func(id, proj string, owner any) {
		exec(`INSERT INTO worktrees (id, name, path, branch, base_branch, project_id, created_at, updated_at, last_active, daemon_id)
			VALUES ($1, $1, '/w/'||$1, $1, 'main', $2, now(), now(), now(), $3)`, id, proj, owner)
	}
	chat := func(id, user, proj string, wt any, noMachine bool, pin any) {
		exec(`INSERT INTO chats (id, title, project_id, user_id, worktree_id, no_machine, active_daemon_id, created_at, updated_at, last_active)
			VALUES ($1, 't', $2, $3, $4, $5, $6, now() - interval '1 day', now() - interval '1 day', now() - interval '1 day')`,
			id, proj, user, wt, noMachine, pin)
	}

	for _, p := range []string{"single", "several", "none", "cloning", "foreign"} {
		project(p)
	}
	daemon("vm", "u1")
	daemon("vm-b", "u1")
	daemon("laptop", "u1")
	daemon("theirs", "u2")

	install("single", "vm", "installed")
	install("single", "laptop", "installing") // a clone in flight is not an install
	install("several", "vm", "installed")
	install("several", "vm-b", "installed")
	install("cloning", "vm", "installing")
	install("foreign", "theirs", "installed")
	worktree("wt-laptop", "several", "laptop")
	worktree("wt-main", "single", nil)
	worktree("wt-theirs", "single", "theirs")

	chat("by-worktree", "u1", "several", "wt-laptop", false, nil)
	chat("by-single-install", "u1", "single", "wt-main", false, nil)
	chat("by-single-install-no-worktree", "u1", "single", nil, false, nil)
	chat("worktree-owned-by-another-user", "u1", "single", nil, false, nil)
	exec(`UPDATE chats SET worktree_id = 'wt-theirs' WHERE id = 'worktree-owned-by-another-user'`)
	chat("several-installs", "u1", "several", nil, false, nil)
	chat("only-cloning", "u1", "cloning", nil, false, nil)
	chat("no-installs", "u1", "none", nil, false, nil)
	chat("install-on-another-users-machine", "u1", "foreign", nil, false, nil)
	chat("already-pinned", "u1", "single", nil, false, "laptop")
	chat("no-machine", "u1", "single", nil, true, nil)

	// Found by name, not version: the merge queue re-versions a branch's
	// migrations above main's when it lands.
	matches, err := fs.Glob(forgedb.MigrationsFS, "migrations/*_pin_unpinned_chats_to_machine.up.sql")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	body, err := forgedb.MigrationsFS.ReadFile(matches[0])
	require.NoError(t, err)
	exec(string(body))

	pin := func(id string) sql.NullString {
		var v sql.NullString
		require.NoError(t, raw.QueryRow(`SELECT active_daemon_id FROM chats WHERE id = $1`, id).Scan(&v))
		return v
	}
	want := map[string]string{
		"by-worktree":                   "laptop", // the machine holding its checkout
		"by-single-install":             "vm",     // the worktree is the main checkout: no owner, so the install
		"by-single-install-no-worktree": "vm",
		"already-pinned":                "laptop", // never overwritten
	}
	for id, daemonID := range want {
		got := pin(id)
		assert.True(t, got.Valid, id)
		assert.Equal(t, daemonID, got.String, id)
	}
	// Its worktree is owned by another user's machine, which is no evidence for
	// this user, so the project's single install decides.
	assert.Equal(t, "vm", pin("worktree-owned-by-another-user").String)
	for _, id := range []string{"several-installs", "only-cloning", "no-installs", "install-on-another-users-machine", "no-machine"} {
		assert.False(t, pin(id).Valid, "%s stays unpinned: nothing proves where it ran", id)
	}

	// Not something the user did to the chat: the sidebar's ordering holds.
	var moved int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM chats WHERE id = 'by-worktree' AND updated_at > now() - interval '1 hour'`).Scan(&moved))
	assert.Zero(t, moved, "updated_at does not move")
}
