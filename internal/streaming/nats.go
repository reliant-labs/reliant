// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package streaming

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
)

const (
	natsStreamName = "STREAMING_DELTAS"
	natsSubjectPfx = "streaming.chat."
)

// NATSHub implements StreamingHub using NATS JetStream.
// It uses a memory-backed stream with short retention for ephemeral streaming deltas.
type NATSHub struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	stream jetstream.Stream

	// Track subscribers for stats
	subscribers sync.Map // map[string]*sync.Map[uint64]*NATSSubscription
	nextID      atomic.Uint64

	// metrics
	publishCount   atomic.Uint64
	dropCount      atomic.Uint64
	coalesceCount  atomic.Uint64
	subscribeCount atomic.Uint64
}

// NATSSubscription represents a single subscription to chat events via NATS.
type NATSSubscription struct {
	id     uint64
	chatID string
	events chan StreamingDelta
	cancel context.CancelFunc // stops the consumer goroutine
	hub    *NATSHub
	once   sync.Once // guards deregister
}

// Events returns the channel to receive streaming deltas.
func (s *NATSSubscription) Events() <-chan StreamingDelta {
	return s.events
}

// Unsubscribe stops the subscription. It only signals cancellation; the
// consume loop (the sole sender) observes the cancel, unhooks itself, and
// closes the events channel. Keeping the close on the sender side means a
// blocking delivery can never race a close and panic.
func (s *NATSSubscription) Unsubscribe() {
	s.cancel()
}

// NewNATSHub creates a new NATS JetStream streaming hub.
func NewNATSHub(natsURL string) (*NATSHub, error) {
	nc, err := natsutil.Connect(natsURL)
	if err != nil {
		return nil, err
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     natsStreamName,
		Subjects: []string{natsSubjectPfx + "*"},
		Storage:  jetstream.MemoryStorage,
		MaxAge:   5 * time.Minute,
		MaxBytes: 128 * 1024 * 1024, // 128 MB cap
	})
	if err != nil {
		nc.Close()
		return nil, err
	}

	return &NATSHub{
		nc:     nc,
		js:     js,
		stream: stream,
	}, nil
}

// Publish broadcasts a streaming delta to all subscribers of a chat via NATS.
// Non-blocking — if publish fails, the event is logged and dropped.
func (h *NATSHub) Publish(ctx context.Context, chatID string, delta StreamingDelta) {
	h.publishCount.Add(1)

	data, err := json.Marshal(delta)
	if err != nil {
		h.dropCount.Add(1)
		observability.StreamingErrorsTotal.WithLabelValues("marshal").Inc()
		logging.Warn("[NATSHub] Failed to marshal delta",
			"chatID", chatID[:min(8, len(chatID))],
			"error", err)
		return
	}

	subject := natsSubjectPfx + chatID
	msg := observability.NATSPublishMsg(ctx, subject, data)
	// PublishMsgAsync is non-blocking; we intentionally ignore the ack future.
	if _, err := h.js.PublishMsgAsync(msg); err != nil {
		h.dropCount.Add(1)
		observability.NATSErrorsTotal.WithLabelValues("streaming.chat", "publish").Inc()
		logging.Warn("[NATSHub] Failed to publish delta",
			"chatID", chatID[:min(8, len(chatID))],
			"error", err)
		return
	}
	observability.NATSPublishTotal.WithLabelValues("streaming.chat").Inc()
}

// PublishEvent is a convenience method that extracts chatID from a ChatEvent.
func (h *NATSHub) PublishEvent(ctx context.Context, event ChatEvent) {
	h.Publish(ctx, event.ChatID, event.Delta)
}

// Subscribe creates a new subscription for events from a specific chat.
// The subscription is automatically cleaned up when ctx is cancelled.
func (h *NATSHub) Subscribe(ctx context.Context, chatID string) Subscription {
	id := h.nextID.Add(1)
	h.subscribeCount.Add(1)

	subCtx, subCancel := context.WithCancel(ctx)

	sub := &NATSSubscription{
		id:     id,
		chatID: chatID,
		events: make(chan StreamingDelta, subscriberBufferSize),
		cancel: subCancel,
		hub:    h,
	}

	// Register in subscriber map
	chatSubs, _ := h.subscribers.LoadOrStore(chatID, &sync.Map{})
	chatSubs.(*sync.Map).Store(id, sub)

	logging.Debug("[NATSHub] New subscriber",
		"chatID", chatID[:min(8, len(chatID))],
		"subscriberID", id)

	// Start consumer goroutine
	go h.consumeLoop(subCtx, sub)

	return sub
}

// deliver sends a delta to the subscriber, blocking until the consumer accepts
// it or the subscription is torn down. This is the backpressure path: a slow
// consumer slows the reader (JetStream buffers upstream) instead of losing
// events. Returns false if ctx was cancelled, signalling the caller to exit.
func (h *NATSHub) deliver(ctx context.Context, sub *NATSSubscription, delta StreamingDelta) bool {
	select {
	case sub.events <- delta:
		return true
	case <-ctx.Done():
		return false
	}
}

// consumeLoop reads messages from a NATS ordered consumer and forwards them to the subscription channel.
func (h *NATSHub) consumeLoop(ctx context.Context, sub *NATSSubscription) {
	// The consume loop is the sole sender on sub.events, so it owns the close:
	// deregister (idempotent) unhooks the subscriber and stops the consumer,
	// then — because the loop has returned and no send is in flight — we close
	// the channel exactly once. External Unsubscribe only signals via cancel;
	// it never closes, so a blocking deliver can't race a close and panic.
	defer func() {
		h.deregister(sub)
		close(sub.events)
	}()

	consumer, err := h.js.OrderedConsumer(ctx, natsStreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{natsSubjectPfx + sub.chatID},
	})
	if err != nil {
		observability.StreamingErrorsTotal.WithLabelValues("consumer_create").Inc()
		logging.Warn("[NATSHub] Failed to create ordered consumer",
			"chatID", sub.chatID[:min(8, len(sub.chatID))],
			"error", err)
		return
	}

	iter, err := consumer.Messages()
	if err != nil {
		observability.StreamingErrorsTotal.WithLabelValues("iterator_start").Inc()
		logging.Warn("[NATSHub] Failed to start message iterator",
			"chatID", sub.chatID[:min(8, len(sub.chatID))],
			"error", err)
		return
	}
	defer iter.Stop()

	// Watch for context cancellation to unblock iter.Next()
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	// pending holds a coalesced run of content-text deltas that could not be
	// delivered immediately because the consumer's channel was full. We only
	// ever carry when the channel is full — i.e. the consumer is behind, not
	// idle — so a merged chunk is always flushed by the next delta or by the
	// structural delta (content_block_stop / message_stop) that ends every
	// message. Structural deltas are never carried or dropped: losing one
	// corrupts the rendered message (orphaned tool calls, stuck placeholders).
	var pending *StreamingDelta

	for {
		// Opportunistically flush a carried chunk the moment the consumer has
		// room, without blocking the reader.
		if pending != nil {
			select {
			case sub.events <- *pending:
				pending = nil
			default:
			}
		}

		msg, err := iter.Next()
		if err != nil {
			// Iterator stopped (context cancelled or connection closed) — exit cleanly
			return
		}

		// Extract trace context from NATS message headers
		carrierMsg := &nats.Msg{Header: msg.Headers()}
		_, span := observability.StartNATSSpan(ctx, carrierMsg, "nats.consume.streaming.delta")

		observability.NATSReceiveTotal.WithLabelValues("streaming.chat").Inc()

		var delta StreamingDelta
		if err := json.Unmarshal(msg.Data(), &delta); err != nil {
			observability.StreamingErrorsTotal.WithLabelValues("unmarshal").Inc()
			logging.Warn("[NATSHub] Failed to unmarshal delta",
				"chatID", sub.chatID[:min(8, len(sub.chatID))],
				"error", err)
			span.End()
			continue
		}

		// Fast path: consumer keeping up — hand the delta off without blocking.
		// Only taken when nothing is carried, so ordering is preserved.
		if pending == nil {
			select {
			case sub.events <- delta:
				span.End()
				continue
			default:
			}
		}

		if delta.coalescible() {
			// Merge into the carried chunk when they target the same block; the
			// consumer catches up with one larger send instead of a backlog.
			if pending != nil && pending.canCoalesceWith(delta) {
				merged := pending.coalesce(delta)
				pending = &merged
				h.coalesceCount.Add(1)
				observability.StreamingErrorsTotal.WithLabelValues("coalesced").Inc()
				span.End()
				continue
			}
			// A carried chunk for a different block/thread must land before this
			// one — flush it (blocking) to preserve order, then carry the new one.
			if pending != nil {
				if !h.deliver(ctx, sub, *pending) {
					span.End()
					return
				}
				pending = nil
			}
			d := delta
			pending = &d
			span.End()
			continue
		}

		// Structural delta: flush any carried text first (order), then deliver
		// this one. Both block so nothing is lost — the reader slows and
		// JetStream buffers upstream instead of dropping events.
		if pending != nil {
			if !h.deliver(ctx, sub, *pending) {
				span.End()
				return
			}
			pending = nil
		}
		if !h.deliver(ctx, sub, delta) {
			span.End()
			return
		}
		span.End()
	}
}

// deregister unhooks a subscriber from the hub and stops its consumer. It does
// NOT close sub.events — the consume loop owns that, closing after it returns
// so no send can be in flight. Idempotent: safe to call from both the loop's
// teardown and (defensively) elsewhere.
func (h *NATSHub) deregister(sub *NATSSubscription) {
	sub.once.Do(func() {
		// Cancel the consumer goroutine context
		sub.cancel()

		// Remove from subscriber map
		if chatSubs, ok := h.subscribers.Load(sub.chatID); ok {
			chatSubs.(*sync.Map).Delete(sub.id)

			// Clean up empty chat maps
			isEmpty := true
			chatSubs.(*sync.Map).Range(func(_, _ interface{}) bool {
				isEmpty = false
				return false
			})
			if isEmpty {
				h.subscribers.Delete(sub.chatID)
			}
		}

		logging.Debug("[NATSHub] Subscriber unsubscribed",
			"chatID", sub.chatID[:min(8, len(sub.chatID))],
			"subscriberID", sub.id)
	})
}

// SubscriberCount returns the number of active subscribers for a chat.
func (h *NATSHub) SubscriberCount(chatID string) int {
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
func (h *NATSHub) TotalSubscriberCount() int {
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

// Stats returns current hub statistics.
func (h *NATSHub) Stats() HubStats {
	activeChats := 0
	h.subscribers.Range(func(_, _ interface{}) bool {
		activeChats++
		return true
	})

	return HubStats{
		TotalPublished:   h.publishCount.Load(),
		TotalDropped:     h.dropCount.Load(),
		TotalCoalesced:   h.coalesceCount.Load(),
		TotalSubscribers: h.subscribeCount.Load(),
		ActiveChats:      activeChats,
	}
}

// IsConnected reports whether the underlying NATS connection is connected.
func (h *NATSHub) IsConnected() bool { return h.nc.IsConnected() }

// Close shuts down the hub, draining the NATS connection gracefully.
func (h *NATSHub) Close() error {
	return h.nc.Drain()
}
