// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// fakeRepo captures the side effects the Derivation consumer performs.
// Models just enough of DerivationRepository to exercise dispatch + ordering
// rules without touching Postgres — the SQL guards for out-of-order delivery
// are mirrored here so a test fails for either a missing repo guard OR a
// missing consumer guard. rows holds attachment leases, which only the reaper
// may delete.
type fakeRepo struct {
	mu      sync.Mutex
	rows    map[string]*db.DaemonAttachment
	deletes []string

	// reapClock, when non-zero, ARMS DeleteStaleDaemonAttachments to mirror
	// the SQL (delete rows whose lease predates reapClock-olderThan).
	// Unarmed by default: the consumer runs a TTL sweep the moment it
	// starts, and the lifecycle tests below pin their fixtures to a fixed
	// date that is months in the past by wall-clock — an armed sweep would
	// race in and delete the very rows they are asserting on. The SQL
	// semantics are covered against real Postgres in internal/db
	// (TestDeleteStaleDaemonAttachments); what these tests own is that the
	// consumer asks for the sweep at all, with the right TTL.
	reapClock   time.Time
	reapCalls   int
	lastReapTTL time.Duration
	reapErr     error

	// daemons are the identity rows lifecycle events land on. Separate from
	// rows above because they are separate tables with separate owners:
	// attachment is this service's lease, the daemons row carries mirrored
	// control-plane lifecycle.
	daemons        map[string]*db.Daemon
	lifecycleCalls int
	lifecycleErr   error

	// snapshots records every registry snapshot applied, in order.
	snapshots []db.RegistrySnapshotApply
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		rows:    map[string]*db.DaemonAttachment{},
		daemons: map[string]*db.Daemon{},
	}
}

func (f *fakeRepo) DeleteStaleDaemonAttachments(_ context.Context, olderThan time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reapCalls++
	f.lastReapTTL = olderThan
	if f.reapErr != nil {
		return 0, f.reapErr
	}
	if f.reapClock.IsZero() {
		return 0, nil // unarmed — see reapClock
	}
	cutoff := f.reapClock.Add(-olderThan)
	var n int64
	for id, row := range f.rows {
		if row.LastStreamActivity.Before(cutoff) {
			delete(f.rows, id)
			f.deletes = append(f.deletes, id)
			n++
		}
	}
	return n, nil
}

// ApplyDaemonLifecycle mirrors the SQL's two conditions — the row must exist,
// and the event must be strictly newer than the stored change — so these tests
// fail for a missing guard in EITHER the consumer or the repository, the same
// way the attachment fakes above do.
func (f *fakeRepo) ApplyDaemonLifecycle(_ context.Context, lc db.DaemonLifecycleUpdate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifecycleCalls++
	if f.lifecycleErr != nil {
		return false, f.lifecycleErr
	}
	row, ok := f.daemons[lc.DaemonID]
	if !ok {
		return false, nil // matches SQL: no row, nothing updated
	}
	if row.LastStatusChangedAt != nil && !lc.ChangedAt.After(*row.LastStatusChangedAt) {
		return false, nil // mirrors WHERE last_status_changed_at < new
	}
	phase := lc.Phase
	row.LifecyclePhase = &phase
	row.LastStatusMessage = lc.StatusMessage
	changed := lc.ChangedAt
	row.LastStatusChangedAt = &changed
	if lc.Size != "" { // COALESCE(NULLIF(?,''), size)
		size := lc.Size
		row.Size = &size
	}
	if lc.LastOOMKilledAt != nil {
		row.LastOOMKilledAt = lc.LastOOMKilledAt
	}
	if lc.OOMKillCount > row.OOMKillCount { // GREATEST
		row.OOMKillCount = lc.OOMKillCount
	}
	return true, nil
}

// seedDaemon registers an identity row so lifecycle events have something to
// attach to, which is what the real ApplyDaemonLifecycle requires.
func (f *fakeRepo) seedDaemon(d *db.Daemon) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.daemons[d.ID] = d
}

func (f *fakeRepo) daemonSnapshot(daemonID string) (*db.Daemon, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.daemons[daemonID]
	if !ok {
		return nil, false
	}
	cp := *row
	return &cp, true
}

func (f *fakeRepo) lifecycleCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lifecycleCalls
}

func (f *fakeRepo) reapStats() (calls int, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reapCalls, f.lastReapTTL
}

func (f *fakeRepo) snapshot(daemonID string) (*db.DaemonAttachment, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[daemonID]
	if !ok {
		return nil, false
	}
	cp := *row
	return &cp, true
}

func (f *fakeRepo) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deletes)
}

func publishEvent(t *testing.T, nc *nats.Conn, evt daemonwire.Event) {
	t.Helper()
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := nc.Publish(daemonwire.Subject(evt.DaemonID, evt.Type), body); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

// TestDerivation_StaleDisconnectNeverDeletesALiveLease pins one way a serving
// daemon's lease row went missing (prod 2026-10-11: a daemon attached and
// serving for hours while the registry reported it DISCONNECTED).
//
// The gateway that holds a stream writes that stream's lease itself, in
// stream order. This consumer used to ALSO apply every connected / activity /
// disconnected event to the same row — on every replica, each event on its own
// goroutine, the delete keyed by daemon id alone. So a "disconnected" from the
// session a gateway drain ended, applied after the reconnect's lease was
// written, deleted the new session's lease.
func TestDerivation_StaleDisconnectNeverDeletesALiveLease(t *testing.T) {
	nc := startTestNATS(t)
	repo := newFakeRepo()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := NewDerivation(nc, repo)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	if !waitFor(t, 500*time.Millisecond, func() bool { return nc.NumSubscriptions() > 0 }) {
		t.Fatal("subscription did not register")
	}

	drained := time.Date(2026, 10, 11, 2, 55, 7, 0, time.UTC)
	reattached := time.Date(2026, 10, 11, 2, 56, 37, 0, time.UTC)

	// The reconnect's lease, as the new session's gateway wrote it.
	repo.mu.Lock()
	repo.rows["d-1"] = &db.DaemonAttachment{
		DaemonID: "d-1", UserID: "u-1", Source: db.DaemonAttachmentSourceInbound,
		AttachedAt: reattached, LastStreamActivity: reattached.Add(15 * time.Second),
	}
	repo.mu.Unlock()

	// The new session's own events, then the drained session's disconnect
	// arriving late.
	publishEvent(t, nc, daemonwire.Event{DaemonID: "d-1", UserID: "u-1", Type: daemonwire.EventConnected, At: reattached})
	publishEvent(t, nc, daemonwire.Event{DaemonID: "d-1", UserID: "u-1", Type: daemonwire.EventActivity, At: reattached.Add(time.Second)})
	publishEvent(t, nc, daemonwire.Event{DaemonID: "d-1", UserID: "u-1", Type: daemonwire.EventDisconnected, At: drained})
	time.Sleep(150 * time.Millisecond)

	row, ok := repo.snapshot("d-1")
	if !ok {
		t.Fatal("a stale disconnected event deleted the live session's lease")
	}
	if !row.AttachedAt.Equal(reattached) || !row.LastStreamActivity.Equal(reattached.Add(15*time.Second)) {
		t.Errorf("liveness events rewrote the gateway's lease: %+v", row)
	}
	if n := repo.deleteCount(); n != 0 {
		t.Errorf("expected no attachment deletes from liveness events, got %d", n)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned err: %v", err)
	}
}

// TestDerivation_DropsInvalidEventsAtParse pins that a payload the wire
// contract rejects (daemonwire.UnmarshalEvent) never reaches the repository,
// even on a subject that names a real event type.
func TestDerivation_DropsInvalidEventsAtParse(t *testing.T) {
	nc := startTestNATS(t)
	repo := newFakeRepo()
	repo.seedDaemon(&db.Daemon{ID: "d-bad", UserID: "u-1"})
	repo.seedDaemon(&db.Daemon{ID: "d-ok", UserID: "u-1"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = NewDerivation(nc, repo).Start(ctx) }()
	if !waitFor(t, 500*time.Millisecond, func() bool { return nc.NumSubscriptions() > 0 }) {
		t.Fatal("subscription did not register")
	}

	at := `"at":"2026-05-30T12:00:00Z"`
	for _, payload := range []string{
		`not-json`,
		`{"daemon_id":"d-bad","user_id":"u-1","type":"weird","phase":"ready",` + at + `}`,
		`{"daemon_id":"d-bad","user_id":"u-1","phase":"ready",` + at + `}`,
		`{"daemon_id":"d-bad","user_id":"u-1","type":"lifecycle","phase":"ready"}`,
	} {
		if err := nc.Publish(daemonwire.Subject("d-bad", daemonwire.EventLifecycle), []byte(payload)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	// A valid event after them, so there is something to wait on.
	publishEvent(t, nc, daemonwire.Event{DaemonID: "d-ok", UserID: "u-1", Type: daemonwire.EventLifecycle, At: time.Now().UTC(), Phase: daemonwire.LifecyclePhaseReady})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		row, ok := repo.daemonSnapshot("d-ok")
		return ok && row.LifecyclePhase != nil
	}) {
		t.Fatal("valid event after the invalid ones was not applied")
	}
	time.Sleep(100 * time.Millisecond)
	if row, _ := repo.daemonSnapshot("d-bad"); row.LifecyclePhase != nil {
		t.Error("an invalid payload reached the repository")
	}
	if n := repo.lifecycleCallCount(); n != 1 {
		t.Errorf("lifecycle applications = %d, want 1 (the valid event only)", n)
	}
}

// TestDerivation_ReapsExpiredAttachmentsOnStart covers the GC for the leak
// that has no other cure: ReleaseDaemonAttachment runs on graceful teardown
// only, so a gateway that crashes or is rescheduled strands every row it
// owned and nothing left alive knows to clean them up. Dev's registry carried
// two such orphans (29 and 51 days) which pinned /flow-health at 503 for the
// whole environment.
//
// The sweep must run at START, not one interval later: a fresh gateway is
// exactly the moment a dead predecessor's rows are lying around.
func TestDerivation_ReapsExpiredAttachmentsOnStart(t *testing.T) {
	nc := startTestNATS(t)
	repo := newFakeRepo()

	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	repo.reapClock = now
	// Seeded before Start so the immediate sweep has something to rule on.
	repo.rows["live"] = &db.DaemonAttachment{DaemonID: "live", UserID: "u", LastStreamActivity: now.Add(-5 * time.Second)}
	repo.rows["recent"] = &db.DaemonAttachment{DaemonID: "recent", UserID: "u", LastStreamActivity: now.Add(-AttachmentTTL + time.Minute)}
	repo.rows["orphan-29d"] = &db.DaemonAttachment{DaemonID: "orphan-29d", UserID: "u", LastStreamActivity: now.Add(-29 * 24 * time.Hour)}
	repo.rows["orphan-51d"] = &db.DaemonAttachment{DaemonID: "orphan-51d", UserID: "u", LastStreamActivity: now.Add(-51 * 24 * time.Hour)}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := NewDerivation(nc, repo)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	if !waitFor(t, time.Second, func() bool {
		_, ok := repo.snapshot("orphan-51d")
		return !ok
	}) {
		t.Fatal("51-day-old orphan survived the reap")
	}
	if _, ok := repo.snapshot("orphan-29d"); ok {
		t.Error("29-day-old orphan survived the reap")
	}
	// A row still inside the TTL is a lease that may yet be renewed — the
	// reaper must not touch it, or a NATS hiccup would evict a live daemon.
	if _, ok := repo.snapshot("recent"); !ok {
		t.Error("reaper deleted a row still inside the TTL")
	}
	if _, ok := repo.snapshot("live"); !ok {
		t.Error("reaper deleted a live daemon's lease")
	}

	calls, ttl := repo.reapStats()
	if calls < 1 {
		t.Errorf("expected at least one sweep on start, got %d", calls)
	}
	if ttl != AttachmentTTL {
		t.Errorf("sweep TTL = %v, want %v", ttl, AttachmentTTL)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned err: %v", err)
	}
}

// TestDerivation_ReapFailureIsNotFatal keeps the GC from being able to take
// the consumer down: a failing DELETE is a logged warning, and lifecycle
// events keep flowing.
func TestDerivation_ReapFailureIsNotFatal(t *testing.T) {
	nc := startTestNATS(t)
	repo := newFakeRepo()
	repo.reapErr = errors.New("db down")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := NewDerivation(nc, repo)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	if !waitFor(t, 500*time.Millisecond, func() bool { return nc.NumSubscriptions() > 0 }) {
		t.Fatal("subscription did not register")
	}
	repo.seedDaemon(&db.Daemon{ID: "d-9", UserID: "u-9"})
	t0 := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	publishEvent(t, nc, daemonwire.Event{DaemonID: "d-9", UserID: "u-9", Type: daemonwire.EventLifecycle, At: t0, Phase: daemonwire.LifecyclePhaseReady})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		row, ok := repo.daemonSnapshot("d-9")
		return ok && row.LifecyclePhase != nil
	}) {
		t.Fatal("consumer stopped processing events after a reap failure")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned err: %v", err)
	}
}
