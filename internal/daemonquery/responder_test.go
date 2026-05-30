// Copyright (c) 2025 Reliant Labs

package daemonquery

import (
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// fakeSource implements StatusSource with canned responses keyed by daemonID.
// Each call increments callCount so tests can assert the responder reads from
// the source per request (not from a cache).
type fakeSource struct {
	mu         sync.Mutex
	lastActive map[string]time.Time
	daemonType map[string]string
	missing    map[string]bool // daemonIDs that report ok=false
	calls      []string
}

func (f *fakeSource) DaemonStatus(daemonID string) (time.Time, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, daemonID)
	if f.missing[daemonID] {
		return time.Time{}, "", false
	}
	return f.lastActive[daemonID], f.daemonType[daemonID], true
}

func (f *fakeSource) callCount(daemonID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == daemonID {
			n++
		}
	}
	return n
}

// startTestNATS runs an in-process NATS server on a random port and returns a
// client connection that the test can use. Tears down on cleanup.
func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	srv := natstest.RunServer(&opts)
	t.Cleanup(func() { srv.Shutdown() })

	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect to test nats: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// avoid linter: import server pkg via type reference so this file doesn't
// import unused (we use the helper above which depends on the server pkg
// transitively through natstest).
var _ = server.Options{}

func TestResponder_RespondsWithCurrentStateOnRequest(t *testing.T) {
	nc := startTestNATS(t)
	lastActive := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	src := &fakeSource{
		lastActive: map[string]time.Time{"d-1": lastActive},
		daemonType: map[string]string{"d-1": "cloud"},
	}

	r := NewResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)

	// Allow NATS subscription to propagate.
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	msg, err := nc.Request(SubjectStatus("d-1"), nil, 1*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	got, err := ParseStatus(msg.Data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Connected {
		t.Error("Connected = false, want true")
	}
	if got.LastActiveMs != lastActive.UnixMilli() {
		t.Errorf("LastActiveMs = %d, want %d", got.LastActiveMs, lastActive.UnixMilli())
	}
	if got.DaemonType != "cloud" {
		t.Errorf("DaemonType = %q, want %q", got.DaemonType, "cloud")
	}
	if n := src.callCount("d-1"); n != 1 {
		t.Errorf("source called %d times, want 1 (responder must read live state per request)", n)
	}
}

func TestResponder_NoReplyWhenDisconnectedBeforeRequest(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeSource{
		lastActive: map[string]time.Time{},
		daemonType: map[string]string{},
	}

	r := NewResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Tear down BEFORE the request goes out. The responder must unsubscribe,
	// so the request times out at the caller — which is the correct signal
	// that no gateway has this daemon.
	r.OnDaemonDisconnected("u-1", "d-1")
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	_, err := nc.Request(SubjectStatus("d-1"), nil, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error after disconnect, got nil")
	}
}

func TestResponder_RaceDisconnect_NoReplyWhenSourceReturnsNotOK(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeSource{
		missing: map[string]bool{"d-1": true},
	}

	r := NewResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Subscription is up but the source says ok=false (race: connection
	// dropped between OnDaemonConnected and the request arriving). The
	// responder must NOT reply, so the caller times out — correct.
	_, err := nc.Request(SubjectStatus("d-1"), nil, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout when source returns ok=false")
	}
}

func TestResponder_MultipleDaemonsRoutedIndependently(t *testing.T) {
	nc := startTestNATS(t)
	t0 := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 29, 10, 5, 0, 0, time.UTC)
	src := &fakeSource{
		lastActive: map[string]time.Time{"d-1": t0, "d-2": t1},
		daemonType: map[string]string{"d-1": "cloud", "d-2": "local"},
	}

	r := NewResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	r.OnDaemonConnected("u-2", "d-2")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	for daemonID, wantTs := range map[string]time.Time{"d-1": t0, "d-2": t1} {
		msg, err := nc.Request(SubjectStatus(daemonID), nil, 1*time.Second)
		if err != nil {
			t.Fatalf("request %s: %v", daemonID, err)
		}
		got, _ := ParseStatus(msg.Data)
		if got.LastActiveMs != wantTs.UnixMilli() {
			t.Errorf("daemon %s LastActiveMs = %d, want %d (cross-routing detected)",
				daemonID, got.LastActiveMs, wantTs.UnixMilli())
		}
	}
}

func TestSubjectStatus_RoundTrip(t *testing.T) {
	cases := []string{"abc", "01833a54-a94d-48bd-8f6d-839e05b9d95a", ""}
	for _, daemonID := range cases {
		subj := SubjectStatus(daemonID)
		want := SubjectQueryPrefix + daemonID + ".status"
		if subj != want {
			t.Errorf("SubjectStatus(%q) = %q, want %q", daemonID, subj, want)
		}
	}
}
