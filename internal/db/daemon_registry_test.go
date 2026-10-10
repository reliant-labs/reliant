// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
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

// seedMachineBindings binds one chat (pinned), one worktree (owned) and one
// project install to each daemon, all in the seeded test-project. Rows are
// named after their daemon: chat-<id>, wt-<id>.
func seedMachineBindings(t *testing.T, rawDB *sql.DB, userID string, daemonIDs ...string) {
	t.Helper()
	for _, id := range daemonIDs {
		if _, err := rawDB.Exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active, active_daemon_id)
			VALUES ($1, 't', 'test-project', $2, now(), now(), now(), $3)`, "chat-"+id, userID, id); err != nil {
			t.Fatalf("seed chat on %s: %v", id, err)
		}
		if _, err := rawDB.Exec(`INSERT INTO worktrees (id, name, path, branch, base_branch, project_id, created_at, updated_at, last_active, daemon_id)
			VALUES ($1, $2, $3, $2, 'main', 'test-project', now(), now(), now(), $4)`, "wt-"+id, "branch-"+id, "/w/"+id, id); err != nil {
			t.Fatalf("seed worktree on %s: %v", id, err)
		}
		if _, err := rawDB.Exec(`INSERT INTO project_daemons (project_id, daemon_id, path) VALUES ('test-project', $1, '/p')`, id); err != nil {
			t.Fatalf("seed project install on %s: %v", id, err)
		}
	}
}

// machineBindings reads back what seedMachineBindings bound to daemonID: the
// chat's pin, the worktree's owner, the project installs on it, and the
// chat_config_changed updates its chat's owner was sent.
type machineBindings struct {
	chatPin       sql.NullString
	worktreeOwner sql.NullString
	installs      int
	configUpdates []string
}

func readMachineBindings(t *testing.T, rawDB *sql.DB, daemonID string) machineBindings {
	t.Helper()
	var b machineBindings
	if err := rawDB.QueryRow(`SELECT active_daemon_id FROM chats WHERE id = $1`, "chat-"+daemonID).Scan(&b.chatPin); err != nil {
		t.Fatalf("read chat pin: %v", err)
	}
	if err := rawDB.QueryRow(`SELECT daemon_id FROM worktrees WHERE id = $1`, "wt-"+daemonID).Scan(&b.worktreeOwner); err != nil {
		t.Fatalf("read worktree owner: %v", err)
	}
	if err := rawDB.QueryRow(`SELECT count(*) FROM project_daemons WHERE daemon_id = $1`, daemonID).Scan(&b.installs); err != nil {
		t.Fatalf("count installs: %v", err)
	}
	rows, err := rawDB.Query(`SELECT data FROM user_updates WHERE chat_id = $1 AND update_type = $2 ORDER BY sequence_number`,
		"chat-"+daemonID, int64(UserUpdateChatConfigChanged))
	if err != nil {
		t.Fatalf("read user updates: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			t.Fatalf("scan user update: %v", err)
		}
		b.configUpdates = append(b.configUpdates, data)
	}
	return b
}

// assertReleased: nothing still points at a daemon that no longer exists,
// and the pinned chat's clients were told it is unpinned.
func assertReleased(t *testing.T, rawDB *sql.DB, daemonID string) {
	t.Helper()
	b := readMachineBindings(t, rawDB, daemonID)
	if b.chatPin.Valid {
		t.Errorf("chat still pinned to removed daemon %s", b.chatPin.String)
	}
	if b.worktreeOwner.Valid {
		t.Errorf("worktree still owned by removed daemon %s", b.worktreeOwner.String)
	}
	if b.installs != 0 {
		t.Errorf("%d project install(s) left on removed daemon %s", b.installs, daemonID)
	}
	if len(b.configUpdates) != 1 || !strings.Contains(b.configUpdates[0], `"active_daemon_id":""`) {
		t.Errorf("chat_config_changed for the unpinned chat = %q; want one clearing active_daemon_id", b.configUpdates)
	}
}

// assertKept: a daemon that still exists keeps everything bound to it.
func assertKept(t *testing.T, rawDB *sql.DB, daemonID string) {
	t.Helper()
	b := readMachineBindings(t, rawDB, daemonID)
	if b.chatPin.String != daemonID || b.worktreeOwner.String != daemonID || b.installs != 1 || len(b.configUpdates) != 0 {
		t.Errorf("bindings of surviving daemon %s = %+v; want untouched", daemonID, b)
	}
}

// TestRemoveDaemon_ReleasesWhatWasBoundToIt: a machine that no longer exists
// must not keep serving as a chat's pin, a worktree's owner or a project's
// install. Each of those resolved to a daemon nothing can reach — routing
// failed for good, and validateOwnedProjectDaemon refused every OTHER machine
// for the project — so a chat on a deleted (or re-identified) machine never
// recovered. Released, each is re-learned from live evidence: default
// resolution for the chat, the sweep's adoption for the worktree, the
// daemon's connect-time reconcile for the install.
func TestRemoveDaemon_ReleasesWhatWasBoundToIt(t *testing.T) {
	repo, rawDB := registryTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"gone-1", "kept-1"} {
		if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: id, UserID: "test-user", DaemonType: "managed"}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seedMachineBindings(t, rawDB, "test-user", "gone-1", "kept-1")

	if _, removed, err := repo.RemoveDaemon(ctx, "gone-1"); err != nil || !removed {
		t.Fatalf("RemoveDaemon = %v, %v; want removed", removed, err)
	}
	assertReleased(t, rawDB, "gone-1")
	assertKept(t, rawDB, "kept-1")

	// Idempotent: a second removal finds no row and tells no one again.
	if _, removed, err := repo.RemoveDaemon(ctx, "gone-1"); err != nil || removed {
		t.Fatalf("second RemoveDaemon = %v, %v; want idempotent no-op", removed, err)
	}
	assertReleased(t, rawDB, "gone-1")
}

// TestApplyDaemonRegistrySnapshot_ReleasesWhatWasBoundToARemovedRow: the
// reconcile path removes rows too, and must release them the same way.
func TestApplyDaemonRegistrySnapshot_ReleasesWhatWasBoundToARemovedRow(t *testing.T) {
	repo, rawDB := registryTestRepo(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-24 * time.Hour)
	for _, id := range []string{"ghost-1", "listed-1"} {
		if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: id, UserID: "test-user", DaemonType: "managed", CreatedAt: old}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seedMachineBindings(t, rawDB, "test-user", "ghost-1", "listed-1")

	now := time.Now().UTC()
	result, err := repo.ApplyDaemonRegistrySnapshot(ctx, RegistrySnapshotApply{
		UserID:            "test-user",
		Daemons:           []RegistrySnapshotDaemon{{Identity: DaemonIdentity{DaemonID: "listed-1", DaemonType: "managed", CreatedAt: old}}},
		KeepCreatedAfter:  now.Add(-10 * time.Minute),
		KeepAttachedSince: now.Add(-90 * time.Second),
	})
	if err != nil {
		t.Fatalf("ApplyDaemonRegistrySnapshot: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "ghost-1" {
		t.Fatalf("Removed = %v; want [ghost-1]", result.Removed)
	}
	assertReleased(t, rawDB, "ghost-1")
	assertKept(t, rawDB, "listed-1")
}

// releaseBindingsMigrationVersion is
// 20261010020219_release_bindings_to_removed_daemons.sql.
const releaseBindingsMigrationVersion int64 = 20261010020219

// TestReleaseBindingsMigration_HealsRowsLeftByEarlierRemovals: removals made
// before releaseDaemonBindings existed left their bindings dangling (in prod,
// one live worktree owner and 13 project installs). The migration releases
// exactly those, and nothing bound to a machine that still exists.
func TestReleaseBindingsMigration_HealsRowsLeftByEarlierRemovals(t *testing.T) {
	repo, rawDB := registryTestRepo(t)
	ctx := context.Background()
	if _, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: "kept-2", UserID: "test-user", DaemonType: "managed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// "gone-2" has bindings and no registry row: what a pre-release removal left.
	seedMachineBindings(t, rawDB, "test-user", "gone-2", "kept-2")

	if _, err := rawDB.Exec(fmt.Sprintf(`DELETE FROM %s WHERE version_id = $1`, goose.TableName()), releaseBindingsMigrationVersion); err != nil { //nolint:gosec // goose.TableName is a compile-time constant
		t.Fatalf("rewind: %v", err)
	}
	if err := initGoose(); err != nil {
		t.Fatalf("init goose: %v", err)
	}
	if err := goose.UpTo(rawDB, migrationsDir, releaseBindingsMigrationVersion, goose.WithAllowMissing()); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	b := readMachineBindings(t, rawDB, "gone-2")
	if b.chatPin.Valid || b.worktreeOwner.Valid || b.installs != 0 {
		t.Errorf("bindings to the removed daemon after the migration = %+v; want released", b)
	}
	assertKept(t, rawDB, "kept-2")
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
