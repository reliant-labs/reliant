package streaming

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
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
func (h *NATSUpdateHub[T]) Publish(ctx context.Context, event UpdateEvent[T]) {
	data, err := json.Marshal(event)
	if err != nil {
		observability.StreamingErrorsTotal.WithLabelValues("marshal").Inc()
		logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to marshal event",
			"key", truncateKey(event.Key),
			"error", err)
		return
	}

	subject := h.subjectPrefix + "." + event.Key
	msg := observability.NATSPublishMsg(ctx, subject, data)
	if err := h.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("updates", "publish").Inc()
		logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to publish",
			"subject", subject,
			"error", err)
		return
	}
	observability.NATSPublishTotal.WithLabelValues("updates").Inc()
}

// Subscribe creates a new subscription for events matching the given key.
//
// Delivery is SYNCHRONOUS (SubscribeSync + a consume goroutine), not an async
// nats.Handler callback, so that exactly one goroutine ever sends on
// sub.events and can therefore own its close. See consumeLoop.
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
	natsSub, err := h.nc.SubscribeSync(subject)
	if err != nil {
		observability.StreamingErrorsTotal.WithLabelValues("subscribe").Inc()
		logging.Error("[NATSUpdateHub:"+h.name+"] Failed to subscribe",
			"subject", subject,
			"error", err)
		// Return a dead subscription — events channel will never receive.
		// No consume loop was started, so nothing can send on sub.events and
		// closing it here is the one safe exception to the ownership rule.
		h.deregister(sub)
		cancel()
		close(sub.events)
		return sub
	}
	sub.natsSub = natsSub

	logging.Debug("[NATSUpdateHub:"+h.name+"] New subscriber",
		"subject", subject,
		"subscriberID", id)

	go h.consumeLoop(subCtx, sub)

	return sub
}

// consumeLoop drains the subscription's NATS messages onto sub.events until
// the subscription context is cancelled.
//
// It is the SOLE sender on sub.events, so it owns the close: deregister
// (idempotent) unhooks the subscriber and tears down the NATS subscription,
// then — because the loop has returned and no send is in flight — we close
// the channel exactly once. unsubscribe() only signals via cancel; it never
// closes, so a NATS delivery can't race a close and panic with "send on
// closed channel". This is the same ownership rule NATSHub.consumeLoop
// (nats.go) follows; the async-callback form this replaced had no owning
// goroutine and did panic, because nats.Subscription.Unsubscribe does not
// wait for a callback already dispatched into waitForMsgs.
func (h *NATSUpdateHub[T]) consumeLoop(ctx context.Context, sub *natsUpdateSubscription[T]) {
	defer func() {
		h.deregister(sub)
		close(sub.events)
	}()

	subject := h.subjectPrefix + "." + sub.key

	for {
		msg, err := sub.natsSub.NextMsgWithContext(ctx)
		if err != nil {
			// Context cancelled, subscription unsubscribed, or connection
			// closed — all normal teardown. Exit and run the deferred close.
			return
		}

		func() {
			_, span := observability.StartNATSSpan(context.Background(), msg, "nats.consume.update")
			defer span.End()

			observability.NATSReceiveTotal.WithLabelValues("updates").Inc()

			var event UpdateEvent[T]
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				logging.Warn("[NATSUpdateHub:"+h.name+"] Failed to unmarshal event",
					"subject", subject,
					"error", err)
				return
			}

			// Non-blocking by design: these events are already durable in the
			// database, so a slow consumer misses a notification and catches
			// up from the DB rather than stalling the reader.
			select {
			case sub.events <- event:
			default:
				observability.StreamingErrorsTotal.WithLabelValues("slow_consumer").Inc()
				logging.Warn("[NATSUpdateHub:"+h.name+"] Dropped event (slow consumer)",
					"key", truncateKey(sub.key),
					"subscriberID", sub.id,
					"seq", event.SequenceNumber)
			}
		}()
	}
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

// unsubscribe tears the subscription down by CANCELLING it — it deliberately
// does not touch sub.events. Cancelling unblocks the consume loop's
// NextMsgWithContext, so the loop returns and performs the deregister + close
// itself (see consumeLoop). Closing here instead would race an in-flight send
// from the consume loop and panic.
//
// Idempotent and safe to call from any goroutine, including after the
// subscription has already torn itself down.
func (h *NATSUpdateHub[T]) unsubscribe(sub *natsUpdateSubscription[T]) {
	sub.cancelFn()
}

// deregister unhooks sub from NATS and from the hub's subscriber index. Called
// only from the consume loop's teardown (and from Subscribe's error path,
// where no loop exists). Idempotent via sub.once.
func (h *NATSUpdateHub[T]) deregister(sub *natsUpdateSubscription[T]) {
	sub.once.Do(func() {
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

		logging.Debug("[NATSUpdateHub:"+h.name+"] Subscriber unsubscribed",
			"key", truncateKey(sub.key),
			"subscriberID", sub.id)
	})
}
