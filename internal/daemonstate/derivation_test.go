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
)

// fakeRepo captures the side effects the Derivation consumer performs.
// Models just enough of DerivationRepository to exercise dispatch + ordering
// rules without touching Postgres — the SQL guard for out-of-order delivery
// (WHERE last_stream_activity < new) is mirrored here so the test fails for
// either a missing repo guard OR a missing consumer guard.
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
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: map[string]*db.DaemonAttachment{}}
}

func (f *fakeRepo) UpsertDaemonAttachment(_ context.Context, att *db.DaemonAttachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *att
	f.rows[att.DaemonID] = &cp
	return nil
}

func (f *fakeRepo) TouchDaemonAttachmentIfNewer(_ context.Context, daemonID string, activityAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[daemonID]
	if !ok {
		return nil // matches SQL: zero rows updated when no row exists
	}
	if !activityAt.After(row.LastStreamActivity) {
		return nil // mirrors WHERE last_stream_activity < new guard
	}
	row.LastStreamActivity = activityAt
	return nil
}

func (f *fakeRepo) DeleteDaemonAttachment(_ context.Context, daemonID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, daemonID)
	f.deletes = append(f.deletes, daemonID)
	return nil
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

func publishEvent(t *testing.T, nc *nats.Conn, evt Event) {
	t.Helper()
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := nc.Publish(Subject(evt.DaemonID, evt.Type), body); err != nil {
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

// TestDerivation_DispatchesAndIsOutOfOrderSafe exercises the full event
// lifecycle plus the out-of-order activity guard. One test covers the three
// behaviours the spec calls out: dispatch correctness, idempotency on
// repeated activity, and "older timestamp must not regress last_stream_activity".
func TestDerivation_DispatchesAndIsOutOfOrderSafe(t *testing.T) {
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

	t0 := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)

	// 1. connected: row appears with attached_at == last_stream_activity == t0
	publishEvent(t, nc, Event{DaemonID: "d-1", UserID: "u-1", Type: EventConnected, At: t0, DaemonType: "cloud"})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		row, ok := repo.snapshot("d-1")
		return ok && row.LastStreamActivity.Equal(t0)
	}) {
		t.Fatalf("connected: row not created at t0")
	}
	row, _ := repo.snapshot("d-1")
	if row.UserID != "u-1" {
		t.Errorf("connected: userID = %q, want %q", row.UserID, "u-1")
	}
	if row.Source != db.DaemonAttachmentSourceInbound {
		t.Errorf("connected: source = %q, want %q", row.Source, db.DaemonAttachmentSourceInbound)
	}
	if !row.AttachedAt.Equal(t0) {
		t.Errorf("connected: attached_at = %v, want %v", row.AttachedAt, t0)
	}

	// 2. activity at t1 > t0: advances last_stream_activity.
	t1 := t0.Add(45 * time.Second)
	publishEvent(t, nc, Event{DaemonID: "d-1", UserID: "u-1", Type: EventActivity, At: t1})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		row, _ := repo.snapshot("d-1")
		return row.LastStreamActivity.Equal(t1)
	}) {
		row, _ := repo.snapshot("d-1")
		t.Fatalf("activity: last_stream_activity = %v, want %v", row.LastStreamActivity, t1)
	}

	// 3. activity at t0+10s (older than t1): must NOT regress.
	publishEvent(t, nc, Event{DaemonID: "d-1", UserID: "u-1", Type: EventActivity, At: t0.Add(10 * time.Second)})
	time.Sleep(75 * time.Millisecond)
	row, _ = repo.snapshot("d-1")
	if !row.LastStreamActivity.Equal(t1) {
		t.Fatalf("out-of-order activity regressed last_stream_activity; got %v want %v", row.LastStreamActivity, t1)
	}

	// 4. activity at t1 again (idempotent, same timestamp): must NOT regress
	// and must not error; guard is "strictly newer".
	publishEvent(t, nc, Event{DaemonID: "d-1", UserID: "u-1", Type: EventActivity, At: t1})
	time.Sleep(75 * time.Millisecond)
	row, _ = repo.snapshot("d-1")
	if !row.LastStreamActivity.Equal(t1) {
		t.Fatalf("idempotent activity at same timestamp changed last_stream_activity; got %v", row.LastStreamActivity)
	}

	// 5. disconnected: row removed.
	publishEvent(t, nc, Event{DaemonID: "d-1", UserID: "u-1", Type: EventDisconnected, At: t1.Add(time.Minute)})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := repo.snapshot("d-1")
		return !ok
	}) {
		t.Fatal("disconnected: row not deleted")
	}
	if repo.deleteCount() != 1 {
		t.Errorf("expected 1 delete, got %d", repo.deleteCount())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned err: %v", err)
	}
}

// TestDerivation_ReapsExpiredAttachmentsOnStart covers the GC for the leak
// that has no other cure: DeleteDaemonAttachment runs on graceful teardown
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
	t0 := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	publishEvent(t, nc, Event{DaemonID: "d-9", UserID: "u-9", Type: EventConnected, At: t0})
	if !waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := repo.snapshot("d-9")
		return ok
	}) {
		t.Fatal("consumer stopped processing events after a reap failure")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned err: %v", err)
	}
}
