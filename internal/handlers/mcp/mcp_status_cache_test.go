// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

func at(daemonID, path string) projectcheckout.Target {
	return projectcheckout.Target{DaemonID: daemonID, Path: path}
}

func TestMCPStatusCache_EvictsFreshEntriesAtCapacity(t *testing.T) {
	cache := newMCPStatusCache(func(_ context.Context, _ string, checkout projectcheckout.Target) (*daemonMCPServerStatus, error) {
		return &daemonMCPServerStatus{Servers: []daemonMCPServerStatusEntry{{Name: checkout.Path}}}, nil
	})
	cache.maxEntries = 2
	for _, projectPath := range []string{"/one", "/two", "/three"} {
		require.NotNil(t, cache.get(context.Background(), "user", at("d", projectPath)))
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.entries, 2)
	_, retainedOldest := cache.entries[mcpStatusKey("user", at("d", "/one"))]
	require.False(t, retainedOldest, "fresh entries must still obey the hard bound")
	_, retainedTwo := cache.entries[mcpStatusKey("user", at("d", "/two"))]
	_, retainedThree := cache.entries[mcpStatusKey("user", at("d", "/three"))]
	require.True(t, retainedTwo)
	require.True(t, retainedThree)
}

// The same path on two machines is two sets of servers: one machine's status
// is never served for the other.
func TestMCPStatusCache_KeysByMachine(t *testing.T) {
	cache := newMCPStatusCache(func(_ context.Context, _ string, checkout projectcheckout.Target) (*daemonMCPServerStatus, error) {
		return &daemonMCPServerStatus{Servers: []daemonMCPServerStatusEntry{{Name: checkout.DaemonID}}}, nil
	})
	a := cache.get(context.Background(), "user", at("laptop", "/p"))
	b := cache.get(context.Background(), "user", at("cloud", "/p"))
	require.Equal(t, "laptop", a.Servers[0].Name)
	require.Equal(t, "cloud", b.Servers[0].Name)
}
