// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareAndSwapCodexAuthTokens(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "codex-cas-test-user"

	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, CodexAuthTokens{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		IDToken:      "id-1",
		AccountID:    "account-1",
	}))

	t.Run("matching refresh token swaps", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapCodexAuthTokens(ctx, userID, "refresh-1", CodexAuthTokens{
			AccessToken:  "access-2",
			RefreshToken: "refresh-2",
			IDToken:      "id-2",
			AccountID:    "account-1",
		})
		require.NoError(t, err)
		assert.True(t, swapped)

		stored, err := repo.GetCodexAuthTokens(ctx, userID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		assert.Equal(t, "access-2", stored.AccessToken)
		assert.Equal(t, "refresh-2", stored.RefreshToken)
		assert.Equal(t, "id-2", stored.IDToken)
	})

	t.Run("stale refresh token does not swap", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapCodexAuthTokens(ctx, userID, "refresh-1", CodexAuthTokens{
			AccessToken:  "access-3",
			RefreshToken: "refresh-3",
			IDToken:      "id-3",
			AccountID:    "account-1",
		})
		require.NoError(t, err)
		assert.False(t, swapped, "refresh-1 was already consumed; the row now holds refresh-2")

		stored, err := repo.GetCodexAuthTokens(ctx, userID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		assert.Equal(t, "access-2", stored.AccessToken, "stale rotation must not clobber the live lineage")
		assert.Equal(t, "refresh-2", stored.RefreshToken)
		assert.Equal(t, "id-2", stored.IDToken)
	})

	t.Run("missing row does not swap", func(t *testing.T) {
		swapped, err := repo.CompareAndSwapCodexAuthTokens(ctx, "no-such-user", "refresh-x", CodexAuthTokens{
			AccessToken:  "access-x",
			RefreshToken: "refresh-y",
			IDToken:      "id-x",
			AccountID:    "account-x",
		})
		require.NoError(t, err)
		assert.False(t, swapped)
	})
}
