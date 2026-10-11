// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPStatusCache_EvictsFreshEntriesAtCapacity(t *testing.T) {
	cache := newMCPStatusCache(func(_ context.Context, _, projectPath string) (*daemonMCPServerStatus, error) {
		return &daemonMCPServerStatus{Servers: []daemonMCPServerStatusEntry{{Name: projectPath}}}, nil
	})
	cache.maxEntries = 2
	for _, projectPath := range []string{"/one", "/two", "/three"} {
		require.NotNil(t, cache.get(context.Background(), "user", projectPath))
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.entries, 2)
	_, retainedOldest := cache.entries[mcpStatusKey("user", "/one")]
	require.False(t, retainedOldest, "fresh entries must still obey the hard bound")
	_, retainedTwo := cache.entries[mcpStatusKey("user", "/two")]
	_, retainedThree := cache.entries[mcpStatusKey("user", "/three")]
	require.True(t, retainedTwo)
	require.True(t, retainedThree)
}
