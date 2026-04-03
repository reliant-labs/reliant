package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	driverspkg "github.com/reliant-labs/reliant/internal/llm/drivers"
)

func TestCatalogService_ListModels_SynthesizesReliantModelsFromAllowlist(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	t.Setenv(features.ReliantManagedAccessEnabledEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/controlplane.v1.LLMAccessService/GetCurrentLLMAccess", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"sk-reliant-runtime","allowedModels":["claude-4.5-sonnet"]}`))
	}))
	defer server.Close()

	driverspkg.InitializeAPIKeyProvider(
		repo,
		driverspkg.WithControlPlaneClient(controlplane.NewClient(controlplane.Config{BaseURL: server.URL})),
		driverspkg.WithReliantRuntimeBaseURL("https://runtime.reliant.test/v1"),
	)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "cpat_test_token"))

	svc := NewCatalogService(nil)
	resp, err := svc.ListModels(ctx, connect.NewRequest(&reliantv1.ListModelsRequest{}))
	require.NoError(t, err)
	require.NotNil(t, resp)

	var reliantModel *reliantv1.ModelInfo
	for _, model := range resp.Msg.Models {
		if model.Id == "claude-4.5-sonnet@reliant" {
			reliantModel = model
			break
		}
	}
	require.NotNil(t, reliantModel, "expected allowlisted Reliant model to be exposed")
	assert.Equal(t, "reliant", reliantModel.DriverId)
	assert.Equal(t, "reliant", reliantModel.Provider)

	for _, model := range resp.Msg.Models {
		assert.NotEqual(t, "gpt-5.4@reliant", model.Id, "non-allowlisted models should not be exposed for Reliant")
	}
}