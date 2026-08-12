package streaming

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natstest "github.com/nats-io/nats-server/v2/test"
)

// startCoreNATS spins up an in-process core-NATS server (no JetStream —
// NATSUpdateHub is deliberately core pub/sub) and returns a connection to it.
func startCoreNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func newTestUpdateHub(t *testing.T) *NATSUpdateHub[string] {
	t.Helper()
	hub := NewNATSUpdateHub[string](startCoreNATS(t), "test.updates", "test")
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}

// TestUpdateHub_DeliversEvents is the happy path: an event published for a key
// reaches that key's subscriber intact.
func TestUpdateHub_DeliversEvents(t *testing.T) {
	hub := newTestUpdateHub(t)
	ctx := t.Context()

	sub := hub.Subscribe(ctx, "user-1")
	// Core NATS has no retention: a publish that beats the subscription's
	// interest reaching the server is dropped. Flush so the SUB is registered
	// server-side before we publish.
	if err := hub.nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	hub.Publish(ctx, UpdateEvent[string]{Key: "user-1", SequenceNumber: 7, Payload: "hello"})

	select {
	case got, ok := <-sub.Events():
		if !ok {
			t.Fatal("events channel closed before delivery")
		}
		if got.SequenceNumber != 7 || got.Payload != "hello" {
			t.Errorf("got %+v, want seq=7 payload=hello", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// TestUpdateHub_UnsubscribeRacingDeliveryNoPanic is the regression test for the
// "panic: send on closed channel" that killed the api server.
//
// The old implementation delivered via an async nats.Handler callback and
// closed sub.events from unsubscribe(). nats.Subscription.Unsubscribe does NOT
// wait for a callback already dispatched into waitForMsgs, so a delivery in
// flight could send on the just-closed channel and take the whole process
// down. Production hit this on StreamUserUpdates reconnect churn: streams tore
// down and resubscribed every few seconds while events were flowing.
//
// The loop below reproduces that shape — publish continuously while tearing
// subscriptions down without draining them — many times over, so the race
// window is hit. The assertion is simply that the process survives; a
// regression panics here (a panic in the NATS delivery goroutine is not
// recoverable from the test goroutine, so it fails the run outright).
func TestUpdateHub_UnsubscribeRacingDeliveryNoPanic(t *testing.T) {
	hub := newTestUpdateHub(t)
	ctx := t.Context()

	const rounds = 200

	for r := range rounds {
		key := fmt.Sprintf("user-%d", r)
		sub := hub.Subscribe(ctx, key)
		if err := hub.nc.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}

		// A publisher that runs for the WHOLE round, so deliveries are still
		// arriving when the teardown lands. Timing is what makes this bite:
		// the delivery goroutine runs the per-message work outside the
		// subscription lock, so Unsubscribe returns while a delivery is
		// mid-flight, and the window is only as wide as one message's
		// unmarshal + send. Publishing continuously — rather than a fixed
		// burst that may drain before teardown — keeps a delivery in that
		// window when Unsubscribe lands.
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				hub.Publish(ctx, UpdateEvent[string]{
					Key:            key,
					SequenceNumber: int64(i),
					Payload:        "x",
				})
			}
		})

		// Block until delivery is demonstrably flowing, so Unsubscribe lands
		// mid-stream rather than before the first message ever arrives.
		select {
		case <-sub.Events():
		case <-time.After(5 * time.Second):
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: no events delivered", r)
		}

		// Tear down mid-flight, without draining. The buffer is full by now,
		// so deliveries are taking the non-blocking drop branch at full tilt.
		sub.Unsubscribe()

		close(stop)
		wg.Wait()
	}

	// Teardown is asynchronous (the consume loop observes cancellation, then
	// deregisters and closes). Give the loops a moment, then confirm every
	// subscriber was unhooked — a leak here means the close never ran.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.subscriberCount() > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := hub.subscriberCount(); n != 0 {
		t.Errorf("subscribers still registered after unsubscribe = %d, want 0", n)
	}
}

// TestUpdateHub_ContextCancelClosesChannel verifies the other teardown entry
// point: cancelling the subscription's context must close the events channel
// exactly once, so consumers ranging over it terminate.
func TestUpdateHub_ContextCancelClosesChannel(t *testing.T) {
	hub := newTestUpdateHub(t)
	ctx, cancel := context.WithCancel(context.Background())

	sub := hub.Subscribe(ctx, "user-cancel")
	if err := hub.nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cancel()

	// Drain to the close; a channel that never closes hangs the select.
	for {
		select {
		case _, ok := <-sub.Events():
			if !ok {
				return // closed — the consume loop owned it
			}
		case <-time.After(2 * time.Second):
			t.Fatal("events channel was never closed after context cancel")
		}
	}
}

// TestUpdateHub_DoubleUnsubscribeIsSafe covers the idempotency the streaming
// service relies on: an explicit Unsubscribe followed by the deferred
// context cancel (and Close() on top) must not double-close.
func TestUpdateHub_DoubleUnsubscribeIsSafe(t *testing.T) {
	hub := newTestUpdateHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "user-double")
	if err := hub.nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	sub.Unsubscribe()
	sub.Unsubscribe()
	if err := hub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cancel()

	// Reaching here without a panic is the assertion.
	time.Sleep(100 * time.Millisecond)
}

// subscriberCount reports how many subscriptions are still registered across
// all keys. Test-only helper.
func (h *NATSUpdateHub[T]) subscriberCount() int {
	n := 0
	h.subscribers.Range(func(_, keySubs any) bool {
		keySubs.(*sync.Map).Range(func(_, _ any) bool {
			n++
			return true
		})
		return true
	})
	return n
}
