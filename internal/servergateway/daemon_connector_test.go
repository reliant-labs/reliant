// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestConnector() *DaemonConnector {
	return &DaemonConnector{
		activeConns: make(map[string]context.CancelFunc),
	}
}

func TestStartConnection_AddsToActiveConns(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dc.startConnection(ctx, "d-1", "u-1", "10.0.0.1", 9190)

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.True(t, exists, "expected activeConns to contain daemon d-1")

	// Clean up.
	dc.stopConnection("d-1")
}

func TestStopConnection_CancelsActiveConnection(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()

	connCtx, connCancel := context.WithCancel(context.Background())
	dc.activeConns["d-1"] = connCancel

	dc.stopConnection("d-1")

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.False(t, exists, "d-1 should be removed from activeConns after disconnect")
	assert.Error(t, connCtx.Err(), "connection context should be cancelled")
}

func TestStopConnection_UnknownDaemon_NoOp(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()

	// Should not panic.
	dc.stopConnection("nonexistent")

	dc.mu.Lock()
	assert.Len(t, dc.activeConns, 0)
	dc.mu.Unlock()
}

func TestStartConnection_ReplacesExisting(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()
	ctx := context.Background()

	firstCtx, firstCancel := context.WithCancel(ctx)
	dc.activeConns["d-1"] = firstCancel

	dc.startConnection(ctx, "d-1", "u-1", "10.0.0.2", 9190)

	assert.Error(t, firstCtx.Err(), "old connection context should be cancelled")

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.True(t, exists, "new connection should exist in activeConns")

	dc.stopConnection("d-1")
}

func TestCloseAll_CancelsAllConnections(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	dc.activeConns["d-1"] = cancel1
	dc.activeConns["d-2"] = cancel2

	dc.CloseAll()

	assert.Error(t, ctx1.Err(), "d-1 context should be cancelled")
	assert.Error(t, ctx2.Err(), "d-2 context should be cancelled")

	dc.mu.Lock()
	assert.Len(t, dc.activeConns, 0, "activeConns should be empty after CloseAll")
	dc.mu.Unlock()
}
