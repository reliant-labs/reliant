// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"
)

// These run the real SQL. The registry writes in daemon_registry.go carry
// their safety in SQL — the conditional ON CONFLICT, the owner scoping, and the
// two guards on snapshot removal — and only executing it proves any of that.
// The consumer's routing is covered in internal/daemonstate/registry_test.go.

func registryTestRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	return repo, rawDB
}

func mustGetDaemon(t *testing.T, repo *Repo, id string) *Daemon {
	t.Helper()
	d, err := repo.GetDaemon(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDaemon(%s): %v", id, err)
	}
	return d
}

func daemonExists(t *testing.T, repo *Repo, id string) bool {
	t.Helper()
	_, err := repo.GetDaemon(context.Background(), id)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("GetDaemon(%s): %v", id, err)
	}
	return true
}

// TestUpsertDaemonIdentity_CreatesNamedRowReadByBothReaders: the create-time
// event must produce a row both daemon readers return with its name — the
// Machines page reads ListDaemonsByUserID, detail reads GetDaemon.
func TestUpsertDaemonIdentity_CreatesNamedRowReadByBothReaders(t *testing.T) {
	repo, _ := registryTestRepo(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 7, 14, 41, 36, 0, time.UTC)

	wrote, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{
		DaemonID: "2aab1465", UserID: "u-owner", Name: "default", DaemonType: "managed", CreatedAt: created,
	})
	if err != nil || !wrote {
		t.Fatalf("UpsertDaemonIdentity = %v, %v; want a written row", wrote, err)
	}

	got := mustGetDaemon(t, repo, "2aab1465")
	if got.Name != "default" || got.UserID != "u-owner" || got.DaemonType == nil || *got.DaemonType != "managed" {
		t.Errorf("GetDaemon = name %q user %q type %v", got.Name, got.UserID, got.DaemonType)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want the control plane's %v", got.CreatedAt, created)
	}
	list, err := repo.ListDaemonsByUserID(ctx, "u-owner")
	if err != nil || len(list) != 1 || list[0].Name != "default" {
		t.Fatalf("ListDaemonsByUserID = %+v, %v; want the named row", list, err)
	}

	// Re-asserting an unchanged identity is a no-op, not a write: the
	// snapshot re-asserts every daemon each sweep.
	wrote, err = repo.UpsertDaemonIdentity(ctx, DaemonIdentity{
		DaemonID: "2aab1465", UserID: "u-owner", Name: "default", DaemonType: "managed", CreatedAt: created,
	})
	if err != nil || wrote {
		t.Errorf("unchanged re-assert = %v, %v; want no write", wrote, err)
	}
}

// TestUpsertDaemonIdentity_NamesARowTheGatewayRegistered: the common prod
// case after deploy — the row exists from the daemon's own registration
// (hostname ws-ws-…, no name) and the control plane supplies the name. The
// daemon's own fields must survive, and the owner must never be rewritten.
func TestUpsertDaemonIdentity_NamesARowTheGatewayRegistered(t *testing.T) {
	repo, _ := registryTestRepo(t)
	ctx := context.Background()
	hostname := "ws-ws-2aab1465"
	managed := "managed"
	if err := repo.UpsertDaemon(ctx, &Daemon{ID: "2aab1465", UserID: "u-owner", Hostname: &hostname, DaemonType: &managed}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	wrote, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{
		DaemonID: "2aab1465", UserID: "u-intruder", Name: "default", DaemonType: "self_hosted",
	})
	if err != nil || !wrote {
		t.Fatalf("naming an existing row = %v, %v", wrote, err)
	}
	got := mustGetDaemon(t, repo, "2aab1465")
	if got.Name != "default" {
		t.Errorf("name = %q, want default", got.Name)
	}
	if got.UserID != "u-owner" {
		t.Errorf("user_id rewritten to %q; a mirror must never move a row between owners in place", got.UserID)
	}
	if got.Hostname == nil || *got.Hostname != hostname {
		t.Errorf("hostname = %v, want the daemon's own registration kept", got.Hostname)
	}
	if *got.DaemonType != "managed" {
		t.Errorf("daemon_type = %q, want the registered type kept", *got.DaemonType)
	}
}

// TestRemoveDaemon_DeletesRowAndLease is the ghost: the row and its lease go,
// the owner is reported back, and removal is idempotent.
func TestRemoveDaemon_DeletesRowAndLease(t *testing.T) {
	repo, rawDB := registryTestRepo(t)
	ctx := context.Background()
	if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: "cda8a89b", UserID: "u-owner", Name: "default", DaemonType: "managed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "cda8a89b", UserID: "u-owner", Source: DaemonAttachmentSourceInbound,
		AttachedAt: time.Now().UTC(), LastStreamActivity: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	userID, removed, err := repo.RemoveDaemon(ctx, "cda8a89b")
	if err != nil || !removed || userID != "u-owner" {
		t.Fatalf("RemoveDaemon = %q, %v, %v; want u-owner, true", userID, removed, err)
	}
	if daemonExists(t, repo, "cda8a89b") {
		t.Fatal("row survived RemoveDaemon")
	}
	var leases int
	if err := rawDB.QueryRow(`SELECT count(*) FROM daemon_attachment WHERE daemon_id = $1`, "cda8a89b").Scan(&leases); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leases != 0 {
		t.Errorf("attachment lease survived removal")
	}

	_, removed, err = repo.RemoveDaemon(ctx, "cda8a89b")
	if err != nil || removed {
		t.Errorf("second RemoveDaemon = %v, %v; want idempotent no-op", removed, err)
	}
}

// TestApplyDaemonRegistrySnapshot_ReconcilesOneOwner reproduces the owner's
// prod drift on 2026-10-07 against real SQL: the managed ghost cda8a89b and
// self-hosted ghost 38181976 (both deleted in the control plane) must go; the
// suspended "test" machine the registry never had must appear with its phase;
// a connected-but-unlisted daemon, a just-registered one, and another owner's
// machine must all be left alone.
func TestApplyDaemonRegistrySnapshot_ReconcilesOneOwner(t *testing.T) {
	repo, rawDB := registryTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	longAgo := now.Add(-30 * 24 * time.Hour)

	seed := func(id, user string, created time.Time) {
		t.Helper()
		if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: id, UserID: user, DaemonType: "managed", CreatedAt: created}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("cda8a89b", "u-owner", longAgo)
	seed("38181976", "u-owner", longAgo)
	seed("2aab1465", "u-owner", longAgo)
	seed("laptop", "u-owner", longAgo)
	seed("just-registered", "u-owner", now)
	seed("someone-else", "u-other", longAgo)
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: "laptop", UserID: "u-owner", Source: DaemonAttachmentSourceInbound,
		AttachedAt: now, LastStreamActivity: now,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	// A STALE lease must not protect a ghost: only a live connection is
	// evidence the daemon is real.
	if _, err := rawDB.Exec(`INSERT INTO daemon_attachment (daemon_id, user_id, source, attached_at, last_stream_activity)
		VALUES ($1, $2, 'inbound', $3, $3)`, "cda8a89b", "u-owner", longAgo); err != nil {
		t.Fatalf("seed stale lease: %v", err)
	}

	suspendedAt := now.Add(-15 * 24 * time.Hour)
	result, err := repo.ApplyDaemonRegistrySnapshot(ctx, RegistrySnapshotApply{
		UserID: "u-owner",
		Daemons: []RegistrySnapshotDaemon{
			{Identity: DaemonIdentity{DaemonID: "2aab1465", Name: "default", DaemonType: "managed", CreatedAt: longAgo}},
			{
				Identity:  DaemonIdentity{DaemonID: "dea98d67", Name: "test", DaemonType: "managed", CreatedAt: longAgo},
				Lifecycle: &DaemonLifecycleUpdate{Phase: "suspended", Size: "small", ChangedAt: suspendedAt},
			},
		},
		KeepCreatedAfter:  now.Add(-10 * time.Minute),
		KeepAttachedSince: now.Add(-90 * time.Second),
	})
	if err != nil {
		t.Fatalf("ApplyDaemonRegistrySnapshot: %v", err)
	}

	sort.Strings(result.Removed)
	if want := []string{"38181976", "cda8a89b"}; len(result.Removed) != 2 || result.Removed[0] != want[0] || result.Removed[1] != want[1] {
		t.Errorf("removed = %v, want %v", result.Removed, want)
	}
	for _, ghost := range []string{"cda8a89b", "38181976"} {
		if daemonExists(t, repo, ghost) {
			t.Errorf("%s survived the snapshot", ghost)
		}
	}
	for _, kept := range []string{"laptop", "just-registered", "someone-else", "2aab1465"} {
		if !daemonExists(t, repo, kept) {
			t.Errorf("%s was removed; it must survive", kept)
		}
	}
	created := mustGetDaemon(t, repo, "dea98d67")
	if created.Name != "test" || created.UserID != "u-owner" ||
		created.LifecyclePhase == nil || *created.LifecyclePhase != "suspended" ||
		created.Size == nil || *created.Size != "small" {
		t.Errorf("created row = %+v, want test/u-owner/suspended/small", created)
	}
	if mustGetDaemon(t, repo, "2aab1465").Name != "default" {
		t.Error("snapshot did not name the existing machine")
	}

	// A second, identical snapshot changes nothing.
	again, err := repo.ApplyDaemonRegistrySnapshot(ctx, RegistrySnapshotApply{
		UserID: "u-owner",
		Daemons: []RegistrySnapshotDaemon{
			{Identity: DaemonIdentity{DaemonID: "2aab1465", Name: "default", DaemonType: "managed", CreatedAt: longAgo}},
			{
				Identity:  DaemonIdentity{DaemonID: "dea98d67", Name: "test", DaemonType: "managed", CreatedAt: longAgo},
				Lifecycle: &DaemonLifecycleUpdate{Phase: "suspended", Size: "small", ChangedAt: suspendedAt},
			},
			{Identity: DaemonIdentity{DaemonID: "laptop", DaemonType: "managed", CreatedAt: longAgo}},
			{Identity: DaemonIdentity{DaemonID: "just-registered", DaemonType: "managed", CreatedAt: now}},
		},
		KeepCreatedAfter:  now.Add(-10 * time.Minute),
		KeepAttachedSince: now.Add(-90 * time.Second),
	})
	if err != nil || again.Changed() {
		t.Errorf("identical re-apply = %+v, %v; want no change", again, err)
	}
}

// TestApplyDaemonRegistrySnapshot_EmptySetRemovesEveryGhost: an owner whose
// machines are all deleted gets an empty snapshot. "id = ANY(empty)" must
// select every row, not none — an empty or NULL array binding that matched
// nothing would leave all of their ghosts listed forever.
func TestApplyDaemonRegistrySnapshot_EmptySetRemovesEveryGhost(t *testing.T) {
	repo, _ := registryTestRepo(t)
	ctx := context.Background()
	longAgo := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for _, id := range []string{"ghost-a", "ghost-b"} {
		if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: id, UserID: "u-owner", DaemonType: "self_hosted", CreatedAt: longAgo}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, daemons := range [][]RegistrySnapshotDaemon{nil, {}} {
		if _, err := repo.ApplyDaemonRegistrySnapshot(ctx, RegistrySnapshotApply{
			UserID: "u-owner", Daemons: daemons,
			KeepCreatedAfter: time.Now().UTC().Add(-10 * time.Minute), KeepAttachedSince: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("ApplyDaemonRegistrySnapshot: %v", err)
		}
	}
	for _, id := range []string{"ghost-a", "ghost-b"} {
		if daemonExists(t, repo, id) {
			t.Errorf("%s survived an empty snapshot", id)
		}
	}
}
