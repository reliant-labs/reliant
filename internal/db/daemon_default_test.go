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

// recreate deletes a machine's row and registers it again under the same id,
// as the registry snapshot and the machine's next connect do.
func recreate(t *testing.T, repo *Repo, id, userID string) {
	t.Helper()
	if _, _, err := repo.RemoveDaemon(context.Background(), id); err != nil {
		t.Fatalf("RemoveDaemon(%s): %v", id, err)
	}
	seedLifecycleDaemon(t, repo, id, userID)
}

// The registry deletes a row the control plane's set omits, and the machine's
// next connect recreates it under the same id: the same machine, so still the
// default (a laptop lost it across every sleep and app update).
func TestDefaultDaemon_SurvivesTheRegistryRecreatingItsRow(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "recreated-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID)
	})
	seedLifecycleDaemon(t, repo, "recreated-a", userID)
	seedLifecycleDaemon(t, repo, "recreated-b", userID)
	if err := repo.SetDefaultDaemon(ctx, userID, "recreated-a"); err != nil {
		t.Fatalf("SetDefaultDaemon: %v", err)
	}

	if _, _, err := repo.RemoveDaemon(ctx, "recreated-a"); err != nil {
		t.Fatalf("RemoveDaemon: %v", err)
	}
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("default while the row is gone = %v, want none", got)
	}
	seedLifecycleDaemon(t, repo, "recreated-a", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "recreated-a" {
		t.Fatalf("default after the row was recreated = %v, want [recreated-a]", got)
	}
	if d, err := repo.GetDaemon(ctx, "recreated-a"); err != nil || !d.IsDefault {
		t.Fatalf("GetDaemon(recreated-a) = %+v, %v; want the default", d, err)
	}
	// Re-registering a row that exists (every connect) changes nothing.
	seedLifecycleDaemon(t, repo, "recreated-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "recreated-a" {
		t.Fatalf("default after b re-registered = %v, want [recreated-a]", got)
	}
}

// previousReleaseSetDefault is the previous release's store.SetDefaultDaemon
// (the one that added user_default_daemons), statement for statement: it
// writes only daemons.is_default, and a clear also deletes the remembered
// default. The release before it never serves beside this schema
// (20261109201518_restore_default_daemon ships a release after it).
func previousReleaseSetDefault(t *testing.T, rawDB *sql.DB, userID, daemonID string) {
	t.Helper()
	tx, err := rawDB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SELECT id FROM daemons WHERE user_id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatalf("previous release lock: %v", err)
	}
	if daemonID == "" {
		if _, err := tx.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("previous release forget: %v", err)
		}
	}
	if _, err := tx.Exec(`UPDATE daemons SET is_default = false WHERE user_id = $1 AND is_default AND id <> $2`, userID, daemonID); err != nil {
		t.Fatalf("previous release clear: %v", err)
	}
	if daemonID != "" {
		if _, err := tx.Exec(`UPDATE daemons SET is_default = true WHERE id = $1 AND user_id = $2 AND NOT is_default`, daemonID, userID); err != nil {
			t.Fatalf("previous release set: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// rememberedDefault is the user's remembered default, "" for none.
func rememberedDefault(t *testing.T, rawDB *sql.DB, userID string) string {
	t.Helper()
	var id string
	err := rawDB.QueryRow(`SELECT daemon_id FROM user_default_daemons WHERE user_id = $1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading remembered default: %v", err)
	}
	return id
}

// The previous release, serving beside this one during a rolling deploy or
// again after a rollback, moves and clears a remembered default: both
// releases read its answer, and a row the registry recreates afterwards comes
// back as what the user chose last — including a clear made while the
// remembered machine had no row.
func TestDefaultDaemon_PreviousReleaseWritesStayInStep(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "mixed-user"
	t.Cleanup(func() {
		_, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID)
		_, _ = rawDB.Exec(`DELETE FROM user_default_daemons WHERE user_id = $1`, userID)
	})
	seedLifecycleDaemon(t, repo, "mixed-a", userID)
	seedLifecycleDaemon(t, repo, "mixed-b", userID)
	if err := repo.SetDefaultDaemon(ctx, userID, "mixed-a"); err != nil {
		t.Fatalf("SetDefaultDaemon a: %v", err)
	}
	if got := rememberedDefault(t, rawDB, userID); got != "mixed-a" {
		t.Fatalf("remembered default = %q, want mixed-a", got)
	}

	// Moved to b by the previous release.
	previousReleaseSetDefault(t, rawDB, userID, "mixed-b")
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "mixed-b" {
		t.Fatalf("default after the previous release set b = %v, want [mixed-b]", got)
	}
	if got := rememberedDefault(t, rawDB, userID); got != "mixed-b" {
		t.Fatalf("remembered default after the previous release set b = %q, want mixed-b", got)
	}
	recreate(t, repo, "mixed-a", userID)
	recreate(t, repo, "mixed-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 1 || got[0] != "mixed-b" {
		t.Fatalf("default after both rows were recreated = %v, want [mixed-b]", got)
	}

	// Cleared by the previous release while b's row exists.
	previousReleaseSetDefault(t, rawDB, userID, "")
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("default after the previous release cleared it = %v, want none", got)
	}
	recreate(t, repo, "mixed-b", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("a cleared default came back with its row: %v", got)
	}

	// Cleared by the previous release while a's row is gone (R2.B1): no
	// is_default write reaches it, and the release still forgets it.
	if err := repo.SetDefaultDaemon(ctx, userID, "mixed-a"); err != nil {
		t.Fatalf("SetDefaultDaemon a again: %v", err)
	}
	if _, _, err := repo.RemoveDaemon(ctx, "mixed-a"); err != nil {
		t.Fatalf("RemoveDaemon a: %v", err)
	}
	previousReleaseSetDefault(t, rawDB, userID, "")
	seedLifecycleDaemon(t, repo, "mixed-a", userID)
	if got := defaultDaemonIDs(t, repo, userID); len(got) != 0 {
		t.Fatalf("a default cleared while its row was gone came back with it: %v", got)
	}
}
