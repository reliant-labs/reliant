package streaming

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// NATS UPDATE HUB
// ============================================================================
// Distributed pub/sub for update events using core NATS (not JetStream).
// Used in cloud/distributed mode where multiple API server replicas need
// to receive update notifications.
//
// Core NATS is used intentionally — these events are already durable in the
// database. NATS is purely the real-time notification channel. No retention,
// no replay, no JetStream overhead.
//
// Subject pattern: {prefix}.{key}
//   e.g., "user.updates.abc123" or "chat.updates.xyz789"
// ============================================================================

// NATSUpdateHub implements UpdateHub using core NATS pub/sub.
type NATSUpdateHub[T any] struct {
	nc            *nats.Conn
	subjectPrefix string // e.g., "user.updates" or "chat.updates"
	name          string // for logging

	subscribers sync.Map // map[key]*sync.Map[id]*natsUpdateSubscription[T]
	nextID      atomic.Uint64
}

// natsUpdateSubscription is a single subscriber backed by a NATS subscription.
type natsUpdateSubscription[T any] struct {
	id       uint64
	key      string
	events   chan UpdateEvent[T]
	natsSub  *nats.Subscription
	hub      *NATSUpdateHub[T]
	once     sync.Once
	cancelFn context.CancelFunc
}

func (s *natsUpdateSubscription[T]) Events() <-chan UpdateEvent[T] {
	return s.events
}

func (s *natsUpdateSubscription[T]) Unsubscribe() {
	s.hub.unsubscribe(s)
}

// NewNATSUpdateHub creates a new NATS-backed update hub.
//
// Parameters:
//   - nc: an existing NATS connection (shared across hubs)
//   - subjectPrefix: the NATS subject prefix (e.g., "user.updates" or "chat.updates")
//   - name: human-readable name for logging
func NewNATSUpdateHub[T any](nc *nats.Conn, subjectPrefix string, name string) *NATSUpdateHub[T] {
	return &NATSUpdateHub[T]{
		nc:            nc,
		subjectPrefix: subjectPrefix,
		name:          name,
	}
}

// Publish broadcasts an event via NATS.
func (h *NATSUpdateHub[T]) Publish(event UpdateEvent[T]) {
	data, err := json.Marshal(event)
	if err != nil {
		logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to marshal event",
			"key", truncateKey(event.Key),
			"error", err)
		return
	}

	subject := h.subjectPrefix + "." + event.Key
	if err := h.nc.Publish(subject, data); err != nil {
		logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to publish",
			"subject", subject,
			"error", err)
	}
}

// Subscribe creates a new subscription for events matching the given key.
func (h *NATSUpdateHub[T]) Subscribe(ctx context.Context, key string) UpdateSubscription[T] {
	id := h.nextID.Add(1)
	subCtx, cancel := context.WithCancel(ctx)

	sub := &natsUpdateSubscription[T]{
		id:       id,
		key:      key,
		events:   make(chan UpdateEvent[T], updateSubscriberBufferSize),
		hub:      h,
		cancelFn: cancel,
	}

	// Register in subscriber map
	keySubs, _ := h.subscribers.LoadOrStore(key, &sync.Map{})
	keySubs.(*sync.Map).Store(id, sub)

	subject := h.subjectPrefix + "." + key
	natsSub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		var event UpdateEvent[T]
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to unmarshal event",
				"subject", subject,
				"error", err)
			return
		}

		select {
		case sub.events <- event:
		default:
			logging.Warn("[NATSUpdateHub:"+h.name+"] Dropped event (slow consumer)",
				"key", truncateKey(key),
				"subscriberID", id,
				"seq", event.SequenceNumber)
		}
	})
	if err != nil {
		logging.Error("[NATSUpdateHub:"+h.name+"] Failed to subscribe",
			"subject", subject,
			"error", err)
		// Return a dead subscription — events channel will never receive
		close(sub.events)
		cancel()
		return sub
	}
	sub.natsSub = natsSub

	logging.Debug("[NATSUpdateHub:"+h.name+"] New subscriber",
		"subject", subject,
		"subscriberID", id)

	// Clean up on context cancellation
	go func() {
		<-subCtx.Done()
		h.unsubscribe(sub)
	}()

	return sub
}

// Close drains the NATS connection (caller is responsible for connection lifecycle
// if the connection is shared across hubs).
func (h *NATSUpdateHub[T]) Close() error {
	// Don't close the shared connection — just unsubscribe all
	h.subscribers.Range(func(_, keySubs any) bool {
		keySubs.(*sync.Map).Range(func(_, value any) bool {
			sub := value.(*natsUpdateSubscription[T])
			h.unsubscribe(sub)
			return true
		})
		return true
	})
	return nil
}

func (h *NATSUpdateHub[T]) unsubscribe(sub *natsUpdateSubscription[T]) {
	sub.once.Do(func() {
		// Cancel context
		sub.cancelFn()

		// Unsubscribe from NATS
		if sub.natsSub != nil {
			_ = sub.natsSub.Unsubscribe()
		}

		// Remove from subscriber map
		if keySubs, ok := h.subscribers.Load(sub.key); ok {
			keySubs.(*sync.Map).Delete(sub.id)

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

		logging.Debug("[NATSUpdateHub:"+h.name+"] Subscriber unsubscribed",
			"key", truncateKey(sub.key),
			"subscriberID", sub.id)
	})
}
