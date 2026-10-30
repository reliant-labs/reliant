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
