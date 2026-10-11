// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestRenewDaemonAttachmentRecreatesAReapedLease pins the renewal the gateway
// performs on every heartbeat it reads.
//
// The bug it guards: the TTL reaper deleted a live stream's lease after a
// 20-minute inbound stall, and the renewal that ran on every heartbeat after
// the stall was an UPDATE — it matched no row, forever. The daemon kept
// serving terminal commands for six hours while every chat read it as
// DISCONNECTED (prod, 2026-10-11).
func TestRenewDaemonAttachmentRecreatesAReapedLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const (
		userID   = "renew-test-user"
		daemonID = "renew-test-daemon"
	)
	if err := repo.UpsertDaemon(ctx, &Daemon{ID: daemonID, UserID: userID}); err != nil {
		t.Fatalf("seed daemon: %v", err)
	}
	attachedAt := time.Now().UTC().Add(-9 * time.Hour).Truncate(time.Microsecond)
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: attachedAt, LastStreamActivity: attachedAt,
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	// The reaper runs while the stream is stalled.
	if n, err := repo.DeleteStaleDaemonAttachments(ctx, 15*time.Minute); err != nil || n != 1 {
		t.Fatalf("reap = (%d, %v), want (1, nil)", n, err)
	}
	if attached, _ := repo.IsDaemonIDAttached(ctx, daemonID, 90*time.Second); attached {
		t.Fatal("precondition: the lease should be gone after the reap")
	}

	// The stall clears and the next heartbeat is read.
	if err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: attachedAt, LastStreamActivity: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RenewDaemonAttachment: %v", err)
	}
	attached, err := repo.IsDaemonIDAttached(ctx, daemonID, 90*time.Second)
	if err != nil {
		t.Fatalf("IsDaemonIDAttached: %v", err)
	}
	if !attached {
		t.Fatal("a heartbeat on a live stream must re-create a lease the reaper deleted; " +
			"otherwise the daemon reads DISCONNECTED until it happens to reconnect")
	}
	var gotAttachedAt time.Time
	if err := rawDB.QueryRow(`SELECT attached_at FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&gotAttachedAt); err != nil {
		t.Fatalf("read attached_at: %v", err)
	}
	if !gotAttachedAt.Equal(attachedAt) {
		t.Errorf("re-created lease attached_at = %v, want the session's %v", gotAttachedAt, attachedAt)
	}
}

// On an existing row a renewal moves last_stream_activity forward and nothing
// else: telemetry belongs to the registered session, and a late renewal (say,
// from a connection a reconnect already superseded) must not regress it.
func TestRenewDaemonAttachmentOnlyAdvancesTheLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const (
		userID   = "renew-advance-user"
		daemonID = "renew-advance-daemon"
	)
	if err := repo.UpsertDaemon(ctx, &Daemon{ID: daemonID, UserID: userID}); err != nil {
		t.Fatalf("seed daemon: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: now.Add(-time.Hour), LastStreamActivity: now,
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if err := repo.UpdateDaemonAttachmentMemory(ctx, daemonID, 100, 200, true); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	lease := func() (activity time.Time, used int64) {
		t.Helper()
		if err := rawDB.QueryRow(`SELECT last_stream_activity, memory_used_bytes FROM daemon_attachment WHERE daemon_id = $1`, daemonID).
			Scan(&activity, &used); err != nil {
			t.Fatalf("read lease: %v", err)
		}
		return activity, used
	}

	// An older renewal is a no-op.
	if err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, LastStreamActivity: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("stale renewal: %v", err)
	}
	if got, _ := lease(); !got.Equal(now) {
		t.Errorf("an older renewal moved the lease to %v, want it to stay at %v", got, now)
	}

	// A newer renewal advances the lease and leaves telemetry alone.
	later := now.Add(15 * time.Second)
	if err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, LastStreamActivity: later,
	}); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if got, used := lease(); !got.Equal(later) || used != 100 {
		t.Errorf("after renewal lease = (%v, used=%d), want (%v, used=100)", got, used, later)
	}
}

// A renewal never creates a lease for a daemon the caller does not own, or one
// the control plane has deleted: the stream's heartbeat is not authority over
// the registry.
func TestRenewDaemonAttachmentDoesNotCreateAForeignOrRemovedLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := repo.UpsertDaemon(ctx, &Daemon{ID: "renew-owned", UserID: "renew-owner"}); err != nil {
		t.Fatalf("seed daemon: %v", err)
	}
	count := func(daemonID string) int {
		t.Helper()
		var n int
		if err := rawDB.QueryRow(`SELECT count(*) FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	if err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "renew-owned", UserID: "someone-else"}); err != nil {
		t.Fatalf("foreign renewal: %v", err)
	}
	if count("renew-owned") != 0 {
		t.Error("a renewal created a lease for a daemon owned by another user")
	}

	if err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "renew-removed", UserID: "renew-owner"}); err != nil {
		t.Fatalf("removed-daemon renewal: %v", err)
	}
	if count("renew-removed") != 0 {
		t.Error("a renewal resurrected a lease for a daemon with no registry row")
	}
}
