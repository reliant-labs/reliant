package drivers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveReliantBaseURL_DefaultWhenUnset(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, reliantDefaultBaseURL, resolved)
}

func TestResolveReliantBaseURL_UsesConfiguredRemoteURLForManagedKey(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "https://proxy.example.com/v1")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, "https://proxy.example.com/v1", resolved)
}

func TestResolveReliantBaseURL_UsesConfiguredLoopbackURLForManagedKey(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "http://localhost:4000/v1")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, "http://localhost:4000/v1", resolved)
}

func TestResolveReliantAPIKey_UsesLoopbackMasterKeyForManagedKey(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-local-master")

	resolvedKey, headers := ResolveReliantAPIKey("rlnt_managed_key", "http://localhost:4000/v1")

	assert.Equal(t, "sk-local-master", resolvedKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rlnt_managed_key"}, headers)
}

func TestResolveReliantAPIKey_FallsBackToDefaultLocalMasterKey(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "")

	resolvedKey, headers := ResolveReliantAPIKey("rly_legacy_managed_key", "http://127.0.0.1:4000/v1")

	assert.Equal(t, reliantLocalLiteLLMMasterKey, resolvedKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rly_legacy_managed_key"}, headers)
}

func TestResolveReliantAPIKey_PreservesManualKeyOffLoopback(t *testing.T) {
	resolvedKey, headers := ResolveReliantAPIKey("sk-litellm-test", "http://127.0.0.1:4000/v1")

	assert.Equal(t, "sk-litellm-test", resolvedKey)
	assert.Nil(t, headers)
}

func TestResolveReliantAPIKey_PreservesManagedKeyForRemoteBaseURL(t *testing.T) {
	resolvedKey, headers := ResolveReliantAPIKey("rlnt_managed_key", "https://api.reliant.dev/v1")

	assert.Equal(t, "rlnt_managed_key", resolvedKey)
	assert.Nil(t, headers)
}

func TestBuildAvailableDrivers_ReliantManagedKeyUsesLoopbackMasterKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	t.Setenv("RELIANT_API_BASE_URL", "http://localhost:4000/v1")
	t.Setenv("LITELLM_MASTER_KEY", "sk-local-master")
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", "rlnt_managed_key"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["reliant"]
	require.True(t, ok)
	assert.Equal(t, "http://localhost:4000/v1", cfg.BaseURL)
	assert.Equal(t, "sk-local-master", cfg.APIKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rlnt_managed_key"}, cfg.ExtraHeaders)
}

func TestBuildAvailableDrivers_ReliantManualKeyKeepsLoopbackOverride(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	t.Setenv("RELIANT_API_BASE_URL", "http://localhost:4000/v1")
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", "sk-litellm-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["reliant"]
	require.True(t, ok)
	assert.Equal(t, "http://localhost:4000/v1", cfg.BaseURL)
	assert.Equal(t, "sk-litellm-test", cfg.APIKey)
	assert.Nil(t, cfg.ExtraHeaders)
}

func TestIsLoopbackBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		expected bool
	}{
		{name: "localhost", rawURL: "http://localhost:4000/v1", expected: true},
		{name: "ipv4 loopback", rawURL: "http://127.0.0.1:4000/v1", expected: true},
		{name: "ipv6 loopback", rawURL: "http://[::1]:4000/v1", expected: true},
		{name: "remote host", rawURL: "https://proxy.example.com/v1", expected: false},
		{name: "invalid", rawURL: "://bad", expected: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, isLoopbackBaseURL(test.rawURL))
		})
	}
}