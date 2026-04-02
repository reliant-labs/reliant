package streaming

import (
	"context"
	"fmt"
	"strings"
)

// StreamingDriver identifies which streaming backend to use.
type StreamingDriver string

const (
	DriverMemory StreamingDriver = "memory"
	DriverNATS   StreamingDriver = "nats"
)

// ParseStreamingDriver parses a raw string into a StreamingDriver.
// Empty string defaults to "memory".
func ParseStreamingDriver(raw string) (StreamingDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DriverMemory):
		return DriverMemory, nil
	case string(DriverNATS):
		return DriverNATS, nil
	default:
		return "", fmt.Errorf("invalid STREAMING_DRIVER %q (expected memory or nats)", raw)
	}
}

// StreamingConfig holds configuration for the streaming hub.
type StreamingConfig struct {
	Driver  StreamingDriver
	NATSUrl string // Required when Driver == DriverNATS
}

// StreamingHub is the interface for pub/sub streaming of ephemeral events.
// Implementations: MemoryHub (in-memory), NATSHub (JetStream).
type StreamingHub interface {
	// Publish broadcasts a streaming delta to all subscribers of a chat.
	// Non-blocking — slow consumers may miss events.
	Publish(ctx context.Context, chatID string, delta StreamingDelta)

	// PublishEvent is a convenience method that extracts chatID from a ChatEvent.
	PublishEvent(ctx context.Context, event ChatEvent)

	// Subscribe creates a new subscription for events from a specific chat.
	// The subscription is automatically cleaned up when ctx is cancelled.
	Subscribe(ctx context.Context, chatID string) Subscription

	// SubscriberCount returns the number of active subscribers for a chat.
	SubscriberCount(chatID string) int

	// TotalSubscriberCount returns the total number of active subscribers.
	TotalSubscriberCount() int

	// Stats returns current hub statistics.
	Stats() HubStats

	// IsConnected reports whether the hub's underlying transport is healthy.
	// Always returns true for in-memory hubs.
	IsConnected() bool

	// Close shuts down the hub and releases resources.
	Close() error
}

// Subscription represents a single subscription to chat streaming events.
type Subscription interface {
	// Events returns the channel to receive streaming deltas.
	Events() <-chan StreamingDelta

	// Unsubscribe removes this subscription and closes the events channel.
	Unsubscribe()
}

// NewStreamingHub creates a new streaming hub based on the config.
func NewStreamingHub(cfg StreamingConfig) (StreamingHub, error) {
	switch cfg.Driver {
	case DriverMemory, "":
		return NewMemoryHub(), nil
	case DriverNATS:
		return NewNATSHub(cfg.NATSUrl)
	default:
		return nil, fmt.Errorf("unknown streaming driver: %q", cfg.Driver)
	}
}
