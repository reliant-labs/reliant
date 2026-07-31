package streaming

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
)

// startJetStreamNATS spins up an in-process JetStream-enabled NATS server and
// returns its client URL. Mirrors the harness used elsewhere in the repo, with
// JetStream turned on so NATSHub can create its stream.
func startJetStreamNATS(t *testing.T) string {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	// Wait for JetStream to be enabled.
	deadline := time.Now().Add(5 * time.Second)
	for !srv.JetStreamEnabled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !srv.JetStreamEnabled() {
		t.Fatal("jetstream did not enable")
	}
	return srv.ClientURL()
}

func newTestHub(t *testing.T) *NATSHub {
	t.Helper()
	hub, err := NewNATSHub(startJetStreamNATS(t))
	if err != nil {
		t.Fatalf("NewNATSHub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}

// drainAll reads deltas off a subscription until it has been idle for quiet,
// reassembling the per-block text so we can assert nothing was lost.
func drainAll(sub Subscription, quiet time.Duration) []StreamingDelta {
	var got []StreamingDelta
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case d, ok := <-sub.Events():
			if !ok {
				return got
			}
			got = append(got, d)
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(quiet)
		case <-timer.C:
			return got
		}
	}
}

// TestConsumeLoop_NoLossUnderBackpressure is the regression test for the
// silent-drop bug: a fast producer + a slow consumer must not lose any content
// text. Before the fix, the non-blocking send dropped deltas once the 100-slot
// buffer filled; now they are coalesced/blocked so all text survives.
func TestConsumeLoop_NoLossUnderBackpressure(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "chat-backpressure"
	sub := hub.Subscribe(ctx, chatID)

	// Publish far more than the 100-slot channel buffer, all same thread+block,
	// each a single character, so we can verify exact reassembly.
	const n = 1000
	var want strings.Builder
	for i := 0; i < n; i++ {
		ch := string(rune('a' + (i % 26)))
		want.WriteString(ch)
		hub.Publish(ctx, chatID, contentDelta("", 0, ch))
	}
	// Terminal structural delta marks end-of-message; it must never be dropped.
	hub.Publish(ctx, chatID, StreamingDelta{DeltaType: DeltaTypeContentBlockStop, BlockIndex: 0})

	// Consumer deliberately starts draining only after a delay, forcing the
	// producer side to hit backpressure and coalesce.
	time.Sleep(200 * time.Millisecond)
	got := drainAll(sub, 2*time.Second)

	if len(got) == 0 {
		t.Fatal("received no deltas")
	}

	// The terminal structural delta must be present and last.
	last := got[len(got)-1]
	if last.DeltaType != DeltaTypeContentBlockStop {
		t.Errorf("last delta = %q, want content_block_stop (structural delta must never be dropped)", last.DeltaType)
	}

	// Reassemble all content text; it must equal exactly what was published,
	// in order, with nothing lost — whether coalesced or not.
	var reassembled strings.Builder
	for _, d := range got {
		if d.DeltaType == DeltaTypeContentBlockDelta {
			reassembled.WriteString(d.Delta)
		}
	}
	if reassembled.String() != want.String() {
		t.Errorf("reassembled text mismatch:\n got len=%d\nwant len=%d\nfirst-diff-context…",
			reassembled.Len(), want.Len())
	}

	// We should have coalesced at least once (far more than 100 pending sends).
	if hub.Stats().TotalCoalesced == 0 {
		t.Error("expected some coalescing under backpressure, got zero")
	}
	// And we must not have dropped anything.
	if d := hub.Stats().TotalDropped; d != 0 {
		t.Errorf("TotalDropped = %d, want 0 (backpressure must not drop)", d)
	}
}

// TestConsumeLoop_StructuralDeltasNeverCoalesced verifies that distinct
// structural deltas each arrive intact and in order even under load — they are
// the events whose loss caused phantom placeholders and stuck tool calls.
func TestConsumeLoop_StructuralDeltasNeverCoalesced(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "chat-structural"
	sub := hub.Subscribe(ctx, chatID)

	seq := []StreamingDelta{
		{DeltaType: DeltaTypeContentBlockStart, BlockIndex: 0},
		contentDelta("", 0, "hello"),
		{DeltaType: DeltaTypeContentBlockStop, BlockIndex: 0},
		{DeltaType: DeltaTypeToolUse, BlockIndex: 1, ToolCall: &StreamingToolCall{ID: "t1", Name: "bash"}},
		{DeltaType: DeltaTypeMessageStop},
	}
	for _, d := range seq {
		hub.Publish(ctx, chatID, d)
	}

	got := drainAll(sub, 2*time.Second)

	var structural []DeltaType
	for _, d := range got {
		if d.DeltaType != DeltaTypeContentBlockDelta {
			structural = append(structural, d.DeltaType)
		}
	}
	want := []DeltaType{
		DeltaTypeContentBlockStart,
		DeltaTypeContentBlockStop,
		DeltaTypeToolUse,
		DeltaTypeMessageStop,
	}
	if fmt.Sprint(structural) != fmt.Sprint(want) {
		t.Errorf("structural deltas = %v, want %v (order preserved, none dropped)", structural, want)
	}
}

// TestConsumeLoop_UnsubscribeNoPanic exercises the teardown path: cancelling
// the subscription while the loop may be mid-delivery must not panic (send on
// closed channel) — the consume loop owns the close.
func TestConsumeLoop_UnsubscribeNoPanic(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "chat-teardown"
	sub := hub.Subscribe(ctx, chatID)

	for i := 0; i < 500; i++ {
		hub.Publish(ctx, chatID, contentDelta("", 0, "x"))
	}
	// Tear down without draining — the loop may be parked in a blocking send.
	sub.Unsubscribe()

	// Give the loop a moment to observe cancellation and close cleanly.
	time.Sleep(200 * time.Millisecond)
	// Reaching here without a panic is the assertion; also confirm the
	// subscriber was deregistered.
	if c := hub.SubscriberCount(chatID); c != 0 {
		t.Errorf("SubscriberCount after unsubscribe = %d, want 0", c)
	}
}
