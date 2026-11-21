// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"

	"github.com/stretchr/testify/assert"
)

func newTestConnector() *DaemonConnector {
	return &DaemonConnector{
		activeConns: make(map[string]context.CancelFunc),
	}
}

// connectWithRetry waits 1s, 2s, 4s, 8s, then 15s for every later reconnect.
func TestReconnectBackoff_Ladder(t *testing.T) {
	t.Parallel()
	s := reconnectBackoff.Schedule()
	var got []time.Duration
	for range 7 {
		got = append(got, s.Next())
	}
	assert.Equal(t, []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		15 * time.Second, 15 * time.Second, 15 * time.Second,
	}, got)
}

func TestStartConnection_AddsToActiveConns(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dc.startConnection(ctx, "d-1", "u-1", "10.0.0.1", 9190, tools_daemon.MachineOrg{})

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

	dc.startConnection(ctx, "d-1", "u-1", "10.0.0.2", 9190, tools_daemon.MachineOrg{})

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

// A snapshot org supersedes a connection's only when it resolves or differs;
// an unresolved tick never does.
func TestOrgChanged(t *testing.T) {
	t.Parallel()
	b := tools_daemon.MachineOrg{ID: "org-b"}
	for name, tc := range map[string]struct {
		active, snapshot tools_daemon.MachineOrg
		want             bool
	}{
		"unresolved, then resolved":        {tools_daemon.MachineOrg{Unresolved: true}, b, true},
		"none (older control plane), then": {tools_daemon.MachineOrg{}, b, true},
		"changed":                          {tools_daemon.MachineOrg{ID: "org-c"}, b, true},
		"unchanged":                        {b, b, false},
		"an unresolved tick":               {b, tools_daemon.MachineOrg{Unresolved: true}, false},
		"still none":                       {tools_daemon.MachineOrg{}, tools_daemon.MachineOrg{}, false},
	} {
		assert.Equal(t, tc.want, orgChanged(tc.active, tc.snapshot), name)
	}
}

// Review B1 (round 3): an ACTIVE connection whose machine org resolves, or
// changes, in a later snapshot is reconnected with the new org, so the
// machine registers with the authoritative org; an unchanged snapshot, or an
// unresolved tick, leaves it alone.
func TestReconcile_ReconnectsAnActiveMachineWhenItsOrgResolvesOrChanges(t *testing.T) {
	t.Parallel()
	dc := newTestConnector()
	r := &ManagedDaemonReconciler{connector: dc}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(dc.CloseAll)

	// canary installs an active connection for d-1 registered with org,
	// whose context reports whether the reconcile replaced it.
	canary := func(org tools_daemon.MachineOrg) context.Context {
		connCtx, connCancel := context.WithCancel(ctx)
		dc.mu.Lock()
		dc.activeConns["d-1"] = connCancel
		if dc.activeOrgs == nil {
			dc.activeOrgs = map[string]tools_daemon.MachineOrg{}
		}
		dc.activeOrgs["d-1"] = org
		dc.mu.Unlock()
		return connCtx
	}
	snapshot := func(org tools_daemon.MachineOrg) managedDaemonStateSnapshot {
		return managedDaemonStateSnapshot{Daemons: []ManagedDaemonState{{
			DaemonID: "d-1", UserID: "u-1", PodIP: "10.255.255.1", PodPort: 9190, OrgID: org.ID, OrgUnresolved: org.Unresolved,
		}}}
	}
	orgOf := func() tools_daemon.MachineOrg {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		return dc.activeOrgs["d-1"]
	}
	b := tools_daemon.MachineOrg{ID: "org-b"}
	c := tools_daemon.MachineOrg{ID: "org-c"}

	// Unresolved → resolved: reconnected with org B.
	first := canary(tools_daemon.MachineOrg{Unresolved: true})
	r.reconcile(ctx, snapshot(b))
	assert.Error(t, first.Err(), "the connection registered with an unresolved org was kept")
	assert.Equal(t, b, orgOf())

	// Unchanged, and an unresolved tick: left alone.
	steady := canary(b)
	r.reconcile(ctx, snapshot(b))
	r.reconcile(ctx, snapshot(tools_daemon.MachineOrg{Unresolved: true}))
	assert.NoError(t, steady.Err(), "an unchanged or unresolved snapshot reconnected the machine")
	assert.Equal(t, b, orgOf())

	// Changed org: reconnected with org C.
	r.reconcile(ctx, snapshot(c))
	assert.Error(t, steady.Err())
	assert.Equal(t, c, orgOf())
}
