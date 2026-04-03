package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCodexJWT(t *testing.T, exp time.Time) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]interface{}{
		"exp": exp.Unix(),
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": "test-account-id",
		},
	}
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
	return header + "." + payload + "." + signature
}

func TestBuildAvailableDrivers_CodexUsesPersistedAccessToken(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	validToken := makeCodexJWT(t, time.Now().Add(1*time.Hour))

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["codex"]
	require.True(t, ok, "codex should be available when oauth token is persisted")
	assert.Equal(t, validToken, cfg.APIKey)
	assert.True(t, cfg.Enabled)
}

func TestBuildAvailableDrivers_CodexExpiredTokenIsSkipped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	expiredToken := makeCodexJWT(t, time.Now().Add(-10*time.Minute))

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "openai", "sk-openai-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasCodex := availableDrivers.Drivers["codex"]
	assert.False(t, hasCodex, "codex should be skipped when access token is expired")

	openAI, hasOpenAI := availableDrivers.Drivers["openai"]
	require.True(t, hasOpenAI, "non-codex provider should still be available")
	assert.Equal(t, "sk-openai-test", openAI.APIKey)
}

func TestBuildAvailableDrivers_CodexMarkerWithoutTokensIsSkipped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "anthropic", "sk-ant-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasCodex := availableDrivers.Drivers["codex"]
	assert.False(t, hasCodex, "codex should be skipped when marker exists but tokens are missing")

	anthropic, hasAnthropic := availableDrivers.Drivers["anthropic"]
	require.True(t, hasAnthropic)
	assert.Equal(t, "sk-ant-test", anthropic.APIKey)
}

func TestBuildAvailableDrivers_ReliantUsesExchangedRuntimeAccess(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	t.Setenv(features.ReliantManagedAccessEnabledEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/controlplane.v1.LLMAccessService/GetCurrentLLMAccess", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"sk-reliant-runtime","allowedModels":["claude-4.5-sonnet"]}`))
	}))
	defer server.Close()

	InitializeAPIKeyProvider(
		repo,
		WithControlPlaneClient(controlplane.NewClient(controlplane.Config{BaseURL: server.URL})),
		WithReliantRuntimeBaseURL("https://runtime.reliant.test/v1"),
	)

	ctx := context.Background()
	userID := "test-user"
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", "cpat_test_token"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	selected, ok := availableDrivers.Drivers[models.DriverID("reliant")]
	require.True(t, ok, "reliant should be synthesized when runtime exchange succeeds")
	assert.Equal(t, models.DriverID("reliant"), selected.DriverID)
	assert.Equal(t, "sk-reliant-runtime", selected.APIKey)
	assert.Equal(t, "https://runtime.reliant.test/v1", selected.BaseURL)
	assert.Contains(t, selected.AllowedModels, models.ModelID("claude-4.5-sonnet"))
}