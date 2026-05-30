// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(func() { srv.Shutdown() })
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// TestPublisher_LifecycleAndRateLimit asserts that connected/disconnected
// are unconditional and that 3 back-to-back Activity calls collapse to a
// single publish under the per-daemon rate limit.
func TestPublisher_LifecycleAndRateLimit(t *testing.T) {
	nc := startTestNATS(t)

	var (
		mu     sync.Mutex
		byType = map[EventType]int{}
		got    []Event
	)
	sub, err := nc.Subscribe(SubjectWildcard, func(msg *nats.Msg) {
		var e Event
		if err := json.Unmarshal(msg.Data, &e); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}
		mu.Lock()
		byType[e.Type]++
		got = append(got, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	p := NewPublisher(nc)
	const daemonID = "d-1"
	p.Connected(daemonID, "u-1", "cloud")
	p.Activity(daemonID, "u-1", "cloud")
	p.Activity(daemonID, "u-1", "cloud")
	p.Activity(daemonID, "u-1", "cloud")
	p.Disconnected(daemonID, "u-1", "cloud")

	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Drain a short window so the async subscription handler runs.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		total := byType[EventConnected] + byType[EventDisconnected] + byType[EventActivity]
		mu.Unlock()
		if total >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if byType[EventConnected] != 1 {
		t.Errorf("connected count = %d, want 1", byType[EventConnected])
	}
	if byType[EventActivity] != 1 {
		t.Errorf("activity count = %d, want 1 (3 calls should collapse under rate limit)", byType[EventActivity])
	}
	if byType[EventDisconnected] != 1 {
		t.Errorf("disconnected count = %d, want 1", byType[EventDisconnected])
	}
	for _, e := range got {
		if e.DaemonID != daemonID {
			t.Errorf("event daemonID = %q, want %q", e.DaemonID, daemonID)
		}
		if e.UserID != "u-1" {
			t.Errorf("event userID = %q, want %q", e.UserID, "u-1")
		}
		if e.At.IsZero() {
			t.Errorf("event At zero")
		}
	}
}
