// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/pkg/daemonwire"
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
		byType = map[daemonwire.EventType]int{}
		got    []daemonwire.Event
	)
	sub, err := nc.Subscribe(daemonwire.SubjectWildcard, func(msg *nats.Msg) {
		var e daemonwire.Event
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
	p.Connected(daemonID, "u-1", "cloud", daemonwire.Build{})
	p.Activity(daemonID, "u-1", "cloud", "tool call shell")
	p.Activity(daemonID, "u-1", "cloud", "terminal input")
	p.Activity(daemonID, "u-1", "cloud", "terminal input")
	p.Disconnected(daemonID, "u-1", "cloud")

	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Drain a short window so the async subscription handler runs.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		total := byType[daemonwire.EventConnected] + byType[daemonwire.EventDisconnected] + byType[daemonwire.EventActivity]
		mu.Unlock()
		if total >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if byType[daemonwire.EventConnected] != 1 {
		t.Errorf("connected count = %d, want 1", byType[daemonwire.EventConnected])
	}
	if byType[daemonwire.EventActivity] != 1 {
		t.Errorf("activity count = %d, want 1 (3 calls should collapse under rate limit)", byType[daemonwire.EventActivity])
	}
	if byType[daemonwire.EventDisconnected] != 1 {
		t.Errorf("disconnected count = %d, want 1", byType[daemonwire.EventDisconnected])
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
		// The window's first reason is the one that travels: it is what the
		// control plane logs as having kept the machine awake.
		if e.Type == daemonwire.EventActivity && e.Reason != "tool call shell" {
			t.Errorf("activity reason = %q, want %q", e.Reason, "tool call shell")
		}
	}
}

// UpgradeChanged publishes an upgrade event on its own subject, with the
// upgrade intact and no build: it is not a connect.
func TestPublisher_UpgradeChangedIsAnUpgradeEvent(t *testing.T) {
	nc := startTestNATS(t)
	sub, err := nc.SubscribeSync(daemonwire.SubjectWildcard)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	deadline := time.UnixMilli(1792800123456).UTC()
	want := daemonwire.Upgrade{State: daemonwire.UpgradeStateDrained, RolloutID: "roll-1", Deadline: &deadline}
	NewPublisher(nc).UpgradeChanged("d-1", "u-1", "cloud", want)

	msg, err := sub.NextMsg(30 * time.Second)
	if err != nil {
		t.Fatalf("no event published: %v", err)
	}
	if msg.Subject != daemonwire.Subject("d-1", daemonwire.EventUpgrade) {
		t.Fatalf("subject = %q, want the upgrade subject", msg.Subject)
	}
	evt, err := daemonwire.UnmarshalEvent(msg.Data)
	if err != nil {
		t.Fatalf("UnmarshalEvent: %v", err)
	}
	if evt.Type != daemonwire.EventUpgrade || evt.UserID != "u-1" || evt.DaemonType != "cloud" {
		t.Fatalf("event = %+v", evt)
	}
	if evt.Build != nil || evt.Reason != "" {
		t.Fatalf("an upgrade event carries a build or a reason: %+v", evt)
	}
	if evt.Upgrade == nil || evt.Upgrade.State != want.State || evt.Upgrade.RolloutID != want.RolloutID ||
		evt.Upgrade.Deadline == nil || !evt.Upgrade.Deadline.Equal(deadline) {
		t.Fatalf("Upgrade = %+v, want %+v", evt.Upgrade, want)
	}
}
