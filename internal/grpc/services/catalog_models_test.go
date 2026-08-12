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
	// provision it here to represent a real authenticated user.
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), "test-user", "reliant", "rlnt_test_managed_key"))

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
