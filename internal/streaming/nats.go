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
	subscribeCount atomic.Uint64
}

// NATSSubscription represents a single subscription to chat events via NATS.
type NATSSubscription struct {
	id     uint64
	chatID string
	events chan StreamingDelta
	cancel context.CancelFunc // stops the consumer goroutine
	hub    *NATSHub
	once   sync.Once // guards channel close
}

// Events returns the channel to receive streaming deltas.
func (s *NATSSubscription) Events() <-chan StreamingDelta {
	return s.events
}

// Unsubscribe removes this subscriber from the hub and stops the consumer.
func (s *NATSSubscription) Unsubscribe() {
	s.hub.unsubscribe(s)
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
func (h *NATSHub) Publish(chatID string, delta StreamingDelta) {
	h.publishCount.Add(1)

	data, err := json.Marshal(delta)
	if err != nil {
		h.dropCount.Add(1)
		logging.Warn("[NATSHub] Failed to marshal delta",
			"chatID", chatID[:min(8, len(chatID))],
			"error", err)
		return
	}

	subject := natsSubjectPfx + chatID
	// PublishAsync is non-blocking; we intentionally ignore the ack future.
	if _, err := h.js.PublishAsync(subject, data); err != nil {
		h.dropCount.Add(1)
		logging.Warn("[NATSHub] Failed to publish delta",
			"chatID", chatID[:min(8, len(chatID))],
			"error", err)
	}
}

// PublishEvent is a convenience method that extracts chatID from a ChatEvent.
func (h *NATSHub) PublishEvent(event ChatEvent) {
	h.Publish(event.ChatID, event.Delta)
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

// consumeLoop reads messages from a NATS ordered consumer and forwards them to the subscription channel.
func (h *NATSHub) consumeLoop(ctx context.Context, sub *NATSSubscription) {
	defer h.unsubscribe(sub)

	consumer, err := h.js.OrderedConsumer(ctx, natsStreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{natsSubjectPfx + sub.chatID},
	})
	if err != nil {
		logging.Warn("[NATSHub] Failed to create ordered consumer",
			"chatID", sub.chatID[:min(8, len(sub.chatID))],
			"error", err)
		return
	}

	iter, err := consumer.Messages()
	if err != nil {
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

	for {
		msg, err := iter.Next()
		if err != nil {
			// Iterator stopped (context cancelled or connection closed) — exit cleanly
			return
		}

		var delta StreamingDelta
		if err := json.Unmarshal(msg.Data(), &delta); err != nil {
			logging.Warn("[NATSHub] Failed to unmarshal delta",
				"chatID", sub.chatID[:min(8, len(sub.chatID))],
				"error", err)
			continue
		}

		select {
		case sub.events <- delta:
			// delivered
		default:
			h.dropCount.Add(1)
			logging.Warn("[NATSHub] Dropped event (slow consumer)",
				"chatID", sub.chatID[:min(8, len(sub.chatID))],
				"subscriberID", sub.id,
				"deltaType", delta.DeltaType)
		}
	}
}

// unsubscribe removes a subscriber from the hub, stops its consumer, and closes the channel.
func (h *NATSHub) unsubscribe(sub *NATSSubscription) {
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

		close(sub.events)

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
