// Copyright (c) 2025 Reliant Labs

package daemonquery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeUserSource implements UserLivenessSource with a settable per-user count.
type fakeUserSource struct {
	mu     sync.Mutex
	counts map[string]int
	calls  int
}

func (f *fakeUserSource) ConnectedDaemonCountForUser(userID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.counts[userID]
}

func (f *fakeUserSource) set(userID string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	f.counts[userID] = n
}

func (f *fakeUserSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestUserResponder_AnswersLiveAfterFirstConnect(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 1)

	r := NewUserResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got, err := QueryUserAnyLive(context.Background(), nc, "u-1", 1*time.Second)
	if err != nil {
		t.Fatalf("QueryUserAnyLive: %v", err)
	}
	if !got.Live {
		t.Error("Live = false, want true")
	}
	if got.Count != 1 {
		t.Errorf("Count = %d, want 1", got.Count)
	}
	if n := src.callCount(); n != 1 {
		t.Errorf("source called %d times, want 1 (responder must read live state per request)", n)
	}
}

func TestUserResponder_SingleSubscriptionSurvivesPartialDisconnect(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 2)

	r := NewUserResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	r.OnDaemonConnected("u-1", "d-2")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got, err := QueryUserAnyLive(context.Background(), nc, "u-1", 1*time.Second)
	if err != nil {
		t.Fatalf("QueryUserAnyLive with 2 daemons: %v", err)
	}
	if got.Count != 2 {
		t.Errorf("Count = %d, want 2", got.Count)
	}

	// One of two daemons disconnects: the subscription must survive.
	r.OnDaemonDisconnected("u-1", "d-1")
	src.set("u-1", 1)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got, err = QueryUserAnyLive(context.Background(), nc, "u-1", 1*time.Second)
	if err != nil {
		t.Fatalf("QueryUserAnyLive after partial disconnect: %v", err)
	}
	if !got.Live || got.Count != 1 {
		t.Errorf("after partial disconnect: got %+v, want Live=true Count=1", got)
	}
}

func TestUserResponder_UnsubscribesOnLastDisconnect(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 1)

	r := NewUserResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Last daemon for the user disconnects: the responder must UNSUBSCRIBE
	// (not answer live=false), so the caller sees ErrUnavailable — the
	// canonical "no gateway holds any daemon for this user" signal.
	r.OnDaemonDisconnected("u-1", "d-1")
	src.set("u-1", 0)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	_, err := QueryUserAnyLive(context.Background(), nc, "u-1", 200*time.Millisecond)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable after last disconnect", err)
	}
}

func TestUserResponder_ReconnectRaceStaysIdempotent(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 1)

	r := NewUserResponder(nc, src)
	// Reconnect takeover: the gateway can fire OnDaemonConnected twice for
	// the same daemonID without an intervening disconnect (the superseded
	// connection's teardown is suppressed). The set-based bookkeeping must
	// treat that as one daemon, so the SINGLE later disconnect still tears
	// the subscription down.
	r.OnDaemonConnected("u-1", "d-1")
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, err := QueryUserAnyLive(context.Background(), nc, "u-1", 1*time.Second); err != nil {
		t.Fatalf("QueryUserAnyLive while connected: %v", err)
	}

	r.OnDaemonDisconnected("u-1", "d-1")
	src.set("u-1", 0)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	_, err := QueryUserAnyLive(context.Background(), nc, "u-1", 200*time.Millisecond)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable after single disconnect following double connect", err)
	}
}

func TestUserResponder_SilentWhenSourceReportsZero(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 0) // race: subscription up, but the map already emptied

	r := NewUserResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The responder must NOT reply live=false — silence lets another replica
	// answer, and total silence correctly reads as not-live.
	_, err := QueryUserAnyLive(context.Background(), nc, "u-1", 200*time.Millisecond)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable when source reports zero", err)
	}
}

func TestUserResponder_MultiReplicaFailover(t *testing.T) {
	nc := startTestNATS(t)

	// Replica A held the user's daemon and lost it: it unsubscribes.
	srcA := &fakeUserSource{}
	srcA.set("u-1", 0)
	replicaA := NewUserResponder(nc, srcA)
	replicaA.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(replicaA.CloseAll)

	// Replica B still holds a stream for the same user.
	srcB := &fakeUserSource{}
	srcB.set("u-1", 1)
	replicaB := NewUserResponder(nc, srcB)
	replicaB.OnDaemonConnected("u-1", "d-2")
	t.Cleanup(replicaB.CloseAll)

	replicaA.OnDaemonDisconnected("u-1", "d-1")
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Only replica B is subscribed now, so the answer must be live=true —
	// deterministically, not depending on which replica NATS picks.
	for i := 0; i < 5; i++ {
		got, err := QueryUserAnyLive(context.Background(), nc, "u-1", 1*time.Second)
		if err != nil {
			t.Fatalf("QueryUserAnyLive (iteration %d): %v", i, err)
		}
		if !got.Live {
			t.Fatalf("Live = false on iteration %d, want true (replica B still holds a stream)", i)
		}
	}
}

func TestUserResponder_UsersIsolated(t *testing.T) {
	nc := startTestNATS(t)
	src := &fakeUserSource{}
	src.set("u-1", 1)

	r := NewUserResponder(nc, src)
	r.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(r.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// A different user must get ErrUnavailable, not u-1's answer.
	_, err := QueryUserAnyLive(context.Background(), nc, "u-2", 200*time.Millisecond)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable for a user with no daemons", err)
	}
}

func TestQueryUserAnyLive_NilConn(t *testing.T) {
	if _, err := QueryUserAnyLive(context.Background(), nil, "u-1", time.Second); err == nil {
		t.Error("expected error for nil NATS connection")
	}
}

func TestSubjectUserAnyLive_Shape(t *testing.T) {
	cases := []string{"abc", "01833a54-a94d-48bd-8f6d-839e05b9d95a", ""}
	for _, userID := range cases {
		subj := SubjectUserAnyLive(userID)
		want := SubjectQueryPrefix + "user." + userID + ".any-live"
		if subj != want {
			t.Errorf("SubjectUserAnyLive(%q) = %q, want %q", userID, subj, want)
		}
	}
}
