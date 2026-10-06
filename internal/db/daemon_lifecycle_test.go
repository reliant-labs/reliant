// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// These tests run the real SQL, not a fake. The ordering guard in
// ApplyDaemonLifecycle lives in a WHERE clause specifically so it holds across
// replicas and retries rather than only within one consumer goroutine, and a
// guard expressed in SQL can only be verified by executing SQL. The consumer's
// dispatch behaviour is covered separately in internal/daemonstate.
func seedLifecycleDaemon(t *testing.T, repo *Repo, id, userID string) {
	t.Helper()
	hostname := "lifecycle-host"
	daemonType := "managed"
	if err := repo.UpsertDaemon(context.Background(), &Daemon{
		ID: id, UserID: userID, Hostname: &hostname, DaemonType: &daemonType,
	}); err != nil {
		t.Fatalf("seed daemon: %v", err)
	}
}

// TestApplyDaemonLifecycle_StoresAndRoundTrips pins that a mirrored lifecycle
// observation survives the write and is read back by BOTH daemon readers.
// Reading it back through GetDaemon and ListDaemonsByUserID is the point: the
// two queries select the same six columns independently, and two readers of one
// concept disagreeing is the exact defect this whole change set exists to
// remove.
func TestApplyDaemonLifecycle_StoresAndRoundTrips(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "lifecycle-user"
	const daemonID = "lifecycle-daemon"
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID) })
	seedLifecycleDaemon(t, repo, daemonID, userID)

	changedAt := time.Now().UTC().Truncate(time.Millisecond)
	oomAt := changedAt.Add(-time.Hour)
	updated, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID:        daemonID,
		Phase:           "cloning",
		Size:            "medium",
		StatusMessage:   "cloning your repository",
		ChangedAt:       changedAt,
		LastOOMKilledAt: &oomAt,
		OOMKillCount:    2,
	})
	if err != nil {
		t.Fatalf("ApplyDaemonLifecycle: %v", err)
	}
	if !updated {
		t.Fatal("ApplyDaemonLifecycle reported no row updated for a seeded daemon")
	}

	assert := func(label string, d *Daemon) {
		t.Helper()
		if d.LifecyclePhase == nil || *d.LifecyclePhase != "cloning" {
			t.Errorf("%s: lifecycle_phase = %v, want \"cloning\"", label, d.LifecyclePhase)
		}
		if d.Size == nil || *d.Size != "medium" {
			t.Errorf("%s: size = %v, want \"medium\"", label, d.Size)
		}
		if d.LastStatusMessage != "cloning your repository" {
			t.Errorf("%s: last_status_message = %q", label, d.LastStatusMessage)
		}
		if d.LastStatusChangedAt == nil || !d.LastStatusChangedAt.Equal(changedAt) {
			t.Errorf("%s: last_status_changed_at = %v, want %v", label, d.LastStatusChangedAt, changedAt)
		}
		if d.LastOOMKilledAt == nil || !d.LastOOMKilledAt.Equal(oomAt) {
			t.Errorf("%s: last_oom_killed_at = %v, want %v", label, d.LastOOMKilledAt, oomAt)
		}
		if d.OOMKillCount != 2 {
			t.Errorf("%s: oom_kill_count = %d, want 2", label, d.OOMKillCount)
		}
	}

	got, err := repo.GetDaemon(ctx, daemonID)
	if err != nil {
		t.Fatalf("GetDaemon: %v", err)
	}
	assert("GetDaemon", got)

	list, err := repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("ListDaemonsByUserID: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListDaemonsByUserID returned %d rows, want 1", len(list))
	}
	assert("ListDaemonsByUserID", list[0])
}

// TestApplyDaemonLifecycle_RejectsStaleEvents is the guard that makes mirroring
// safe at all. Lifecycle events ride plain NATS — newest wins, no redelivery,
// no ordering guarantee — so a delayed PROVISIONING can arrive after the READY
// that superseded it. Without this comparison a ready machine would render as
// still provisioning until its next transition, which for a healthy machine may
// be hours away.
func TestApplyDaemonLifecycle_RejectsStaleEvents(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "lifecycle-stale-user"
	const daemonID = "lifecycle-stale-daemon"
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID) })
	seedLifecycleDaemon(t, repo, daemonID, userID)

	late := time.Now().UTC().Truncate(time.Millisecond)
	early := late.Add(-2 * time.Minute)

	if _, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "ready", ChangedAt: late,
	}); err != nil {
		t.Fatalf("apply ready: %v", err)
	}

	updated, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "provisioning", ChangedAt: early,
	})
	if err != nil {
		t.Fatalf("apply stale: %v", err)
	}
	if updated {
		t.Error("a stale lifecycle event reported itself as applied")
	}

	got, err := repo.GetDaemon(ctx, daemonID)
	if err != nil {
		t.Fatalf("GetDaemon: %v", err)
	}
	if *got.LifecyclePhase != "ready" {
		t.Fatalf("stale event regressed lifecycle_phase to %q; want \"ready\"", *got.LifecyclePhase)
	}

	// Equal timestamps must also be rejected: the guard is strictly newer, so a
	// redelivered duplicate cannot rewrite the row it already wrote.
	updated, err = repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "failed", ChangedAt: late,
	})
	if err != nil {
		t.Fatalf("apply duplicate: %v", err)
	}
	if updated {
		t.Error("a duplicate lifecycle event at the same timestamp was applied")
	}
}

// TestApplyDaemonLifecycle_PreservesSizeAndOOMOnPhaseChange covers COALESCE and
// GREATEST. A lifecycle event that omits size or OOM data is reporting a phase
// change, not asserting the machine has no size and has never been OOM-killed.
// Without these the ordinary provisioning→ready transition would erase the OOM
// history the UI renders.
func TestApplyDaemonLifecycle_PreservesSizeAndOOMOnPhaseChange(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "lifecycle-preserve-user"
	const daemonID = "lifecycle-preserve-daemon"
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID) })
	seedLifecycleDaemon(t, repo, daemonID, userID)

	t0 := time.Now().UTC().Truncate(time.Millisecond)
	oomAt := t0.Add(-time.Hour)
	if _, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "ready", Size: "large",
		ChangedAt: t0, LastOOMKilledAt: &oomAt, OOMKillCount: 3,
	}); err != nil {
		t.Fatalf("apply initial: %v", err)
	}

	// A bare phase transition, carrying neither size nor OOM data.
	if _, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "suspended", ChangedAt: t0.Add(time.Minute),
	}); err != nil {
		t.Fatalf("apply transition: %v", err)
	}

	got, err := repo.GetDaemon(ctx, daemonID)
	if err != nil {
		t.Fatalf("GetDaemon: %v", err)
	}
	if got.Size == nil || *got.Size != "large" {
		t.Errorf("size = %v after a bare phase change, want \"large\" preserved", got.Size)
	}
	if got.OOMKillCount != 3 {
		t.Errorf("oom_kill_count = %d after a bare phase change, want 3 preserved", got.OOMKillCount)
	}
	if got.LastOOMKilledAt == nil || !got.LastOOMKilledAt.Equal(oomAt) {
		t.Errorf("last_oom_killed_at = %v after a bare phase change, want %v preserved", got.LastOOMKilledAt, oomAt)
	}
	if *got.LifecyclePhase != "suspended" {
		t.Errorf("lifecycle_phase = %q, want \"suspended\"", *got.LifecyclePhase)
	}
}

// TestApplyDaemonLifecycle_UnregisteredDaemonIsNoOp pins that lifecycle events
// do NOT create identity rows. Managed machines exist in control-plane from
// CreateDaemon and do not exist here until they register, so the first
// lifecycle events for every managed machine have no row to land on. Creating
// one from a lifecycle event would make this table a projection of a stream it
// does not own — and it would be a row with no hostname, platform or project
// paths, which every reader treats as a real daemon.
func TestApplyDaemonLifecycle_UnregisteredDaemonIsNoOp(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	updated, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: "lifecycle-never-registered", Phase: "provisioning",
		ChangedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ApplyDaemonLifecycle on an absent row returned an error: %v", err)
	}
	if updated {
		t.Fatal("ApplyDaemonLifecycle created a row for a daemon that never registered")
	}
	if _, err := repo.GetDaemon(ctx, "lifecycle-never-registered"); err == nil {
		t.Fatal("a daemons row exists for a daemon that only ever had a lifecycle event")
	}
}

// TestApplyDaemonLifecycle_RejectsIncompleteUpdates pins the two preconditions
// that cannot be defaulted. ChangedAt in particular is not merely a timestamp
// to store — it is the ordering key the guard compares against, so a zero value
// would make every subsequent event look newer and silently defeat the
// out-of-order protection above.
func TestApplyDaemonLifecycle_RejectsIncompleteUpdates(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		Phase: "ready", ChangedAt: time.Now().UTC(),
	}); err == nil {
		t.Error("an empty daemon ID was accepted")
	}
	if _, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: "d-1", Phase: "ready",
	}); err == nil {
		t.Error("a zero ChangedAt was accepted; the ordering guard would be defeated")
	}
}

// A NULL mirror must accept a lifecycle event whatever its timestamp: the
// ordering guard protects a phase that exists, and there is none to protect.
func TestApplyDaemonLifecycle_WritesWhenPhaseNullRegardlessOfTimestamp(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "lifecycle-nullphase-user"
	const daemonID = "lifecycle-nullphase-daemon"
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemons WHERE user_id = $1`, userID) })
	seedLifecycleDaemon(t, repo, daemonID, userID)

	newer := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := rawDB.Exec(`UPDATE daemons SET lifecycle_phase = NULL, last_status_changed_at = $1 WHERE id = $2`, newer, daemonID); err != nil {
		t.Fatalf("seed null phase: %v", err)
	}

	updated, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: "suspended", ChangedAt: newer.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !updated {
		t.Fatal("event was dropped although lifecycle_phase is NULL")
	}
	got, err := repo.GetDaemon(ctx, daemonID)
	if err != nil || got.LifecyclePhase == nil || *got.LifecyclePhase != "suspended" {
		t.Fatalf("lifecycle_phase not written: %v %v", got, err)
	}
}
