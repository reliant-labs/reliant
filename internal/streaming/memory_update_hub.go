package streaming

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// MEMORY UPDATE HUB
// ============================================================================
// In-process pub/sub for update events. Used in monolith/desktop mode
// where all components run in a single process and no NATS is available.
// ============================================================================

// memoryUpdateSubscription is a single subscriber to update events.
type memoryUpdateSubscription[T any] struct {
	id     uint64
	key    string
	events chan UpdateEvent[T]
	hub    *MemoryUpdateHub[T]
	once   sync.Once
}

func (s *memoryUpdateSubscription[T]) Events() <-chan UpdateEvent[T] {
	return s.events
}

func (s *memoryUpdateSubscription[T]) Unsubscribe() {
	s.hub.unsubscribe(s)
}

// MemoryUpdateHub implements UpdateHub using in-memory channels.
type MemoryUpdateHub[T any] struct {
	// subscribers maps key -> map of subscriber ID -> subscriber
	subscribers sync.Map // map[string]*sync.Map[uint64]*memoryUpdateSubscription[T]
	nextID      atomic.Uint64
	name        string // for logging (e.g., "UserUpdate", "ChatUpdate")
}

// NewMemoryUpdateHub creates a new in-memory update hub.
// name is used in log messages to distinguish hub instances.
func NewMemoryUpdateHub[T any](name string) *MemoryUpdateHub[T] {
	return &MemoryUpdateHub[T]{name: name}
}

// Subscribe creates a new subscription for events matching the given key.
func (h *MemoryUpdateHub[T]) Subscribe(ctx context.Context, key string) UpdateSubscription[T] {
	id := h.nextID.Add(1)

	sub := &memoryUpdateSubscription[T]{
		id:     id,
		key:    key,
		events: make(chan UpdateEvent[T], updateSubscriberBufferSize),
		hub:    h,
	}

	keySubs, _ := h.subscribers.LoadOrStore(key, &sync.Map{})
	keySubs.(*sync.Map).Store(id, sub)

	logging.Debug("[MemoryUpdateHub:"+h.name+"] New subscriber",
		"key", truncateKey(key),
		"subscriberID", id)

	go func() {
		<-ctx.Done()
		h.unsubscribe(sub)
	}()

	return sub
}

// Publish broadcasts an event to all subscribers for the event's key.
func (h *MemoryUpdateHub[T]) Publish(event UpdateEvent[T]) {
	keySubs, ok := h.subscribers.Load(event.Key)
	if !ok {
		return
	}

	keySubs.(*sync.Map).Range(func(_, value any) bool {
		sub := value.(*memoryUpdateSubscription[T])
		select {
		case sub.events <- event:
		default:
			logging.Warn("[MemoryUpdateHub:"+h.name+"] Dropped event (slow consumer)",
				"key", truncateKey(event.Key),
				"subscriberID", sub.id,
				"seq", event.SequenceNumber)
		}
		return true
	})
}

// Close is a no-op for the in-memory hub.
func (h *MemoryUpdateHub[T]) Close() error {
	return nil
}

func (h *MemoryUpdateHub[T]) unsubscribe(sub *memoryUpdateSubscription[T]) {
	sub.once.Do(func() {
		if keySubs, ok := h.subscribers.Load(sub.key); ok {
			keySubs.(*sync.Map).Delete(sub.id)

			// Clean up empty key maps
			isEmpty := true
			keySubs.(*sync.Map).Range(func(_, _ any) bool {
				isEmpty = false
				return false
			})
			if isEmpty {
				h.subscribers.Delete(sub.key)
			}
		}

		close(sub.events)

		logging.Debug("[MemoryUpdateHub:"+h.name+"] Subscriber unsubscribed",
			"key", truncateKey(sub.key),
			"subscriberID", sub.id)
	})
}

// truncateKey shortens a key for log output.
func truncateKey(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}
