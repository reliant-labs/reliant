// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
)

// TestRevokeDaemonPATsByDaemonID verifies that revoking by daemon ID marks
// exactly the live PATs bound to that daemon, leaves other daemons' and
// unbound PATs untouched, and is idempotent (a second call revokes nothing).
func TestRevokeDaemonPATsByDaemonID(t *testing.T) {
	repo, db, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	// Isolate this test's rows from any other test's daemon_pats.
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM daemon_pats WHERE user_id = 'test-user'`)
	})
	if _, err := db.Exec(`DELETE FROM daemon_pats WHERE user_id = 'test-user'`); err != nil {
		t.Fatalf("pre-clean daemon_pats: %v", err)
	}

	mk := func(id, daemonID, hash string) {
		t.Helper()
		p := &DaemonPAT{
			ID:          id,
			UserID:      "test-user",
			DaemonID:    daemonID,
			TokenHash:   hash,
			TokenPrefix: "rlnt_pat_",
			Name:        "n-" + id,
		}
		if err := repo.CreateDaemonPAT(ctx, p); err != nil {
			t.Fatalf("CreateDaemonPAT(%s): %v", id, err)
		}
	}

	// Two live PATs for daemon-A, one for daemon-B, one unbound.
	mk("pat-a1", "daemon-A", "hash-a1")
	mk("pat-a2", "daemon-A", "hash-a2")
	mk("pat-b1", "daemon-B", "hash-b1")
	mk("pat-unbound", "", "hash-unbound")

	count, err := repo.RevokeDaemonPATsByDaemonID(ctx, "daemon-A")
	if err != nil {
		t.Fatalf("RevokeDaemonPATsByDaemonID: %v", err)
	}
	if count != 2 {
		t.Errorf("revoked count = %d, want 2", count)
	}

	revoked := func(id string) bool {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_pats WHERE id = $1 AND revoked_at IS NOT NULL`, id).Scan(&n); err != nil {
			t.Fatalf("query revoked(%s): %v", id, err)
		}
		return n == 1
	}

	if !revoked("pat-a1") || !revoked("pat-a2") {
		t.Errorf("daemon-A PATs should be revoked: a1=%v a2=%v", revoked("pat-a1"), revoked("pat-a2"))
	}
	if revoked("pat-b1") {
		t.Errorf("daemon-B PAT must not be revoked")
	}
	if revoked("pat-unbound") {
		t.Errorf("unbound PAT must not be revoked")
	}

	// Idempotent: nothing live remains for daemon-A.
	count2, err := repo.RevokeDaemonPATsByDaemonID(ctx, "daemon-A")
	if err != nil {
		t.Fatalf("second RevokeDaemonPATsByDaemonID: %v", err)
	}
	if count2 != 0 {
		t.Errorf("second revoke count = %d, want 0", count2)
	}

	// Empty daemon ID is rejected.
	if _, err := repo.RevokeDaemonPATsByDaemonID(ctx, ""); err == nil {
		t.Errorf("RevokeDaemonPATsByDaemonID(\"\") = nil error, want error")
	}
}
