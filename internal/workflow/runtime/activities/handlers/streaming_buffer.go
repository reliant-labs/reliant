// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"sync"
	"time"
)

// StreamBuffer buffers content deltas and flushes periodically to reduce database writes.
// This is used during LLM streaming to batch multiple small deltas into fewer database operations.
//
// Flush triggers:
// - Newline character in delta (ensures complete lines are visible)
// - Flush interval elapsed (default 200ms)
// - Manual Flush() call (at end of stream)
type StreamBuffer struct {
	mu            sync.Mutex
	chatID        string
	blockID       string
	buffer        strings.Builder
	lastFlush     time.Time
	flushInterval time.Duration
	onFlush       func(ctx context.Context, content string) error
}

// NewStreamBuffer creates a new streaming buffer.
//
// Parameters:
//   - chatID: Chat ID for logging/debugging
//   - blockID: Content block ID to append to
//   - flushInterval: Time between automatic flushes (e.g., 200ms)
//   - onFlush: Callback to write buffered content to database
func NewStreamBuffer(
	chatID, blockID string,
	flushInterval time.Duration,
	onFlush func(context.Context, string) error,
) *StreamBuffer {
	return &StreamBuffer{
		chatID:        chatID,
		blockID:       blockID,
		flushInterval: flushInterval,
		onFlush:       onFlush,
		lastFlush:     time.Now(),
	}
}

// Append adds content to the buffer and flushes if needed.
// Flush triggers:
// - Newline in delta (ensures complete lines are visible)
// - Flush interval elapsed
func (sb *StreamBuffer) Append(ctx context.Context, delta string) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sb.buffer.WriteString(delta)

	// Flush on newline OR flush interval elapsed
	shouldFlush := strings.Contains(delta, "\n") ||
		time.Since(sb.lastFlush) >= sb.flushInterval

	if shouldFlush {
		return sb.flush(ctx)
	}

	return nil
}

// Flush writes any buffered content to the database.
// This should be called at the end of streaming to ensure all content is persisted.
func (sb *StreamBuffer) Flush(ctx context.Context) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.flush(ctx)
}

// flush is the internal implementation (assumes lock is held).
func (sb *StreamBuffer) flush(ctx context.Context) error {
	if sb.buffer.Len() == 0 {
		return nil
	}

	content := sb.buffer.String()
	if err := sb.onFlush(ctx, content); err != nil {
		return err
	}

	sb.buffer.Reset()
	sb.lastFlush = time.Now()
	return nil
}
