package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	llmdrivers "github.com/reliant-labs/reliant/internal/llm/drivers"
	reliantdriver "github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCatalogServiceTestContext() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

func supportedReliantModelIDs() []string {
	ids := make([]string, 0, len(reliantdriver.SupportedModels))
	for _, modelID := range reliantdriver.SupportedModels {
		ids = append(ids, string(modelID))
	}
	return ids
}

func TestCatalogService_ListModels_ReliantOnlyExposesCuratedAllowlist(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	llmdrivers.InitializeAPIKeyProvider(repo)
	ctx := newCatalogServiceTestContext()
	// A logged-in user always has their managed Reliant key synced locally
	// (JWT -> admin server -> LiteLLM key, minted and persisted by
	// SyncReliantProvider). ListModels gates reliant on that stored key, so
	// provision it here to represent a real authenticated user. It must have
	// the rlat_ access-token shape: a legacy rlnt_ key is not a usable gateway
	// credential and is skipped by BuildAvailableDrivers.
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), "test-user", "reliant", "rlat_abcdef0123456789abcdef0123456789"))

	svc := NewCatalogService(nil)
	resp, err := svc.ListModels(ctx, connect.NewRequest(&reliantv1.ListModelsRequest{}))
	require.NoError(t, err)

	reliantIDs := make([]string, 0)
	for _, model := range resp.Msg.Models {
		if model.DriverId != "reliant" {
			continue
		}
		reliantIDs = append(reliantIDs, extractBaseModelID(model.Id))
		assert.Equal(t, "Reliant", model.Provider)
	}

	assert.ElementsMatch(t, supportedReliantModelIDs(), reliantIDs)
	assert.NotContains(t, reliantIDs, "vertex-claude-4.5-sonnet")
	assert.NotContains(t, reliantIDs, "vertex-gemini-2.5-pro")
}

func TestCatalogService_ListModelsByProvider_ReliantOnlyExposesCuratedAllowlist(t *testing.T) {
	svc := NewCatalogService(nil)
	resp, err := svc.ListModelsByProvider(context.Background(), connect.NewRequest(&reliantv1.ListModelsByProviderRequest{Provider: "reliant"}))
	require.NoError(t, err)

	ids := make([]string, 0, len(resp.Msg.Models))
	for _, model := range resp.Msg.Models {
		ids = append(ids, model.Id)
		assert.Equal(t, "reliant", model.DriverId)
	}

	assert.ElementsMatch(t, supportedReliantModelIDs(), ids)
	assert.NotContains(t, ids, "vertex-claude-4.5-sonnet")
	assert.NotContains(t, ids, "vertex-gemini-2.5-pro")
}

// A key for a provider whose driver is not registered (xai) must not make its
// models selectable; registered providers are unaffected, and temperature
// support is reported per model@driver.
func TestCatalogService_ListModels_HidesUnregisteredDriversAndReportsTemperature(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	llmdrivers.InitializeAPIKeyProvider(repo)
	ctx := newCatalogServiceTestContext()
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), "test-user", "xai", "xai-test-key"))
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), "test-user", "openrouter", "sk-or-test"))

	resp, err := NewCatalogService(nil).ListModels(ctx, connect.NewRequest(&reliantv1.ListModelsRequest{}))
	require.NoError(t, err)

	sawOpenRouter := false
	for _, m := range resp.Msg.Models {
		assert.NotEqual(t, "xai", m.DriverId, "xai has no registered driver; %s must not be listed", m.Id)
		assert.NotEqual(t, "grok-4", extractBaseModelID(m.Id))
		if m.DriverId == "openrouter" {
			sawOpenRouter = true
		}
		base := extractBaseModelID(m.Id)
		if m.DriverId == "openrouter" && base == "claude-5-opus" {
			assert.False(t, m.SupportsTemperature, "adaptive Claude is temperature-omit")
		}
		if m.DriverId == "openrouter" && base == "gemini-2.5-pro" {
			assert.True(t, m.SupportsTemperature)
		}
	}
	assert.True(t, sawOpenRouter)
}
