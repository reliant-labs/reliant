// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestDeleteStaleDaemonAttachments pins the TTL sweep's SQL semantics against
// a real Postgres: rows whose reachability lease expired are deleted, rows
// still inside the window are not, and a reconnect that just renewed its
// lease is never raced away.
//
// The rows this GCs are not hypothetical. daemon_attachment rows are deleted
// on graceful teardown only, so a gateway that crashes or is rescheduled
// strands every row it held. Dev's registry on 2026-08-24 still carried
// 0c9cff04 (29 days stale) and 81dc53c1 (a workspace pod deleted 51 days
// earlier) alongside one live daemon — and those two orphans had pinned
// /flow-health at 503 for the entire environment ever since.
func TestDeleteStaleDaemonAttachments(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "reap-test-user"
	clean := func() {
		_, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID)
	}
	clean()
	t.Cleanup(clean)

	now := time.Now().UTC()
	seed := func(daemonID string, lastActivity time.Time) {
		t.Helper()
		att := &DaemonAttachment{
			DaemonID:           daemonID,
			UserID:             userID,
			Source:             DaemonAttachmentSourceInbound,
			AttachedAt:         lastActivity,
			LastStreamActivity: lastActivity,
		}
		if err := repo.UpsertDaemonAttachment(ctx, att); err != nil {
			t.Fatalf("seed %s: %v", daemonID, err)
		}
	}
	exists := func(daemonID string) bool {
		t.Helper()
		var n int
		err := rawDB.QueryRow(`SELECT count(*) FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&n)
		if err != nil {
			t.Fatalf("count %s: %v", daemonID, err)
		}
		return n > 0
	}

	seed("reap-live", now.Add(-5*time.Second))    // lease renewed seconds ago
	seed("reap-recent", now.Add(-10*time.Minute)) // stale to readers, inside the TTL
	seed("reap-orphan-29d", now.Add(-29*24*time.Hour))
	seed("reap-orphan-51d", now.Add(-51*24*time.Hour))

	n, err := repo.DeleteStaleDaemonAttachments(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("DeleteStaleDaemonAttachments: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d rows, want 2", n)
	}
	if exists("reap-orphan-29d") || exists("reap-orphan-51d") {
		t.Error("expired orphans survived the sweep")
	}
	// A lease inside the TTL may still be renewed — evicting it would knock a
	// daemon offline over a NATS hiccup.
	if !exists("reap-recent") || !exists("reap-live") {
		t.Error("sweep deleted a row still inside the TTL")
	}

	// Idempotent: a second sweep with nothing expired deletes nothing.
	if n, err = repo.DeleteStaleDaemonAttachments(ctx, 15*time.Minute); err != nil || n != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", n, err)
	}

	// Reconnect wins the race: the upsert sets the lease to now, so the row
	// no longer matches the DELETE's predicate even under a tight TTL.
	seed("reap-reconnect", now.Add(-51*24*time.Hour))
	seed("reap-reconnect", time.Now().UTC())
	if n, err = repo.DeleteStaleDaemonAttachments(ctx, time.Minute); err != nil {
		t.Fatalf("tight sweep: %v", err)
	} else if n != 1 { // only reap-recent (10m) expires at a 1m TTL
		t.Errorf("tight sweep deleted %d rows, want 1", n)
	}
	if !exists("reap-reconnect") {
		t.Error("a just-reconnected daemon's lease was reaped")
	}

	// A non-positive threshold is a caller bug, not a request to delete the
	// whole table.
	if _, err := repo.DeleteStaleDaemonAttachments(ctx, 0); err == nil {
		t.Error("expected an error for a non-positive threshold")
	}
	if !exists("reap-live") {
		t.Error("a zero threshold wiped live rows")
	}
}
