package streaming

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// MEMORY STREAMING HUB
// ============================================================================
// The MemoryHub provides in-memory pub/sub for ephemeral streaming events.
//
// Key features:
// - Per-chat subscriptions (multiple subscribers per chat)
// - Non-blocking publish (drops messages to slow consumers)
// - Thread-safe
// - Automatic cleanup on unsubscribe
// ============================================================================

const (
	// subscriberBufferSize is the channel buffer size for each subscriber.
	// Larger buffer = more tolerance for slow consumers, more memory.
	subscriberBufferSize = 100
)

// MemorySubscription represents a single subscription to chat events.
type MemorySubscription struct {
	id     uint64
	chatID string
	events chan StreamingDelta
	hub    *MemoryHub
	once   sync.Once
}

// Events returns the channel to receive streaming deltas.
func (s *MemorySubscription) Events() <-chan StreamingDelta {
	return s.events
}

// Unsubscribe removes this subscriber from the hub.
func (s *MemorySubscription) Unsubscribe() {
	s.hub.unsubscribe(s)
}

// MemoryHub manages streaming subscriptions and event distribution in-memory.
type MemoryHub struct {
	// subscribers maps chatID -> map of subscriber ID -> subscriber
	subscribers sync.Map // map[string]*sync.Map[uint64]*MemorySubscription

	// nextID is an atomic counter for unique subscriber IDs
	nextID atomic.Uint64

	// metrics for observability
	publishCount   atomic.Uint64
	dropCount      atomic.Uint64
	subscribeCount atomic.Uint64
}

// NewMemoryHub creates a new in-memory streaming hub.
func NewMemoryHub() *MemoryHub {
	return &MemoryHub{}
}

// Subscribe creates a new subscription for events from a specific chat.
func (h *MemoryHub) Subscribe(ctx context.Context, chatID string) Subscription {
	id := h.nextID.Add(1)
	h.subscribeCount.Add(1)

	sub := &MemorySubscription{
		id:     id,
		chatID: chatID,
		events: make(chan StreamingDelta, subscriberBufferSize),
		hub:    h,
	}

	// Get or create the subscriber map for this chat
	chatSubs, _ := h.subscribers.LoadOrStore(chatID, &sync.Map{})
	chatSubs.(*sync.Map).Store(id, sub)

	logging.Debug("[StreamingHub] New subscriber",
		"chatID", chatID[:min(8, len(chatID))],
		"subscriberID", id)

	// Start goroutine to clean up on context cancellation
	go func() {
		<-ctx.Done()
		h.unsubscribe(sub)
	}()

	return sub
}

// unsubscribe removes a subscriber from the hub.
func (h *MemoryHub) unsubscribe(sub *MemorySubscription) {
	sub.once.Do(func() {
		if chatSubs, ok := h.subscribers.Load(sub.chatID); ok {
			chatSubs.(*sync.Map).Delete(sub.id)

			// Clean up empty chat maps (optional, for memory efficiency)
			isEmpty := true
			chatSubs.(*sync.Map).Range(func(_, _ interface{}) bool {
				isEmpty = false
				return false
			})
			if isEmpty {
				h.subscribers.Delete(sub.chatID)
			}
		}

		// Close the events channel to signal completion
		close(sub.events)

		logging.Debug("[StreamingHub] Subscriber unsubscribed",
			"chatID", sub.chatID[:min(8, len(sub.chatID))],
			"subscriberID", sub.id)
	})
}

// Publish broadcasts a streaming delta to all subscribers of a chat.
// This is non-blocking - if a subscriber's buffer is full, the event is dropped.
func (h *MemoryHub) Publish(chatID string, delta StreamingDelta) {
	h.publishCount.Add(1)

	chatSubs, ok := h.subscribers.Load(chatID)
	if !ok {
		// No subscribers for this chat - event is silently dropped
		return
	}

	chatSubs.(*sync.Map).Range(func(_, value interface{}) bool {
		sub := value.(*MemorySubscription)
		select {
		case sub.events <- delta:
			// Successfully sent
		default:
			// Buffer full - drop the event for this subscriber
			h.dropCount.Add(1)
			logging.Warn("[StreamingHub] Dropped event (slow consumer)",
				"chatID", chatID[:min(8, len(chatID))],
				"subscriberID", sub.id,
				"deltaType", delta.DeltaType)
		}
		return true
	})
}

// PublishEvent is a convenience method that extracts chatID from a ChatEvent.
func (h *MemoryHub) PublishEvent(event ChatEvent) {
	h.Publish(event.ChatID, event.Delta)
}

// SubscriberCount returns the number of subscribers for a chat.
func (h *MemoryHub) SubscriberCount(chatID string) int {
	chatSubs, ok := h.subscribers.Load(chatID)
	if !ok {
		return 0
	}

	count := 0
	chatSubs.(*sync.Map).Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// TotalSubscriberCount returns the total number of active subscribers.
func (h *MemoryHub) TotalSubscriberCount() int {
	total := 0
	h.subscribers.Range(func(_, value interface{}) bool {
		value.(*sync.Map).Range(func(_, _ interface{}) bool {
			total++
			return true
		})
		return true
	})
	return total
}

// HubStats holds current hub statistics.
type HubStats struct {
	TotalPublished   uint64
	TotalDropped     uint64
	TotalSubscribers uint64
	ActiveChats      int
}

// Stats returns current hub statistics.
func (h *MemoryHub) Stats() HubStats {
	activeChats := 0
	h.subscribers.Range(func(_, _ interface{}) bool {
		activeChats++
		return true
	})

	return HubStats{
		TotalPublished:   h.publishCount.Load(),
		TotalDropped:     h.dropCount.Load(),
		TotalSubscribers: h.subscribeCount.Load(),
		ActiveChats:      activeChats,
	}
}

// IsConnected always returns true for the in-memory hub.
func (h *MemoryHub) IsConnected() bool { return true }

// Close shuts down the hub and releases resources.
// For the in-memory hub this is a no-op.
func (h *MemoryHub) Close() error {
	return nil
}
