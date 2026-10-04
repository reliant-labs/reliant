// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// The automation credential is stored in api_keys under a reserved provider.
// It must be readable by its exact name (the router's resolver) and invisible
// to everything that enumerates providers — settings and LLM driver selection
// iterate GetProviderAPIKeys.
func TestAutomationKeyIsHiddenFromProviderListing(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	const user = "user-hidden"
	automation := core.AutomationProviderPrefix + "daemon-1"
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-real"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, automation, "rlat_secret"))

	got, err := repo.GetProviderAPIKey(ctx, user, automation)
	require.NoError(t, err)
	require.Equal(t, "rlat_secret", got, "the resolver reads it by exact name")

	all, err := repo.GetProviderAPIKeys(ctx, user)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"anthropic": "sk-real"}, all,
		"the automation credential must never appear as a provider key")
}
