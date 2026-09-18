package streaming

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Regression test for the reconnect-storm bug.
//
// STREAMING_DELTAS is a MemoryStorage stream created exactly once, in
// NewNATSHub, at api-server startup. When NATS restarted underneath a running
// api-server the stream was gone and nothing recreated it, so every
// OrderedConsumer call failed with "stream not found" for the life of the
// process.
//
// That failure is not contained inside the hub: a failed Subscribe ends the
// StreamUserUpdates RPC, the client treats the ended stream as a disconnect and
// reconnects, resubscribes, fails again. Observed in dev at ~20 cycles/second
// for over three hours, each cycle dragging a full ListChats / GetChat /
// ListArchivedChats refetch behind it.
//
// The hub must heal itself: recreate the missing stream and retry the consumer,
// so a subscription taken out after a NATS restart still delivers deltas.
func TestConsumeLoop_RecreatesStreamAfterNATSWipe(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulate the NATS restart: the memory-backed stream vanishes while the
	// hub keeps its live connection. Deleting the stream reproduces exactly the
	// state the 404 came from, without restarting the test server.
	delCtx, delCancel := context.WithTimeout(ctx, 5*time.Second)
	defer delCancel()
	if err := hub.js.DeleteStream(delCtx, natsStreamName); err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if _, err := hub.js.Stream(delCtx, natsStreamName); err == nil {
		t.Fatal("stream still present after delete; precondition not met")
	}

	// A subscription taken out now is the one that used to fail forever.
	chatID := "chat-after-wipe"
	sub := hub.Subscribe(ctx, chatID)

	// The consume loop recreates the stream asynchronously; wait for it to
	// exist before publishing, otherwise the publish races the recreate and
	// lands on a stream that does not exist yet.
	waitForStream(t, hub, 5*time.Second)

	hub.Publish(ctx, chatID, contentDelta("", 0, "hello"))
	hub.Publish(ctx, chatID, StreamingDelta{DeltaType: DeltaTypeContentBlockStop, BlockIndex: 0})

	got := drainAll(sub, 3*time.Second)
	if len(got) == 0 {
		t.Fatal("received no deltas after NATS wiped the stream: the hub did not recover, " +
			"which is what drove the client reconnect storm")
	}

	var text string
	sawStop := false
	for _, d := range got {
		switch d.DeltaType {
		case DeltaTypeContentBlockDelta:
			text += d.Delta
		case DeltaTypeContentBlockStop:
			sawStop = true
		}
	}
	if text != "hello" {
		t.Errorf("reassembled text = %q, want %q", text, "hello")
	}
	if !sawStop {
		t.Error("structural content_block_stop delta missing after stream recreate")
	}
}

// TestEnsureStream_Idempotent pins that healing an already-healthy stream is a
// no-op, since the subscribe path may call it concurrently for many chats.
func TestEnsureStream_Idempotent(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < 3; i++ {
		if err := hub.ensureStream(ctx); err != nil {
			t.Fatalf("ensureStream call %d: %v", i, err)
		}
	}

	stream, err := hub.js.Stream(ctx, natsStreamName)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Stream.Info: %v", err)
	}
	if info.Config.Storage != jetstream.MemoryStorage {
		t.Errorf("Storage = %v, want MemoryStorage", info.Config.Storage)
	}
	if info.Config.MaxAge != 5*time.Minute {
		t.Errorf("MaxAge = %v, want 5m", info.Config.MaxAge)
	}
}

// waitForStream blocks until the deltas stream exists, so a publish cannot race
// the asynchronous recreate performed by the consume loop.
func waitForStream(t *testing.T, hub *NATSHub, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, err := hub.js.Stream(ctx, natsStreamName); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("deltas stream was never recreated after the NATS wipe")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
