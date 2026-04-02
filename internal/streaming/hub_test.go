package streaming

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHub_PublishSubscribe(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "test-chat-123"

	// Subscribe to the chat
	sub := hub.Subscribe(ctx, chatID)

	// Publish a delta
	delta := StreamingDelta{
		DeltaType: DeltaTypeContentBlockDelta,
		Delta:     "Hello, World!",
	}
	hub.Publish(context.Background(), chatID, delta)

	// Receive the delta
	select {
	case received := <-sub.Events():
		if received.Delta != "Hello, World!" {
			t.Errorf("expected delta 'Hello, World!', got %q", received.Delta)
		}
		if received.DeltaType != DeltaTypeContentBlockDelta {
			t.Errorf("expected delta type %q, got %q", DeltaTypeContentBlockDelta, received.DeltaType)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for delta")
	}
}

func TestHub_MultipleSubscribers(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "test-chat-multi"

	// Create multiple subscribers
	sub1 := hub.Subscribe(ctx, chatID)
	sub2 := hub.Subscribe(ctx, chatID)

	// Publish a delta
	delta := StreamingDelta{
		DeltaType: DeltaTypeMessageStart,
		Delta:     "broadcast",
	}
	hub.Publish(context.Background(), chatID, delta)

	// Both should receive the delta
	for i, sub := range []Subscription{sub1, sub2} {
		select {
		case received := <-sub.Events():
			if received.Delta != "broadcast" {
				t.Errorf("subscriber %d: expected delta 'broadcast', got %q", i, received.Delta)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: timeout waiting for delta", i)
		}
	}
}

func TestHub_UnsubscribeOnContextCancel(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())

	chatID := "test-chat-cancel"
	sub := hub.Subscribe(ctx, chatID)

	// Verify subscriber count
	if count := hub.SubscriberCount(chatID); count != 1 {
		t.Errorf("expected 1 subscriber, got %d", count)
	}

	// Cancel the context
	cancel()

	// Give time for cleanup goroutine
	time.Sleep(50 * time.Millisecond)

	// Channel should be closed
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for channel close")
	}

	// Subscriber count should be 0
	if count := hub.SubscriberCount(chatID); count != 0 {
		t.Errorf("expected 0 subscribers after cancel, got %d", count)
	}
}

func TestHub_DifferentChats(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chat1 := "chat-1"
	chat2 := "chat-2"

	sub1 := hub.Subscribe(ctx, chat1)
	sub2 := hub.Subscribe(ctx, chat2)

	// Publish to chat1 only
	hub.Publish(context.Background(), chat1, StreamingDelta{Delta: "for-chat-1"})

	// sub1 should receive it
	select {
	case received := <-sub1.Events():
		if received.Delta != "for-chat-1" {
			t.Errorf("expected 'for-chat-1', got %q", received.Delta)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for delta on chat1")
	}

	// sub2 should NOT receive it (non-blocking check)
	select {
	case <-sub2.Events():
		t.Error("sub2 should not receive events from chat1")
	case <-time.After(50 * time.Millisecond):
		// Expected - no event for chat2
	}
}

func TestHub_ConcurrentPublish(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chatID := "test-concurrent"
	sub := hub.Subscribe(ctx, chatID)

	// Publish concurrently
	var wg sync.WaitGroup
	numPublishers := 10
	messagesPerPublisher := 10

	for i := 0; i < numPublishers; i++ {
		wg.Add(1)
		go func(publisherID int) {
			defer wg.Done()
			for j := 0; j < messagesPerPublisher; j++ {
				hub.Publish(context.Background(), chatID, StreamingDelta{
					DeltaType:  DeltaTypeContentBlockDelta,
					BlockIndex: publisherID*100 + j,
				})
			}
		}(i)
	}

	// Collect received messages
	received := 0
	timeout := time.After(2 * time.Second)
	expectedTotal := numPublishers * messagesPerPublisher

collectLoop:
	for received < expectedTotal {
		select {
		case <-sub.Events():
			received++
		case <-timeout:
			break collectLoop
		}
	}

	wg.Wait()

	// Should receive all messages (assuming buffer is large enough)
	if received != expectedTotal {
		t.Errorf("expected %d messages, received %d", expectedTotal, received)
	}
}

func TestHub_Stats(t *testing.T) {
	hub := NewMemoryHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initial stats
	stats := hub.Stats()
	if stats.TotalPublished != 0 {
		t.Errorf("expected 0 published, got %d", stats.TotalPublished)
	}

	// Subscribe and publish
	hub.Subscribe(ctx, "chat-1")
	hub.Subscribe(ctx, "chat-2")
	hub.Publish(context.Background(), "chat-1", StreamingDelta{})
	hub.Publish(context.Background(), "chat-1", StreamingDelta{})
	hub.Publish(context.Background(), "chat-2", StreamingDelta{})

	stats = hub.Stats()
	if stats.TotalPublished != 3 {
		t.Errorf("expected 3 published, got %d", stats.TotalPublished)
	}
	if stats.ActiveChats != 2 {
		t.Errorf("expected 2 active chats, got %d", stats.ActiveChats)
	}
}

func TestNewStreamingHub_Memory(t *testing.T) {
	hub, err := NewStreamingHub(StreamingConfig{Driver: DriverMemory})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}
}

func TestNewStreamingHub_DefaultIsMemory(t *testing.T) {
	hub, err := NewStreamingHub(StreamingConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}
}

func TestNewStreamingHub_UnknownDriver(t *testing.T) {
	_, err := NewStreamingHub(StreamingConfig{Driver: "redis"})
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
}

func TestParseStreamingDriver(t *testing.T) {
	tests := []struct {
		input    string
		expected StreamingDriver
		wantErr  bool
	}{
		{"", DriverMemory, false},
		{"memory", DriverMemory, false},
		{"MEMORY", DriverMemory, false},
		{"nats", DriverNATS, false},
		{"NATS", DriverNATS, false},
		{" nats ", DriverNATS, false},
		{"redis", "", true},
		{"kafka", "", true},
	}

	for _, tt := range tests {
		t.Run("input="+tt.input, func(t *testing.T) {
			got, err := ParseStreamingDriver(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseStreamingDriver(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseStreamingDriver(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMemoryHub_Close(t *testing.T) {
	hub := NewMemoryHub()
	if err := hub.Close(); err != nil {
		t.Errorf("expected nil error from Close, got %v", err)
	}
}

func TestMemoryHub_ImplementsStreamingHub(t *testing.T) {
	// Compile-time check that MemoryHub implements StreamingHub
	var _ StreamingHub = (*MemoryHub)(nil)
}

func TestMemorySubscription_ImplementsSubscription(t *testing.T) {
	// Compile-time check that MemorySubscription implements Subscription
	var _ Subscription = (*MemorySubscription)(nil)
}
