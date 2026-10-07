// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/db"
)

// These tests pin the three ways the registry learns what the control plane
// knows about a machine's EXISTENCE — the gap behind MACHINE_LIST_BUGS_2026-10-07:
// a machine deleted in the control plane stayed in this registry forever, and a
// machine created there was absent from it until its pod reached the gateway.
// The SQL behind each write is pinned against real Postgres in internal/db
// (daemon_registry_test.go); what these own is that the consumer routes each
// event to the right write and tells the owner's clients.

// UpsertDaemonIdentity models the SQL: create when absent, fill the name when
// present, never rewrite the owner.
func (f *fakeRepo) UpsertDaemonIdentity(_ context.Context, id db.DaemonIdentity) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.daemons[id.DaemonID]
	if !ok {
		dt := id.DaemonType
		f.daemons[id.DaemonID] = &db.Daemon{
			ID: id.DaemonID, UserID: id.UserID, Name: id.Name, DaemonType: &dt, CreatedAt: id.CreatedAt,
		}
		return true, nil
	}
	if id.Name != "" && row.Name != id.Name {
		row.Name = id.Name
		return true, nil
	}
	return false, nil
}

func (f *fakeRepo) RemoveDaemon(_ context.Context, daemonID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removeLocked(daemonID)
}

func (f *fakeRepo) removeLocked(daemonID string) (string, bool, error) {
	delete(f.rows, daemonID)
	row, ok := f.daemons[daemonID]
	if !ok {
		return "", false, nil
	}
	delete(f.daemons, daemonID)
	return row.UserID, true, nil
}

// ApplyDaemonRegistrySnapshot models the SQL's semantics (owner-scoped upsert,
// then removal of absent rows behind the two guards) closely enough that a
// consumer which passes the wrong guard values fails here too.
func (f *fakeRepo) ApplyDaemonRegistrySnapshot(ctx context.Context, snap db.RegistrySnapshotApply) (db.RegistrySnapshotResult, error) {
	var result db.RegistrySnapshotResult
	keep := map[string]bool{}
	for _, d := range snap.Daemons {
		keep[d.Identity.DaemonID] = true
		wrote, _ := f.UpsertDaemonIdentity(ctx, d.Identity)
		if d.Lifecycle != nil {
			applied, _ := f.ApplyDaemonLifecycle(ctx, *d.Lifecycle)
			wrote = wrote || applied
		}
		if wrote {
			result.Upserted = append(result.Upserted, d.Identity.DaemonID)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, snap)
	for id, row := range f.daemons {
		if row.UserID != snap.UserID || keep[id] {
			continue
		}
		if !row.CreatedAt.Before(snap.KeepCreatedAfter) {
			continue
		}
		if att, ok := f.rows[id]; ok && att.LastStreamActivity.After(snap.KeepAttachedSince) {
			continue
		}
		if _, removed, _ := f.removeLocked(id); removed {
			result.Removed = append(result.Removed, id)
		}
	}
	return result, nil
}

type notifyCall struct{ userID, daemonID string }

type notifyRecorder struct {
	mu    sync.Mutex
	calls []notifyCall
}

func (n *notifyRecorder) record(_ context.Context, userID, daemonID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{userID, daemonID})
}

func (n *notifyRecorder) snapshot() []notifyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifyCall(nil), n.calls...)
}

// startRegistryDerivation boots a Derivation with a notifier and returns the
// connection so tests can publish both event and snapshot subjects. wantSubs
// is how many subscriptions the test needs live before it publishes: 1 for the
// per-daemon event family (subscribed first), 2 to include the snapshot.
func startRegistryDerivation(t *testing.T, repo *fakeRepo, wantSubs int) (*nats.Conn, *notifyRecorder) {
	t.Helper()
	nc := startTestNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &notifyRecorder{}

	d := NewDerivation(nc, repo)
	d.NotifyLifecycleApplied(rec.record)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	// The subscriptions a test publishes to must be live before it publishes:
	// plain NATS drops what arrives first.
	if !waitFor(t, time.Second, func() bool { return nc.NumSubscriptions() >= wantSubs }) {
		cancel()
		t.Fatalf("derivation registered %d subscriptions, want %d (the event family, then the registry snapshot)", nc.NumSubscriptions(), wantSubs)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Start returned err: %v", err)
		}
	})
	return nc, rec
}

func publishSnapshot(t *testing.T, nc *nats.Conn, snap RegistrySnapshot) {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := nc.Publish(SubjectRegistrySnapshot, body); err != nil {
		t.Fatalf("publish snapshot: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// TestDerivation_RemovedEventDeletesRegistryRow is the ghost machine. The
// control plane soft-deleted cda8a89b on 2026-10-06; nothing told this
// registry, which kept listing it ("Starting" on Projects, "Pending" on
// Machines) and offered it as the clone target. A removed event must take the
// row — and its attachment lease — out, and tell the owner's clients, using
// the owner the row recorded because the event need not carry one.
func TestDerivation_RemovedEventDeletesRegistryRow(t *testing.T) {
	repo := newFakeRepo()
	phase := string(LifecyclePhaseProvisioning)
	repo.seedDaemon(&db.Daemon{ID: "cda8a89b", UserID: "u-owner", LifecyclePhase: &phase})
	repo.rows["cda8a89b"] = &db.DaemonAttachment{DaemonID: "cda8a89b", UserID: "u-owner"}
	nc, rec := startRegistryDerivation(t, repo, 1)

	publishEvent(t, nc, Event{DaemonID: "cda8a89b", Type: EventRemoved, At: time.Now().UTC()})

	if !waitFor(t, time.Second, func() bool {
		_, ok := repo.daemonSnapshot("cda8a89b")
		return !ok
	}) {
		t.Fatal("a removed event left the registry row in place: the machine stays listed forever")
	}
	if _, ok := repo.snapshot("cda8a89b"); ok {
		t.Error("removed daemon kept its attachment lease")
	}
	if !waitFor(t, time.Second, func() bool { return len(rec.snapshot()) == 1 }) {
		t.Fatalf("owner's clients were not told: notify calls = %+v", rec.snapshot())
	}
	if got := rec.snapshot()[0]; got.userID != "u-owner" || got.daemonID != "cda8a89b" {
		t.Errorf("notify = %+v, want the row's owner and daemon", got)
	}
}

// TestDerivation_RemovedEventForUnknownDaemonIsQuiet: removal is idempotent and
// a removal for a machine this registry never held is normal (one that failed
// before it ever registered). Neither is an error, and neither notifies.
func TestDerivation_RemovedEventForUnknownDaemonIsQuiet(t *testing.T) {
	repo := newFakeRepo()
	nc, rec := startRegistryDerivation(t, repo, 1)

	publishEvent(t, nc, Event{DaemonID: "never-registered", Type: EventRemoved, At: time.Now().UTC()})
	time.Sleep(100 * time.Millisecond)
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("removing nothing notified: %+v", calls)
	}
}

// TestDerivation_IdentityLifecycleCreatesRowWithName is the invisible new
// machine. "default" (2aab1465) was created at 14:41:36 and had no registry
// row until its pod reached the gateway at 14:45:26, so for the four slowest,
// most anxious minutes the Machines page did not list it. The control plane's
// create-time PROVISIONING event now carries the owner, type and name; it must
// create the row, provisioning, under the name the user typed.
func TestDerivation_IdentityLifecycleCreatesRowWithName(t *testing.T) {
	repo := newFakeRepo()
	nc, rec := startRegistryDerivation(t, repo, 1)

	created := time.Date(2026, 10, 7, 14, 41, 36, 0, time.UTC)
	publishEvent(t, nc, Event{
		DaemonID: "2aab1465", Type: EventLifecycle, At: created,
		UserID: "u-owner", DaemonType: "managed", Name: "default", CreatedAt: &created,
		Phase: LifecyclePhaseProvisioning, Size: "small",
	})

	if !waitFor(t, time.Second, func() bool {
		row, ok := repo.daemonSnapshot("2aab1465")
		return ok && row.LifecyclePhase != nil
	}) {
		t.Fatal("an identity-bearing lifecycle event did not create the registry row")
	}
	row, _ := repo.daemonSnapshot("2aab1465")
	if row.Name != "default" {
		t.Errorf("name = %q, want the name the owner typed", row.Name)
	}
	if row.UserID != "u-owner" {
		t.Errorf("user_id = %q, want u-owner", row.UserID)
	}
	if *row.LifecyclePhase != string(LifecyclePhaseProvisioning) {
		t.Errorf("phase = %q, want provisioning", *row.LifecyclePhase)
	}
	if !row.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want the control plane's %v", row.CreatedAt, created)
	}
	if !waitFor(t, time.Second, func() bool { return len(rec.snapshot()) > 0 }) {
		t.Fatal("creating the row did not tell the owner's clients to refetch")
	}
}

// TestDerivation_RegistrySnapshotReconciles is the guarantee. Events are plain
// NATS and get lost; the snapshot is the control plane's whole set for one
// owner, and the registry converges to it: rows the control plane no longer
// has are removed, rows it has and the registry lacks are created — with name
// and phase — and a row that is connected right now is never removed however
// stale the snapshot.
//
// The fixture is the owner's actual prod drift on 2026-10-07: the managed
// ghost cda8a89b and the self-hosted 38181976, both deleted in the control
// plane, plus a live suspended machine the registry never had.
func TestDerivation_RegistrySnapshotReconciles(t *testing.T) {
	repo := newFakeRepo()
	longAgo := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	repo.seedDaemon(&db.Daemon{ID: "cda8a89b", UserID: "u-owner", CreatedAt: longAgo})
	repo.seedDaemon(&db.Daemon{ID: "38181976", UserID: "u-owner", CreatedAt: longAgo})
	repo.seedDaemon(&db.Daemon{ID: "2aab1465", UserID: "u-owner", CreatedAt: longAgo})
	// Connected now but absent from the snapshot: the control plane has not
	// processed its connected event yet. Must survive.
	repo.seedDaemon(&db.Daemon{ID: "laptop", UserID: "u-owner", CreatedAt: longAgo})
	repo.rows["laptop"] = &db.DaemonAttachment{DaemonID: "laptop", UserID: "u-owner", LastStreamActivity: time.Now().UTC()}
	// Another owner's row: a snapshot names one owner and touches only them.
	repo.seedDaemon(&db.Daemon{ID: "someone-else", UserID: "u-other", CreatedAt: longAgo})
	nc, rec := startRegistryDerivation(t, repo, 2)

	suspendedAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	publishSnapshot(t, nc, RegistrySnapshot{
		UserID: "u-owner",
		At:     time.Now().UTC(),
		Daemons: []RegistryDaemon{
			{DaemonID: "2aab1465", Name: "default", DaemonType: "managed", CreatedAt: longAgo},
			{
				DaemonID: "dea98d67", Name: "test", DaemonType: "managed", CreatedAt: longAgo,
				Phase: LifecyclePhaseSuspended, PhaseChangedAt: &suspendedAt, Size: "small",
			},
		},
	})

	if !waitFor(t, time.Second, func() bool {
		_, ok := repo.daemonSnapshot("dea98d67")
		return ok
	}) {
		t.Fatal("snapshot did not create the machine the registry lacked")
	}
	created, _ := repo.daemonSnapshot("dea98d67")
	if created.Name != "test" || created.LifecyclePhase == nil || *created.LifecyclePhase != "suspended" {
		t.Errorf("created row = name %q phase %v, want test/suspended", created.Name, created.LifecyclePhase)
	}
	for _, ghost := range []string{"cda8a89b", "38181976"} {
		if _, ok := repo.daemonSnapshot(ghost); ok {
			t.Errorf("%s is deleted in the control plane but survived the snapshot", ghost)
		}
	}
	if row, ok := repo.daemonSnapshot("2aab1465"); !ok || row.Name != "default" {
		t.Errorf("live machine lost or unnamed after snapshot: %+v", row)
	}
	if _, ok := repo.daemonSnapshot("laptop"); !ok {
		t.Error("a connected daemon was removed because a snapshot did not list it yet")
	}
	if _, ok := repo.daemonSnapshot("someone-else"); !ok {
		t.Error("a snapshot for one owner removed another owner's machine")
	}
	if !waitFor(t, time.Second, func() bool { return len(rec.snapshot()) == 1 }) {
		t.Fatalf("expected exactly one refetch notification for the owner, got %+v", rec.snapshot())
	}
	if got := rec.snapshot()[0].userID; got != "u-owner" {
		t.Errorf("notified %q, want u-owner", got)
	}
}

// TestDerivation_EmptySnapshotRemovesEveryGhost: an owner whose machines are
// all deleted gets an EMPTY snapshot, and that is precisely what clears their
// ghosts. An implementation that skipped empty sets — or bound "not in ()" to
// NULL — would leave every one of them listed.
func TestDerivation_EmptySnapshotRemovesEveryGhost(t *testing.T) {
	repo := newFakeRepo()
	longAgo := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	repo.seedDaemon(&db.Daemon{ID: "ghost-a", UserID: "u-owner", CreatedAt: longAgo})
	repo.seedDaemon(&db.Daemon{ID: "ghost-b", UserID: "u-owner", CreatedAt: longAgo})
	nc, _ := startRegistryDerivation(t, repo, 2)

	publishSnapshot(t, nc, RegistrySnapshot{UserID: "u-owner", At: time.Now().UTC(), Daemons: []RegistryDaemon{}})

	if !waitFor(t, time.Second, func() bool {
		_, a := repo.daemonSnapshot("ghost-a")
		_, b := repo.daemonSnapshot("ghost-b")
		return !a && !b
	}) {
		t.Fatal("an empty snapshot did not remove the owner's ghosts")
	}
}
