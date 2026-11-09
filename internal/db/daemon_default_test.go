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
// re-registration keeps the choice, a removed machine is nobody's default,
// and a row the registry recreates under the same id is still the default.
func TestSetDefaultDaemon_OnePerUserOwnedOnlyAndSurvivesTheRowBeingRecreated(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "default-user"
	const otherUser = "default-other-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id IN ($1, $2)`, userID, otherUser)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id IN ($1, $2)`, userID, otherUser)
	})
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
		t.Fatalf("a removed machine is still listed as the default: %v", got)
	}
	if d, err := repo.GetDaemon(ctx, "default-b"); err != nil || d.IsDefault {
		t.Fatalf("GetDaemon(default-b) = %+v, %v; want a machine that is not the default", d, err)
	}

	// The registry deletes a row the control plane's set omits and the
	// machine's next connect recreates it under the same id: the same
	// machine, so still the default (a laptop lost it across every sleep).
	seedLifecycleDaemon(t, repo, "default-a", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "default-a" {
		t.Fatalf("a recreated row lost the default: %v", got)
	}
	if d, err := repo.GetDaemon(ctx, "default-a"); err != nil || !d.IsDefault {
		t.Fatalf("GetDaemon(default-a) = %+v, %v; want the default", d, err)
	}
}

// A default set by the previous release, which writes only daemons.is_default
// (the rolling deploy), still reads as the default until a choice is recorded.
func TestSetDefaultDaemon_PreviousReleaseColumnReadsUntilAChoiceIsRecorded(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "default-legacy-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID)
	})
	seedLifecycleDaemon(t, repo, "legacy-a", userID)
	seedLifecycleDaemon(t, repo, "legacy-b", userID)

	if _, err := rawDB.Exec(`UPDATE daemons SET is_default = true WHERE id = 'legacy-a'`); err != nil {
		t.Fatalf("seed legacy default: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "legacy-a" {
		t.Fatalf("default = %v, want the column's [legacy-a]", got)
	}

	if err := repo.SetDefaultDaemon(ctx, userID, "legacy-b"); err != nil {
		t.Fatalf("SetDefaultDaemon: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "legacy-b" {
		t.Fatalf("default = %v, want the recorded [legacy-b]", got)
	}
	var column string
	if err := rawDB.QueryRow(`SELECT id FROM daemons WHERE user_id = $1 AND is_default`, userID).Scan(&column); err != nil || column != "legacy-b" {
		t.Fatalf("daemons.is_default = %q, %v; the previous release must read the same choice", column, err)
	}
}
