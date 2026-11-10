// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// defaultDaemonIDs returns the user's daemons that carry is_default, read
// back through ListDaemonsByUserID — the read default resolution uses.
func defaultDaemonIDs(t *testing.T, repo *Repo, userID string) []string {
	t.Helper()
	daemons, err := repo.ListDaemonsByUserID(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListDaemonsByUserID: %v", err)
	}
	var ids []string
	for _, d := range daemons {
		if d.IsDefault {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// The real SQL: a user has at most one default (the partial unique index
// would refuse a second, so the set must clear the old one first), a
// machine another user owns is NotFound and changes nothing, "" clears, a
// re-registration keeps the choice, and removing the machine removes it.
func TestSetDefaultDaemon_OnePerUserOwnedOnlyAndClearedWithTheMachine(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "default-user"
	const otherUser = "default-other-user"
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id IN ($1, $2)`, userID, otherUser) })
	seedLifecycleDaemon(t, repo, "default-a", userID)
	seedLifecycleDaemon(t, repo, "default-b", userID)
	seedLifecycleDaemon(t, repo, "default-theirs", otherUser)

	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("a new machine is nobody's default; got %v", got)
	}

	if err := repo.SetDefaultDaemon(ctx, userID, "default-a"); err != nil {
		t.Fatalf("SetDefaultDaemon a: %v", err)
	}
	if err := repo.SetDefaultDaemon(ctx, userID, "default-b"); err != nil {
		t.Fatalf("SetDefaultDaemon b: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "default-b" {
		t.Fatalf("default = %v, want [default-b]", got)
	}

	err := repo.SetDefaultDaemon(ctx, userID, "default-theirs")
	if !svcerr.IsNotFound(err) {
		t.Fatalf("another user's machine: err = %v, want NotFound", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "default-b" {
		t.Fatalf("a refused set changed the default: %v", got)
	}
	if got := defaultDaemonIDs(t, repo, otherUser); len(got) != 0 {
		t.Fatalf("a refused set wrote another user's row: %v", got)
	}

	// The daemon re-registering (every connect) must not reset the choice.
	seedLifecycleDaemon(t, repo, "default-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "default-b" {
		t.Fatalf("re-registration reset the default: %v", got)
	}

	if err := repo.SetDefaultDaemon(ctx, userID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("cleared default still set: %v", got)
	}

	if err := repo.SetDefaultDaemon(ctx, userID, "default-a"); err != nil {
		t.Fatalf("SetDefaultDaemon a again: %v", err)
	}
	if _, _, err := repo.RemoveDaemon(ctx, "default-a"); err != nil {
		t.Fatalf("RemoveDaemon: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("the removed machine's default survived: %v", got)
	}
}

// Clearing forgets the user's remembered default (user_default_daemons) even
// when that machine has no daemons row right now, which no is_default write
// can reach; setting one leaves the remembered default to the release that
// remembers it, and another user's is untouched.
func TestSetDefaultDaemon_ClearForgetsTheRememberedDefault(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "remembered-user"
	const otherUser = "remembered-other-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id IN ($1, $2)`, userID, otherUser)
	})
	seedLifecycleDaemon(t, repo, "remembered-b", userID)
	// Remembered for a machine whose row the registry has deleted.
	if _, err := rawDB.Exec(`INSERT INTO user_default_daemons (user_id, daemon_id) VALUES ($1, 'remembered-a'), ($2, 'theirs')`, userID, otherUser); err != nil {
		t.Fatalf("seed remembered defaults: %v", err)
	}

	remembered := func(user string) bool {
		t.Helper()
		var n int
		if err := rawDB.QueryRow(`SELECT count(*) FROM user_default_daemons WHERE user_id = $1`, user).Scan(&n); err != nil {
			t.Fatalf("count remembered default: %v", err)
		}
		return n > 0
	}

	if err := repo.SetDefaultDaemon(ctx, userID, "remembered-b"); err != nil {
		t.Fatalf("SetDefaultDaemon b: %v", err)
	}
	if !remembered(userID) {
		t.Fatal("setting a default forgot the remembered one")
	}

	if err := repo.SetDefaultDaemon(ctx, userID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if remembered(userID) {
		t.Fatal("clearing left the remembered default of a machine with no row")
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("default after clear = %v, want none", got)
	}
	if !remembered(otherUser) {
		t.Fatal("clearing forgot another user's remembered default")
	}
}
