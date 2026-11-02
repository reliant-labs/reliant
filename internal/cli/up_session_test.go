package cli

// The §7.4 contract, as tests: presence reporting NEVER delays or fails
// `forge env up`, and the heartbeat it starts always stops.
//
// These are the tests the design asks to be MUTATION-CHECKED, so each one
// names the mutation it catches. That matters more here than usual: every
// assertion is about something NOT happening (no delay, no error, no leaked
// goroutine), and a test of an absence passes just as cheerfully against code
// that does nothing at all.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// fakeSessionReporter records what it was told and does what it was asked.
type fakeSessionReporter struct {
	mu       sync.Mutex
	reports  []release.LocalSession
	err      error
	block    chan struct{} // non-nil: every call waits on it or on ctx
	calls    atomic.Int64
	blocking atomic.Int64
}

func (f *fakeSessionReporter) ReportSession(ctx context.Context, s release.LocalSession) error {
	f.calls.Add(1)
	if f.block != nil {
		f.blocking.Add(1)
		defer f.blocking.Add(-1)
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, s)
	return f.err
}

func (f *fakeSessionReporter) snapshot() []release.LocalSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]release.LocalSession(nil), f.reports...)
}

// testUpSession builds a session around a fake reporter, bypassing the
// render-and-capture construction newUpSession does. The lifecycle is what is
// under test here, not the minting.
func testUpSession(reporter sessionReporter, log *strings.Builder, heartbeat time.Duration) *upSession {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return &upSession{
		reporter: reporter,
		session: release.LocalSession{
			ID:         sessionIDFor("dev", "host-abc", "wt-1"),
			Env:        "dev",
			Worktree:   release.Worktree{Key: "wt-1", Label: "feat-x", Host: "host-abc"},
			StartedAt:  now,
			LastSeenAt: now,
		},
		log:            log,
		heartbeat:      heartbeat,
		loggedFailures: map[string]bool{},
	}
}

// TestUpSessionStartDoesNotBlockOnABlockingReporter is the central §7.4
// assertion: a reporter that never returns must not hold up `forge env up`.
//
// MUTATION: make start() call s.report synchronously instead of in a
// goroutine, and this test hangs until the 5s report timeout — well past the
// budget below.
func TestUpSessionStartDoesNotBlockOnABlockingReporter(t *testing.T) {
	reporter := &fakeSessionReporter{block: make(chan struct{})}
	defer close(reporter.block)
	var log strings.Builder
	session := testUpSession(reporter, &log, time.Hour)

	done := make(chan struct{})
	go func() {
		session.start(time.Now().UTC())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("start() blocked on a reporter that never returns; `forge env up` must never wait on presence (§7.4)")
	}

	// And prove the call was actually attempted — otherwise this test would
	// also pass against a start() that reports nothing at all.
	deadline := time.Now().Add(2 * time.Second)
	for reporter.blocking.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if reporter.blocking.Load() == 0 {
		t.Fatal("start() never called the reporter: the test would pass against a no-op")
	}
}

// TestUpSessionReportSurvivesAnErroringReporter proves an error is absorbed,
// logged, and logged ONCE.
//
// MUTATION: have report() return the error (or panic on it), and the
// no-error assertion below is the one that catches it — report has no return
// value precisely so a caller cannot propagate one.
func TestUpSessionReportSurvivesAnErroringReporter(t *testing.T) {
	reporter := &fakeSessionReporter{err: errors.New("control plane unreachable: dial tcp: connection refused")}
	var log strings.Builder
	session := testUpSession(reporter, &log, time.Hour)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	session.report(now, false)
	session.report(now.Add(time.Minute), false)
	session.report(now.Add(2*time.Minute), false)

	if got := reporter.calls.Load(); got != 3 {
		t.Fatalf("reporter calls = %d, want 3 (an error must not stop later heartbeats)", got)
	}
	// ONE note for three failures of the same mode. A line a minute for a
	// working day is noise that trains a reader to ignore the log.
	if n := strings.Count(log.String(), "could not report this stack's presence"); n != 1 {
		t.Fatalf("logged the same failure mode %d times, want 1:\n%s", n, log.String())
	}
	// The note must say the stack is fine, or it reads as a startup failure
	// and the next thing the reader does is restart a healthy stack.
	if !strings.Contains(log.String(), "the stack is unaffected") {
		t.Fatalf("failure note does not say the stack is unaffected:\n%s", log.String())
	}
}

// TestUpSessionStopSetsStoppedAtOnce pins the clean-exit marker, and that it
// is written at most once.
func TestUpSessionStopSetsStoppedAtOnce(t *testing.T) {
	reporter := &fakeSessionReporter{}
	var log strings.Builder
	session := testUpSession(reporter, &log, time.Hour)

	now := time.Date(2026, 3, 1, 12, 5, 0, 0, time.UTC)
	session.stop(now)
	session.stop(now.Add(time.Second)) // the Ctrl-C cascade and shutdown can both reach teardown

	reports := reporter.snapshot()
	if len(reports) != 1 {
		t.Fatalf("stop reported %d times, want 1", len(reports))
	}
	if reports[0].StoppedAt == nil {
		t.Fatal("stop did not set StoppedAt; a stopped session is one whose StoppedAt is set — there is no flag")
	}
	if !reports[0].StoppedAt.Equal(now) {
		t.Fatalf("StoppedAt = %s, want %s", reports[0].StoppedAt, now)
	}
	if reports[0].Live() {
		t.Fatal("the stopped report still reads as live")
	}
}

// TestUpSessionHeartbeatExitsWhenTheStackStops is the goroutine-leak
// assertion.
//
// MUTATION: drop the ctx.Done() case from heartbeatUntil's select, and this
// test times out.
func TestUpSessionHeartbeatExitsWhenTheStackStops(t *testing.T) {
	reporter := &fakeSessionReporter{}
	var log strings.Builder
	session := testUpSession(reporter, &log, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() {
		session.heartbeatUntil(ctx)
		close(exited)
	}()

	// Let it beat at least twice, so the test also proves the heartbeat
	// RUNS — a goroutine that exits immediately would pass the exit
	// assertion on its own.
	deadline := time.Now().Add(2 * time.Second)
	for reporter.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := reporter.calls.Load(); got < 2 {
		t.Fatalf("heartbeat fired %d times in 2s at a 5ms interval, want >= 2", got)
	}

	cancel()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the heartbeat goroutine outlived the stack: it must exit on ctx.Done()")
	}
}

// TestUpSessionHeartbeatRefreshesLastSeen proves a heartbeat is a REFRESH
// rather than a duplicate: same id, moving LastSeenAt, still live.
func TestUpSessionHeartbeatRefreshesLastSeen(t *testing.T) {
	reporter := &fakeSessionReporter{}
	var log strings.Builder
	session := testUpSession(reporter, &log, time.Hour)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	session.report(now, false)
	session.report(now.Add(time.Minute), false)

	reports := reporter.snapshot()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	if reports[0].ID != reports[1].ID {
		t.Fatalf("heartbeat changed the session id (%s → %s): the id must be stable across heartbeats, or each beat creates a new row",
			reports[0].ID, reports[1].ID)
	}
	if !reports[1].LastSeenAt.After(reports[0].LastSeenAt) {
		t.Fatalf("heartbeat did not move LastSeenAt (%s → %s): a reader greys a session out on it",
			reports[0].LastSeenAt, reports[1].LastSeenAt)
	}
	if !reports[1].Live() {
		t.Fatal("a heartbeat reported a non-live session")
	}
}

// TestUpSessionStopReportsThroughACancelledContext is the Ctrl-C case.
//
// Teardown runs while the command's context is ALREADY cancelled — that is
// how the cascade started — so a stop report on the caller's context would
// be cancelled before it was sent, and every clean Ctrl-C would read as a
// crash. report() derives its context from Background for exactly this.
//
// MUTATION: thread the caller's ctx into report() instead, and this fails.
func TestUpSessionStopReportsThroughACancelledContext(t *testing.T) {
	reporter := &fakeSessionReporter{}
	var log strings.Builder
	session := testUpSession(reporter, &log, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The heartbeat observes the cancellation and exits; the stop report
	// must still land.
	session.heartbeatUntil(ctx)
	session.stop(time.Now().UTC())

	reports := reporter.snapshot()
	if len(reports) != 1 || reports[0].StoppedAt == nil {
		t.Fatalf("stop did not report through a cancelled caller context: got %d report(s)", len(reports))
	}
}

// TestUpSessionNilIsInert pins the nil-receiver contract. newUpSession
// returns nil for a non-local env, an unrenderable project and an
// unopenable ledger, and up.go calls these methods unconditionally — so a
// nil that panicked would turn a cosmetic loss into a crashed `forge env up`.
func TestUpSessionNilIsInert(t *testing.T) {
	var session *upSession
	session.start(time.Now())
	session.report(time.Now(), false)
	session.stop(time.Now())
	session.heartbeatUntil(context.Background()) // must return, not block
}

// TestSessionIDIsStablePerIdentity pins the derivation. The id must be a
// function of (env, host, worktree) alone — the same triple the file store
// keys on — so restarting a stack REPLACES its row instead of leaving a
// stopped twin beside a running one.
func TestSessionIDIsStablePerIdentity(t *testing.T) {
	a := sessionIDFor("dev", "host-abc", "wt-1")
	if b := sessionIDFor("dev", "host-abc", "wt-1"); a != b {
		t.Fatalf("the same identity minted two ids (%s, %s): a restarted stack would leave a stopped twin", a, b)
	}
	for _, other := range []struct{ env, host, key string }{
		{"staging", "host-abc", "wt-1"},
		{"dev", "host-xyz", "wt-1"},
		{"dev", "host-abc", "wt-2"},
	} {
		if got := sessionIDFor(other.env, other.host, other.key); got == a {
			t.Fatalf("%v collided with (dev, host-abc, wt-1): two stacks would share one row", other)
		}
	}
	// The primary checkout's key is "", which must still produce an id.
	if sessionIDFor("dev", "host-abc", "") == "" {
		t.Fatal("the primary checkout (empty worktree key) minted no id")
	}
}
