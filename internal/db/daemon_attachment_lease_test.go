// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The reachability lease (daemon_attachment) semantics behind prod
// 2026-10-11, against real Postgres: a daemon attached and serving for hours
// read DISCONNECTED to every chat because its lease row was gone — and the
// keepalive could only UPDATE a row that was there.
//
// The lease is written only by the gateway holding the stream: upserted on
// connect, renewed on every heartbeat read (RenewDaemonAttachment), released
// on teardown (ReleaseDaemonAttachment). These pin the two properties that
// make it follow the stream: a renewal re-creates a lost row, and a release
// never deletes a newer session's row.

type leaseState struct {
	attachedAt time.Time
	activity   time.Time
	userID     string
	memoryUsed int64
}

func readLease(t *testing.T, rawDB *sql.DB, daemonID string) (leaseState, bool) {
	t.Helper()
	var s leaseState
	err := rawDB.QueryRow(`SELECT attached_at, last_stream_activity, user_id, memory_used_bytes FROM daemon_attachment WHERE daemon_id = $1`, daemonID).
		Scan(&s.attachedAt, &s.activity, &s.userID, &s.memoryUsed)
	if err == sql.ErrNoRows {
		return leaseState{}, false
	}
	if err != nil {
		t.Fatalf("read lease %s: %v", daemonID, err)
	}
	return s, true
}

func seedLeaseDaemon(t *testing.T, repo *Repo, daemonID, userID string) {
	t.Helper()
	if err := repo.UpsertDaemon(context.Background(), &Daemon{ID: daemonID, UserID: userID}); err != nil {
		t.Fatalf("seed daemon %s: %v", daemonID, err)
	}
}

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
	seedLeaseDaemon(t, repo, daemonID, userID)
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
	created, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: attachedAt, LastStreamActivity: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RenewDaemonAttachment: %v", err)
	}
	if !created {
		t.Error("a renewal that re-created the lease must say so: the gateway announces it to the owner's clients")
	}
	attached, err := repo.IsDaemonIDAttached(ctx, daemonID, 90*time.Second)
	if err != nil {
		t.Fatalf("IsDaemonIDAttached: %v", err)
	}
	if !attached {
		t.Fatal("a heartbeat on a live stream must re-create a lease the reaper deleted; " +
			"otherwise the daemon reads DISCONNECTED until it happens to reconnect")
	}
	if got, _ := readLease(t, rawDB, daemonID); !got.attachedAt.Equal(attachedAt) {
		t.Errorf("re-created lease attached_at = %v, want the session's %v", got.attachedAt, attachedAt)
	}
}

// On an existing row a renewal moves the lease forward and nothing else:
// telemetry belongs to the registered session, and a late renewal (say, from a
// connection a reconnect already superseded) must not regress it.
func TestRenewDaemonAttachmentOnlyAdvancesTheLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const (
		userID   = "renew-advance-user"
		daemonID = "renew-advance-daemon"
	)
	seedLeaseDaemon(t, repo, daemonID, userID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := now.Add(-time.Hour)
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: session, LastStreamActivity: now,
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if err := repo.UpdateDaemonAttachmentMemory(ctx, daemonID, 100, 200, true); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	renew := func(activity time.Time) bool {
		t.Helper()
		created, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
			DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
			AttachedAt: session, LastStreamActivity: activity,
		})
		if err != nil {
			t.Fatalf("renew: %v", err)
		}
		return created
	}

	// An older renewal is a no-op, and renewing a present row is no creation.
	if renew(now.Add(-time.Minute)) {
		t.Error("renewing a present row reported a creation")
	}
	if got, _ := readLease(t, rawDB, daemonID); !got.activity.Equal(now) {
		t.Errorf("an older renewal moved the lease to %v, want it to stay at %v", got.activity, now)
	}

	// A newer renewal advances the lease and leaves the session and its
	// telemetry alone.
	later := now.Add(15 * time.Second)
	renew(later)
	got, _ := readLease(t, rawDB, daemonID)
	if !got.activity.Equal(later) || got.memoryUsed != 100 || !got.attachedAt.Equal(session) {
		t.Errorf("after renewal lease = (activity %v, used=%d, attached %v), want (%v, used=100, attached %v)",
			got.activity, got.memoryUsed, got.attachedAt, later, session)
	}
}

// A renewal never creates a lease for a daemon the caller does not own or one
// the control plane has deleted, and never touches another user's lease: the
// stream's heartbeat is not authority over the registry.
func TestRenewDaemonAttachmentDoesNotCreateAForeignOrRemovedLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	at := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	renew := func(daemonID, userID string) {
		t.Helper()
		created, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
			DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
			AttachedAt: at.Add(time.Minute), LastStreamActivity: at.Add(time.Minute),
		})
		if err != nil || created {
			t.Fatalf("renew %s as %s = (created %v, %v), want (false, nil): nothing of the caller's to renew", daemonID, userID, created, err)
		}
	}

	seedLeaseDaemon(t, repo, "renew-owned", "renew-owner")
	renew("renew-owned", "someone-else")
	if _, ok := readLease(t, rawDB, "renew-owned"); ok {
		t.Error("a renewal created a lease for a daemon owned by another user")
	}

	renew("renew-removed", "renew-owner")
	if _, ok := readLease(t, rawDB, "renew-removed"); ok {
		t.Error("a renewal resurrected a lease for a daemon with no registry row")
	}

	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "renew-owned", UserID: "renew-owner", Source: DaemonAttachmentSourceInbound,
		AttachedAt: at, LastStreamActivity: at,
	}); err != nil {
		t.Fatalf("seed owner's lease: %v", err)
	}
	renew("renew-owned", "someone-else")
	if got, _ := readLease(t, rawDB, "renew-owned"); got.userID != "renew-owner" || !got.activity.Equal(at) || !got.attachedAt.Equal(at) {
		t.Errorf("a foreign renewal changed the owner's lease: %+v", got)
	}
}

// A session's release deletes its own lease (or an older one) and never a
// newer session's: the drained replica tears its half of the stream down
// after the daemon already re-attached elsewhere. Deleting by daemon id alone
// removed the live session's lease.
func TestReleaseDaemonAttachmentKeepsANewerSessionsLease(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	old := time.Date(2026, 10, 11, 1, 0, 0, 123456000, time.UTC)
	newer := time.Date(2026, 10, 11, 2, 56, 37, 864000000, time.UTC)
	upsert := func(attachedAt time.Time) {
		t.Helper()
		if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
			DaemonID: "lease-fence", UserID: "lease-user", Source: DaemonAttachmentSourceInbound,
			AttachedAt: attachedAt, LastStreamActivity: attachedAt,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	release := func(attachedAt time.Time) {
		t.Helper()
		if err := repo.ReleaseDaemonAttachment(ctx, "lease-fence", attachedAt); err != nil {
			t.Fatalf("release: %v", err)
		}
	}

	upsert(newer)
	release(old)
	if _, ok := readLease(t, rawDB, "lease-fence"); !ok {
		t.Fatal("the old session's teardown deleted the new session's lease")
	}

	release(newer)
	if _, ok := readLease(t, rawDB, "lease-fence"); ok {
		t.Fatal("a session's own release must delete its lease")
	}

	// A row left by an older session (this session's connect upsert never
	// landed) is released with it.
	upsert(old)
	release(newer)
	if _, ok := readLease(t, rawDB, "lease-fence"); ok {
		t.Fatal("a release must also clear a lease older than its session")
	}

	if err := repo.ReleaseDaemonAttachment(ctx, "lease-fence", time.Time{}); err == nil {
		t.Error("a release that names no session must be refused, not delete by daemon id alone")
	}
}

// Releasing a lease keeps when the daemon was last heard from. A gateway
// rollout drains every stream (prod 2026-10-10: ten in one day), and the
// release used to erase the only trace that the daemon had been there seconds
// earlier, so a tool call could not tell a daemon about to redial from a
// laptop closed last week.
func TestReleaseDaemonAttachmentRecordsWhenTheDaemonWasLastSeen(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const (
		userID   = "seen-user"
		daemonID = "seen-daemon"
	)
	seedLeaseDaemon(t, repo, daemonID, userID)
	lastSeen := func(user string) time.Time {
		t.Helper()
		seen, err := repo.DaemonLastSeenAt(ctx, user, daemonID)
		if err != nil {
			t.Fatalf("DaemonLastSeenAt: %v", err)
		}
		return seen
	}
	lease := func(attachedAt, activity time.Time) {
		t.Helper()
		if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
			DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
			AttachedAt: attachedAt, LastStreamActivity: activity,
		}); err != nil {
			t.Fatalf("upsert lease: %v", err)
		}
	}
	release := func(attachedAt time.Time) {
		t.Helper()
		if err := repo.ReleaseDaemonAttachment(ctx, daemonID, attachedAt); err != nil {
			t.Fatalf("release: %v", err)
		}
	}

	if seen := lastSeen(userID); !seen.IsZero() {
		t.Fatalf("a daemon never attached was last seen at %v, want never", seen)
	}

	session := time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC)
	heartbeat := time.Date(2026, 10, 10, 16, 53, 45, 0, time.UTC)
	lease(session, heartbeat)
	if seen := lastSeen(userID); !seen.Equal(heartbeat) {
		t.Errorf("while the lease exists last seen = %v, want its last activity %v", seen, heartbeat)
	}
	if seen := lastSeen("someone-else"); !seen.IsZero() {
		t.Errorf("another user read this daemon as last seen at %v", seen)
	}

	release(session)
	if attached, _ := repo.IsDaemonIDAttached(ctx, daemonID, 90*time.Second); attached {
		t.Fatal("precondition: the release must delete the lease")
	}
	if seen := lastSeen(userID); !seen.Equal(heartbeat) {
		t.Errorf("after the release last seen = %v, want the released lease's last activity %v", seen, heartbeat)
	}

	// The next session runs longer; its release moves last seen on.
	next := time.Date(2026, 10, 10, 16, 54, 24, 0, time.UTC)
	nextHeartbeat := next.Add(10 * time.Minute)
	lease(next, nextHeartbeat)
	release(next)
	if seen := lastSeen(userID); !seen.Equal(nextHeartbeat) {
		t.Errorf("after the second release last seen = %v, want %v", seen, nextHeartbeat)
	}

	// A row a superseded session left behind, released late, never rewinds it.
	lease(session, heartbeat)
	release(session)
	if seen := lastSeen(userID); !seen.Equal(nextHeartbeat) {
		t.Errorf("an older lease's release rewound last seen to %v, want %v", seen, nextHeartbeat)
	}
}

// A renewal adopts the newer session, so the older session's later release
// cannot delete the row the live stream is renewing. Without it, a new
// session whose connect upsert failed renewed the old session's row in place,
// and the old replica's teardown deleted it out from under the live stream.
func TestRenewDaemonAttachmentAdoptsTheNewerSession(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	seedLeaseDaemon(t, repo, "lease-adopt", "lease-user")
	old := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 11, 2, 56, 37, 0, time.UTC)
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "lease-adopt", UserID: "lease-user", Source: DaemonAttachmentSourceInbound,
		AttachedAt: old, LastStreamActivity: old,
	}); err != nil {
		t.Fatalf("old session upsert: %v", err)
	}
	if _, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "lease-adopt", UserID: "lease-user", Source: DaemonAttachmentSourceInbound,
		AttachedAt: newer, LastStreamActivity: newer.Add(15 * time.Second),
	}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := repo.ReleaseDaemonAttachment(ctx, "lease-adopt", old); err != nil {
		t.Fatalf("old session release: %v", err)
	}
	got, ok := readLease(t, rawDB, "lease-adopt")
	if !ok {
		t.Fatal("the old session's release deleted the lease the live session is renewing")
	}
	if !got.attachedAt.Equal(newer) {
		t.Errorf("attached_at = %v, want the live session's %v", got.attachedAt, newer)
	}

	// The old session's own late renewal (a heartbeat its stalled stream
	// buffered) never takes the row back.
	if _, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "lease-adopt", UserID: "lease-user", Source: DaemonAttachmentSourceInbound,
		AttachedAt: old, LastStreamActivity: newer.Add(20 * time.Second),
	}); err != nil {
		t.Fatalf("late renew: %v", err)
	}
	if got, _ := readLease(t, rawDB, "lease-adopt"); !got.attachedAt.Equal(newer) {
		t.Errorf("a superseded session's renewal moved attached_at back to %v", got.attachedAt)
	}
}
