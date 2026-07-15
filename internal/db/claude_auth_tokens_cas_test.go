// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareAndSwapClaudeAuthTokens(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "cas-test-user"
	expiry := time.Now().Add(8 * time.Hour).UTC().Truncate(time.Second)

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, userID, ClaudeAuthTokens{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    expiry,
	}))

	t.Run("matching refresh token swaps", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapClaudeAuthTokens(ctx, userID, "refresh-1", ClaudeAuthTokens{
			AccessToken:  "access-2",
			RefreshToken: "refresh-2",
			ExpiresAt:    expiry.Add(time.Hour),
		})
		require.NoError(t, err)
		assert.True(t, swapped)

		stored, err := repo.GetClaudeAuthTokens(ctx, userID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		assert.Equal(t, "access-2", stored.AccessToken)
		assert.Equal(t, "refresh-2", stored.RefreshToken)
	})

	t.Run("stale refresh token does not swap", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapClaudeAuthTokens(ctx, userID, "refresh-1", ClaudeAuthTokens{
			AccessToken:  "access-3",
			RefreshToken: "refresh-3",
			ExpiresAt:    expiry,
		})
		require.NoError(t, err)
		assert.False(t, swapped, "refresh-1 was already consumed; the row now holds refresh-2")

		stored, err := repo.GetClaudeAuthTokens(ctx, userID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		assert.Equal(t, "access-2", stored.AccessToken, "stale rotation must not clobber the live lineage")
		assert.Equal(t, "refresh-2", stored.RefreshToken)
	})

	t.Run("missing row does not swap", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapClaudeAuthTokens(ctx, "no-such-user", "refresh-x", ClaudeAuthTokens{
			AccessToken:  "access-x",
			RefreshToken: "refresh-y",
			ExpiresAt:    expiry,
		})
		require.NoError(t, err)
		assert.False(t, swapped)
	})
}
