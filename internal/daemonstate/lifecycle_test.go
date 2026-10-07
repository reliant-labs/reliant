// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// startLifecycleDerivation boots a Derivation against a test NATS server and
// returns a publish func for it.
//
// It waits for the subscription before returning, which is the precondition
// every test below shares: lifecycle events ride plain NATS, so an event
// published before the subscription registers is simply lost and the test would
// fail for the wrong reason.
func startLifecycleDerivation(t *testing.T, repo *fakeRepo) func(Event) {
	t.Helper()
	nc := startTestNATS(t)
	ctx, cancel := context.WithCancel(context.Background())

	d := NewDerivation(nc, repo)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	if !waitFor(t, 500*time.Millisecond, func() bool { return nc.NumSubscriptions() > 0 }) {
		cancel()
		t.Fatal("subscription did not register")
	}

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Start returned err: %v", err)
		}
	})

	return func(evt Event) { publishEvent(t, nc, evt) }
}

// TestDerivation_LifecycleMirrorsManagedPhase is the core of option (b): the
// registry learns what a managed machine is DOING from the control plane,
// because the Workspace CR is the only thing that knows and only the
// control-plane operator watches it.
//
// Before the lifecycle event type existed this was unreachable. dispatch()
// rejected any type it did not recognize ("unknown event type"), the daemons
// row had nowhere to put a phase, and the registry could therefore only ever
// report attached/not-attached. A provisioning machine and a crashed one were
// the same value.
func TestDerivation_LifecycleMirrorsManagedPhase(t *testing.T) {
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-managed", UserID: "u-1"})
	publish := startLifecycleDerivation(t, repo)

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	publish(Event{
		DaemonID:      "d-managed",
		UserID:        "u-1",
		Type:          EventLifecycle,
		At:            at,
		Phase:         LifecyclePhaseProvisioning,
		Size:          "medium",
		StatusMessage: "pulling image",
	})

	if !waitFor(t, time.Second, func() bool {
		row, ok := repo.daemonSnapshot("d-managed")
		return ok && row.LifecyclePhase != nil
	}) {
		t.Fatal("lifecycle event never applied to the daemons row")
	}

	row, _ := repo.daemonSnapshot("d-managed")
	if got := *row.LifecyclePhase; got != string(LifecyclePhaseProvisioning) {
		t.Errorf("lifecycle_phase = %q, want %q", got, LifecyclePhaseProvisioning)
	}
	if row.Size == nil || *row.Size != "medium" {
		t.Errorf("size = %v, want \"medium\"", row.Size)
	}
	if row.LastStatusMessage != "pulling image" {
		t.Errorf("last_status_message = %q, want %q", row.LastStatusMessage, "pulling image")
	}
	if row.LastStatusChangedAt == nil || !row.LastStatusChangedAt.Equal(at) {
		t.Errorf("last_status_changed_at = %v, want %v", row.LastStatusChangedAt, at)
	}
}

// TestDerivation_LifecycleIsOutOfOrderSafe pins the guard that makes mirroring
// safe at all. Lifecycle events ride plain NATS — newest wins, no redelivery,
// no ordering guarantee — so a delayed PROVISIONING can arrive after the READY
// that superseded it. Without the timestamp comparison the UI would show a
// ready machine as still provisioning, with nothing to correct it until the
// next transition, which for a healthy machine may be hours away.
func TestDerivation_LifecycleIsOutOfOrderSafe(t *testing.T) {
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-ooo", UserID: "u-1"})
	publish := startLifecycleDerivation(t, repo)

	early := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	late := early.Add(2 * time.Minute)

	publish(Event{
		DaemonID: "d-ooo", UserID: "u-1", Type: EventLifecycle,
		At: late, Phase: LifecyclePhaseReady,
	})
	if !waitFor(t, time.Second, func() bool {
		row, ok := repo.daemonSnapshot("d-ooo")
		return ok && row.LifecyclePhase != nil && *row.LifecyclePhase == string(LifecyclePhaseReady)
	}) {
		t.Fatal("ready phase never applied")
	}

	// The stale one arrives second, as it would after a reconnect or a
	// redelivered burst.
	publish(Event{
		DaemonID: "d-ooo", UserID: "u-1", Type: EventLifecycle,
		At: early, Phase: LifecyclePhaseProvisioning,
	})
	time.Sleep(100 * time.Millisecond)

	row, _ := repo.daemonSnapshot("d-ooo")
	if got := *row.LifecyclePhase; got != string(LifecyclePhaseReady) {
		t.Fatalf("stale lifecycle event regressed the phase to %q; want %q", got, LifecyclePhaseReady)
	}
	if !row.LastStatusChangedAt.Equal(late) {
		t.Fatalf("stale event moved last_status_changed_at to %v; want %v", row.LastStatusChangedAt, late)
	}
}

// TestDerivation_LifecycleForUnregisteredDaemonIsNotAnError covers the window
// the design doc names as option (b)'s honest cost, and the reason
// control-plane publishes PROVISIONING at CreateDaemon time.
//
// A managed machine exists in control-plane from CreateDaemon and does not
// exist here until it registers, which is after the pod runs and clones. So the
// first lifecycle events for every managed machine arrive with no row to attach
// to. That is ordinary operation, not a failure: treating it as an error would
// make a normal provision emit warnings for its entire startup, and on a
// JetStream-backed path would retry forever.
func TestDerivation_LifecycleForUnregisteredDaemonIsNotAnError(t *testing.T) {
	repo := newFakeRepo()
	publish := startLifecycleDerivation(t, repo) // no seeded daemon row

	publish(Event{
		DaemonID: "d-not-yet", UserID: "u-1", Type: EventLifecycle,
		At: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), Phase: LifecyclePhaseProvisioning,
	})

	if !waitFor(t, time.Second, func() bool { return repo.lifecycleCallCount() > 0 }) {
		t.Fatal("lifecycle event was never dispatched to the repository")
	}
	// No DaemonType: this event carries no identity (HasIdentity), which is
	// what the control plane's workspace-event path sends. It must not create
	// a row — only an identity-bearing event or a registry snapshot may.
	if _, ok := repo.daemonSnapshot("d-not-yet"); ok {
		t.Fatal("an identity-less lifecycle event invented a registry row with no owner type")
	}
}

// TestDerivation_LifecycleUnknownPhaseIsDropped pins the forward-compatibility
// choice. A publisher on a newer contract may emit a phase this consumer has
// never heard of; storing it would put a value in the column that no reader can
// interpret and the CHECK constraint would reject anyway. Dropping it leaves
// the registry falling back to attachment-derived status, which is always
// correct if less specific.
func TestDerivation_LifecycleUnknownPhaseIsDropped(t *testing.T) {
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-future", UserID: "u-1"})
	publish := startLifecycleDerivation(t, repo)

	publish(Event{
		DaemonID: "d-future", UserID: "u-1", Type: EventLifecycle,
		At: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), Phase: LifecyclePhase("hibernating"),
	})
	time.Sleep(150 * time.Millisecond)

	if repo.lifecycleCallCount() != 0 {
		t.Fatal("an unknown phase reached the repository; it must be dropped before the write")
	}
	row, _ := repo.daemonSnapshot("d-future")
	if row.LifecyclePhase != nil {
		t.Fatalf("unknown phase was stored as %q", *row.LifecyclePhase)
	}
}

// TestDerivation_LifecycleOOMAccountingAccumulates covers the two fields whose
// write semantics differ from the rest: a lifecycle event that omits size or
// OOM data is reporting a phase change, not asserting the machine has no size
// and has never been OOM-killed. COALESCE and GREATEST in the SQL are what keep
// a plain phase transition from erasing the OOM history the UI renders.
func TestDerivation_LifecycleOOMAccountingAccumulates(t *testing.T) {
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-oom", UserID: "u-1"})
	publish := startLifecycleDerivation(t, repo)

	killedAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	publish(Event{
		DaemonID: "d-oom", UserID: "u-1", Type: EventLifecycle,
		At: killedAt, Phase: LifecyclePhaseReady, Size: "large",
		LastOOMKilledAt: &killedAt, OOMKillCount: 2,
	})
	if !waitFor(t, time.Second, func() bool {
		row, ok := repo.daemonSnapshot("d-oom")
		return ok && row.OOMKillCount == 2
	}) {
		t.Fatal("OOM accounting never applied")
	}

	// A later phase transition carrying no OOM data must not erase it.
	publish(Event{
		DaemonID: "d-oom", UserID: "u-1", Type: EventLifecycle,
		At: killedAt.Add(time.Minute), Phase: LifecyclePhaseSuspended,
	})
	if !waitFor(t, time.Second, func() bool {
		row, ok := repo.daemonSnapshot("d-oom")
		return ok && row.LifecyclePhase != nil && *row.LifecyclePhase == string(LifecyclePhaseSuspended)
	}) {
		t.Fatal("suspended phase never applied")
	}

	row, _ := repo.daemonSnapshot("d-oom")
	if row.OOMKillCount != 2 {
		t.Errorf("oom_kill_count = %d after a phase transition, want 2 preserved", row.OOMKillCount)
	}
	if row.LastOOMKilledAt == nil || !row.LastOOMKilledAt.Equal(killedAt) {
		t.Errorf("last_oom_killed_at = %v after a phase transition, want %v preserved", row.LastOOMKilledAt, killedAt)
	}
	if row.Size == nil || *row.Size != "large" {
		t.Errorf("size = %v after a phase transition, want \"large\" preserved", row.Size)
	}
}

// TestDerivation_LifecycleRepoFailurePropagates keeps the handler honest about
// real failures. Dropping an unknown phase and tolerating a missing row are
// deliberate; swallowing a database error is not, because then a genuinely
// broken mirror looks exactly like a healthy one.
func TestDerivation_LifecycleRepoFailurePropagates(t *testing.T) {
	repo := newFakeRepo()
	repo.lifecycleErr = errors.New("connection reset")
	d := NewDerivation(nil, repo)

	err := d.onLifecycle(context.Background(), Event{
		DaemonID: "d-err", Type: EventLifecycle,
		At: time.Now().UTC(), Phase: LifecyclePhaseReady,
	})
	if err == nil {
		t.Fatal("onLifecycle swallowed a repository error")
	}
}

// An applied lifecycle event is the registry changing under the user's open
// web clients, so it must be announced: they refetch the daemon list on it
// rather than polling. A stale or unregistered one changed nothing and must
// stay silent — every gateway replica sees every event, and only the one whose
// write applied may speak, or each transition would be announced per replica.
func TestDerivation_LifecycleNotifiesOnlyWhenApplied(t *testing.T) {
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-managed", UserID: "u-1"})
	d := NewDerivation(nil, repo)

	type call struct{ userID, daemonID string }
	var calls []call
	d.NotifyLifecycleApplied(func(_ context.Context, userID, daemonID string) {
		calls = append(calls, call{userID, daemonID})
	})

	ctx := context.Background()
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	ready := Event{DaemonID: "d-managed", Type: EventLifecycle, At: at, Phase: LifecyclePhaseReady}

	if err := d.dispatch(ctx, ready); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(calls) != 1 || calls[0] != (call{"", "d-managed"}) {
		t.Fatalf("applied event: calls = %+v, want one call for d-managed (owner left for the notifier to resolve)", calls)
	}

	// The same event again (another replica, or a redelivery) is not newer.
	if err := d.dispatch(ctx, ready); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// A daemon with no registry row has nothing a client could list.
	if err := d.dispatch(ctx, Event{DaemonID: "d-unknown", Type: EventLifecycle, At: at.Add(time.Minute), Phase: LifecyclePhaseReady}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("stale/unregistered events must not notify: calls = %+v", calls)
	}
}
