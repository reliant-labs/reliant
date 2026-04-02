package streaming

import (
	"context"
	"encoding/json"
)

// ============================================================================
// UPDATE HUB — Generic pub/sub for durable update events
// ============================================================================
// UpdateHub provides event-driven notifications for updates that are also
// persisted to the database. It eliminates the need for polling by pushing
// events to subscribers in real-time.
//
// Two implementations:
//   - MemoryUpdateHub: in-process fan-out (monolith/desktop)
//   - NATSUpdateHub:   core NATS pub/sub (distributed/cloud)
//
// The database remains the source of truth. UpdateHub is the notification
// channel — on reconnect, clients catch up from the DB, then switch to
// the hub for real-time delivery.
// ============================================================================

const (
	// updateSubscriberBufferSize is the channel buffer for each update subscriber.
	updateSubscriberBufferSize = 64
)

// UpdateEvent wraps an update payload with routing metadata.
// T is typically db.UserUpdate or db.ChatUpdate.
type UpdateEvent[T any] struct {
	Key            string // Routing key: userID for user updates, chatID for chat updates
	SequenceNumber int64  // DB sequence number for dedup/ordering
	Payload        T      // The full update struct
}

// MarshalJSON implements JSON encoding for NATS transport.
func (e UpdateEvent[T]) MarshalJSON() ([]byte, error) {
	type wire struct {
		Key            string `json:"key"`
		SequenceNumber int64  `json:"seq"`
		Payload        T      `json:"payload"`
	}
	return json.Marshal(wire(e))
}

// UnmarshalJSON implements JSON decoding for NATS transport.
func (e *UpdateEvent[T]) UnmarshalJSON(data []byte) error {
	type wire struct {
		Key            string          `json:"key"`
		SequenceNumber int64           `json:"seq"`
		Payload        json.RawMessage `json:"payload"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.Key = w.Key
	e.SequenceNumber = w.SequenceNumber
	return json.Unmarshal(w.Payload, &e.Payload)
}

// UpdateSubscription represents a subscription to update events for a specific key.
type UpdateSubscription[T any] interface {
	// Events returns the channel to receive update events.
	Events() <-chan UpdateEvent[T]

	// Unsubscribe removes this subscription and closes the events channel.
	Unsubscribe()
}

// UpdateHub is a generic pub/sub hub for durable update events.
// Implementations handle both in-process and distributed delivery.
type UpdateHub[T any] interface {
	// Publish broadcasts an update event to all subscribers for the event's key.
	// Non-blocking — slow consumers may miss events (they catch up from DB).
	Publish(ctx context.Context, event UpdateEvent[T])

	// Subscribe creates a new subscription for events matching the given key.
	// The subscription is automatically cleaned up when ctx is cancelled.
	Subscribe(ctx context.Context, key string) UpdateSubscription[T]

	// Close shuts down the hub and releases resources.
	Close() error
}