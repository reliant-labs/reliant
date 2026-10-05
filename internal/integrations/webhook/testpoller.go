// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"strconv"
	"sync"

	"github.com/reliant-labs/reliant/internal/triggers"
)

// TestPollerID is the reference poller's integration id.
const TestPollerID = "test_feed"

// TestPoller is the reference poller: an in-memory, append-only feed whose
// cursor is the index of the next unseen item. It shows the contract a real
// poller implements — baseline on an empty cursor, items after it otherwise —
// and backs the poll path's end-to-end tests. Never registered in production.
type TestPoller struct {
	mu    sync.Mutex
	items []triggers.PollItem
}

// NewTestPoller returns an empty feed.
func NewTestPoller() *TestPoller { return &TestPoller{} }

// Append adds an item to the feed.
func (p *TestPoller) Append(item triggers.PollItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, item)
}

// Poll implements triggers.Poller.
func (p *TestPoller) Poll(_ context.Context, req triggers.PollRequest) (*triggers.PollResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	end := len(p.items)
	if req.Cursor == "" {
		return &triggers.PollResult{Cursor: strconv.Itoa(end)}, nil
	}
	start, err := strconv.Atoi(req.Cursor)
	if err != nil || start < 0 || start > end {
		// An unusable cursor re-baselines rather than replaying the feed.
		return &triggers.PollResult{Cursor: strconv.Itoa(end)}, nil
	}
	items := append([]triggers.PollItem(nil), p.items[start:end]...)
	return &triggers.PollResult{Cursor: strconv.Itoa(end), Items: items}, nil
}
