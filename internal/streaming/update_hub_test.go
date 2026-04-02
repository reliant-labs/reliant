package streaming

import (
	"context"
	"sync"
	"testing"
	"time"
)

type testUpdate struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

// --- Compile-time interface checks ---

func TestMemoryUpdateHub_ImplementsUpdateHub(t *testing.T) {
	var _ UpdateHub[testUpdate] = (*MemoryUpdateHub[testUpdate])(nil)
}

func TestMemoryUpdateSubscription_ImplementsUpdateSubscription(t *testing.T) {
	var _ UpdateSubscription[testUpdate] = (*memoryUpdateSubscription[testUpdate])(nil)
}

// --- MemoryUpdateHub Tests ---

func TestMemoryUpdateHub_SubscribeAndReceive(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "key-1")

	event := UpdateEvent[testUpdate]{
		Key:            "key-1",
		SequenceNumber: 1,
		Payload:        testUpdate{ID: "u1", Data: "hello"},
	}
	hub.Publish(context.Background(), event)

	select {
	case received := <-sub.Events():
		if received.Key != "key-1" {
			t.Errorf("expected key 'key-1', got %q", received.Key)
		}
		if received.SequenceNumber != 1 {
			t.Errorf("expected seq 1, got %d", received.SequenceNumber)
		}
		if received.Payload.ID != "u1" {
			t.Errorf("expected payload ID 'u1', got %q", received.Payload.ID)
		}
		if received.Payload.Data != "hello" {
			t.Errorf("expected payload Data 'hello', got %q", received.Payload.Data)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestMemoryUpdateHub_KeyIsolation(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subA := hub.Subscribe(ctx, "key-A")
	subB := hub.Subscribe(ctx, "key-B")

	hub.Publish(context.Background(), UpdateEvent[testUpdate]{
		Key:     "key-A",
		Payload: testUpdate{ID: "for-A"},
	})

	// subA should receive the event
	select {
	case received := <-subA.Events():
		if received.Payload.ID != "for-A" {
			t.Errorf("expected payload ID 'for-A', got %q", received.Payload.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event on subA")
	}

	// subB should NOT receive anything
	select {
	case evt := <-subB.Events():
		t.Errorf("subB should not receive events for key-A, got %+v", evt)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestMemoryUpdateHub_MultipleSubscribers(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1 := hub.Subscribe(ctx, "shared-key")
	sub2 := hub.Subscribe(ctx, "shared-key")
	sub3 := hub.Subscribe(ctx, "shared-key")

	hub.Publish(context.Background(), UpdateEvent[testUpdate]{
		Key:     "shared-key",
		Payload: testUpdate{ID: "broadcast"},
	})

	for i, sub := range []UpdateSubscription[testUpdate]{sub1, sub2, sub3} {
		select {
		case received := <-sub.Events():
			if received.Payload.ID != "broadcast" {
				t.Errorf("subscriber %d: expected 'broadcast', got %q", i, received.Payload.ID)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: timeout waiting for event", i)
		}
	}
}

func TestMemoryUpdateHub_Unsubscribe(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "key-unsub")

	// Verify we can receive
	hub.Publish(context.Background(), UpdateEvent[testUpdate]{
		Key:     "key-unsub",
		Payload: testUpdate{ID: "before"},
	})
	select {
	case <-sub.Events():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event before unsubscribe")
	}

	// Unsubscribe
	sub.Unsubscribe()

	// Channel should be closed
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Error("expected channel to be closed after unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for channel close")
	}

	// Publishing after unsubscribe should not panic
	hub.Publish(context.Background(), UpdateEvent[testUpdate]{
		Key:     "key-unsub",
		Payload: testUpdate{ID: "after"},
	})
}

func TestMemoryUpdateHub_ContextCancellation(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())

	sub := hub.Subscribe(ctx, "key-ctx")

	// Cancel the context
	cancel()

	// Give the cleanup goroutine time to run
	time.Sleep(50 * time.Millisecond)

	// Channel should be closed
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Error("expected channel to be closed after context cancellation")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for channel close after context cancellation")
	}
}

func TestMemoryUpdateHub_SlowConsumerDrop(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "key-slow")

	// Fill the buffer completely (buffer size is updateSubscriberBufferSize = 64)
	for i := 0; i < updateSubscriberBufferSize+10; i++ {
		hub.Publish(context.Background(), UpdateEvent[testUpdate]{
			Key:            "key-slow",
			SequenceNumber: int64(i),
			Payload:        testUpdate{ID: "overflow"},
		})
	}

	// Drain what we can — should get exactly the buffer size
	received := 0
	for {
		select {
		case <-sub.Events():
			received++
		default:
			goto done
		}
	}
done:
	if received != updateSubscriberBufferSize {
		t.Errorf("expected %d buffered events, got %d", updateSubscriberBufferSize, received)
	}
}

func TestMemoryUpdateHub_SequenceNumberOrdering(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "key-order")

	count := 20
	for i := 1; i <= count; i++ {
		hub.Publish(context.Background(), UpdateEvent[testUpdate]{
			Key:            "key-order",
			SequenceNumber: int64(i),
			Payload:        testUpdate{ID: "ordered"},
		})
	}

	for i := 1; i <= count; i++ {
		select {
		case received := <-sub.Events():
			if received.SequenceNumber != int64(i) {
				t.Errorf("expected seq %d, got %d", i, received.SequenceNumber)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout waiting for event with seq %d", i)
		}
	}
}

func TestMemoryUpdateHub_EmptyPublish(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")

	// Publishing with no subscribers should not panic
	hub.Publish(context.Background(), UpdateEvent[testUpdate]{
		Key:            "no-subscribers",
		SequenceNumber: 1,
		Payload:        testUpdate{ID: "lonely"},
	})
}

func TestMemoryUpdateHub_Close(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	if err := hub.Close(); err != nil {
		t.Errorf("expected nil error from Close, got %v", err)
	}
}

func TestMemoryUpdateHub_ConcurrentPublishSubscribe(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Spawn concurrent subscribers
	numSubscribers := 5
	subs := make([]UpdateSubscription[testUpdate], numSubscribers)
	for i := 0; i < numSubscribers; i++ {
		subs[i] = hub.Subscribe(ctx, "key-concurrent")
	}

	// Concurrent publishers
	numPublishers := 5
	messagesPerPublisher := 10
	for i := 0; i < numPublishers; i++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			for j := 0; j < messagesPerPublisher; j++ {
				hub.Publish(context.Background(), UpdateEvent[testUpdate]{
					Key:            "key-concurrent",
					SequenceNumber: int64(pid*100 + j),
					Payload:        testUpdate{ID: "concurrent"},
				})
			}
		}(i)
	}

	// Collect from each subscriber
	expectedTotal := numPublishers * messagesPerPublisher
	for si, sub := range subs {
		wg.Add(1)
		go func(idx int, s UpdateSubscription[testUpdate]) {
			defer wg.Done()
			received := 0
			timeout := time.After(2 * time.Second)
			for received < expectedTotal {
				select {
				case <-s.Events():
					received++
				case <-timeout:
					t.Errorf("subscriber %d: timeout after %d/%d events", idx, received, expectedTotal)
					return
				}
			}
		}(si, sub)
	}

	wg.Wait()
}

func TestMemoryUpdateHub_DoubleUnsubscribe(t *testing.T) {
	hub := NewMemoryUpdateHub[testUpdate]("test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := hub.Subscribe(ctx, "key-double")

	// Double unsubscribe should not panic (protected by sync.Once)
	sub.Unsubscribe()
	sub.Unsubscribe()
}

// --- UpdateEvent JSON round-trip ---

func TestUpdateEvent_JSONRoundTrip(t *testing.T) {
	original := UpdateEvent[testUpdate]{
		Key:            "user-123",
		SequenceNumber: 42,
		Payload:        testUpdate{ID: "u1", Data: "hello"},
	}

	data, err := original.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var decoded UpdateEvent[testUpdate]
	if err := decoded.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if decoded.Key != original.Key {
		t.Errorf("Key: got %q, want %q", decoded.Key, original.Key)
	}
	if decoded.SequenceNumber != original.SequenceNumber {
		t.Errorf("SequenceNumber: got %d, want %d", decoded.SequenceNumber, original.SequenceNumber)
	}
	if decoded.Payload.ID != original.Payload.ID {
		t.Errorf("Payload.ID: got %q, want %q", decoded.Payload.ID, original.Payload.ID)
	}
	if decoded.Payload.Data != original.Payload.Data {
		t.Errorf("Payload.Data: got %q, want %q", decoded.Payload.Data, original.Payload.Data)
	}
}
