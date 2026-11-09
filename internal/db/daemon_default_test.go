// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
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

// The previous release, still serving during a rolling deploy or again after a
// rollback, writes only daemons.is_default, with exactly these statements (its
// store.SetDefaultDaemon).
func previousReleaseSetDefault(t *testing.T, rawDB *sql.DB, userID, daemonID string) {
	t.Helper()
	if _, err := rawDB.Exec(`UPDATE daemons SET is_default = false WHERE user_id = $1 AND is_default AND id <> $2`, userID, daemonID); err != nil {
		t.Fatalf("previous release clear: %v", err)
	}
	if daemonID == "" {
		return
	}
	if _, err := rawDB.Exec(`UPDATE daemons SET is_default = true WHERE id = $1 AND user_id = $2 AND NOT is_default`, daemonID, userID); err != nil {
		t.Fatalf("previous release set: %v", err)
	}
}

// recordedDefault is the user's durable choice (user_default_daemons), "" for none.
func recordedDefault(t *testing.T, rawDB *sql.DB, userID string) string {
	t.Helper()
	var id string
	err := rawDB.QueryRow(`SELECT daemon_id FROM user_default_daemons WHERE user_id = $1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading recorded default: %v", err)
	}
	return id
}

// recreate deletes a machine's row and registers it again under the same id,
// as the registry snapshot and the machine's next connect do.
func recreate(t *testing.T, repo *Repo, id, userID string) {
	t.Helper()
	if _, _, err := repo.RemoveDaemon(context.Background(), id); err != nil {
		t.Fatalf("RemoveDaemon(%s): %v", id, err)
	}
	seedLifecycleDaemon(t, repo, id, userID)
}

// A user whose choice is already recorded (backfilled, or set by this
// release) changes or clears it on the previous release: both releases read
// the new answer, and the durable choice follows, so a row the registry
// recreates afterwards comes back as what the user chose last.
func TestSetDefaultDaemon_PreviousReleaseWritesKeepBothReleasesInStep(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "default-mixed-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID)
	})
	seedLifecycleDaemon(t, repo, "mixed-a", userID)
	seedLifecycleDaemon(t, repo, "mixed-b", userID)

	if err := repo.SetDefaultDaemon(ctx, userID, "mixed-a"); err != nil {
		t.Fatalf("SetDefaultDaemon a: %v", err)
	}
	if got := recordedDefault(t, rawDB, userID); got != "mixed-a" {
		t.Fatalf("recorded default = %q, want mixed-a", got)
	}

	// The previous release moves it to b.
	previousReleaseSetDefault(t, rawDB, userID, "mixed-b")
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "mixed-b" {
		t.Fatalf("default after the previous release set b = %v, want [mixed-b]", got)
	}
	if got := recordedDefault(t, rawDB, userID); got != "mixed-b" {
		t.Fatalf("recorded default after the previous release set b = %q, want mixed-b", got)
	}
	recreate(t, repo, "mixed-a", userID)
	recreate(t, repo, "mixed-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "mixed-b" {
		t.Fatalf("default after both rows were recreated = %v, want [mixed-b]", got)
	}

	// The previous release clears it.
	previousReleaseSetDefault(t, rawDB, userID, "")
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("default after the previous release cleared it = %v, want none", got)
	}
	if got := recordedDefault(t, rawDB, userID); got != "" {
		t.Fatalf("recorded default after the previous release cleared it = %q, want none", got)
	}
	recreate(t, repo, "mixed-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("a cleared default came back with its row: %v", got)
	}
}

// Clearing on this release also forgets a default whose machine has no row
// right now, so it does not come back when that machine reconnects.
func TestSetDefaultDaemon_ClearForgetsADefaultWhoseRowIsGone(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "default-gone-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID)
	})
	seedLifecycleDaemon(t, repo, "gone-a", userID)
	seedLifecycleDaemon(t, repo, "gone-b", userID)
	if err := repo.SetDefaultDaemon(ctx, userID, "gone-a"); err != nil {
		t.Fatalf("SetDefaultDaemon: %v", err)
	}
	if _, _, err := repo.RemoveDaemon(ctx, "gone-a"); err != nil {
		t.Fatalf("RemoveDaemon: %v", err)
	}
	if err := repo.SetDefaultDaemon(ctx, userID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	seedLifecycleDaemon(t, repo, "gone-a", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("a cleared default came back with its row: %v", got)
	}
}
