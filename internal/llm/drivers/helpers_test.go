package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepo is a minimal db.Repository stub for testing BuildAvailableDrivers.
// It embeds db.Repository (left nil) so that any unimplemented method panics —
// our safety net to catch unexpected calls from the function under test.
type fakeRepo struct {
	db.Repository

	mu sync.Mutex

	maskedKeys     map[string]string
	maskedKeysErr  error
	providerKeys   map[string]string
	providerKeyErr error

	setCalls []setProviderAPIKeyCall
	setErr   error
}

type setProviderAPIKeyCall struct {
	UserID   string
	Provider string
	APIKey   string
}

func (f *fakeRepo) GetProviderAPIKeys(_ context.Context, _ string) (map[string]string, error) {
	if f.maskedKeysErr != nil {
		return nil, f.maskedKeysErr
	}
	out := make(map[string]string, len(f.maskedKeys))
	for k, v := range f.maskedKeys {
		out[k] = v
	}
	return out, nil
}

func (f *fakeRepo) GetProviderAPIKey(_ context.Context, _ string, provider string) (string, error) {
	if f.providerKeyErr != nil {
		return "", f.providerKeyErr
	}
	return f.providerKeys[provider], nil
}

func (f *fakeRepo) SetProviderAPIKey(_ context.Context, userID, provider, apiKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, setProviderAPIKeyCall{
		UserID:   userID,
		Provider: provider,
		APIKey:   apiKey,
	})
	return f.setErr
}

// helpers.go conditionally calls these for claude/codex provider markers; we
// never include those markers in tests below, so these should not be invoked.
// Implement them as safe no-ops anyway in case the function evolves.
func (f *fakeRepo) GetClaudeAuthTokens(_ context.Context, _ string) (*db.ClaudeAuthTokens, error) {
	return nil, nil
}

func (f *fakeRepo) GetCodexAuthTokens(_ context.Context, _ string) (*db.CodexAuthTokens, error) {
	return nil, nil
}

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

// TestBuildAvailableDrivers_LazyProvisionsReliantWhenMissing verifies the
// lazy-provision branch at the bottom of BuildAvailableDrivers: when no
// "reliant" entry exists in the user's stored provider keys, the function
// mints a new virtual key via MintReliantUserAPIKey, persists it, and adds
// it to the returned driver map.
func TestBuildAvailableDrivers_LazyProvisionsReliantWhenMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"sk-test-virtual-key"}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL)
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	repo := &fakeRepo{
		maskedKeys: map[string]string{}, // no reliant entry
	}

	availableDrivers, err := BuildAvailableDrivers(context.Background(), repo, "user-1")
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["reliant"]
	require.True(t, ok, "reliant driver should be lazy-provisioned and present")
	assert.Equal(t, "sk-test-virtual-key", cfg.APIKey)
	assert.True(t, cfg.Enabled)

	require.Len(t, repo.setCalls, 1, "SetProviderAPIKey should be called exactly once")
	assert.Equal(t, setProviderAPIKeyCall{
		UserID:   "user-1",
		Provider: "reliant",
		APIKey:   "sk-test-virtual-key",
	}, repo.setCalls[0])
}

// TestBuildAvailableDrivers_UsesExistingReliantKeyWithoutProvisioning verifies
// that when a reliant key is already persisted, the lazy-provision branch is
// skipped: no admin HTTP call is made and SetProviderAPIKey is not invoked.
func TestBuildAvailableDrivers_UsesExistingReliantKeyWithoutProvisioning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("unexpected admin HTTP call: existing reliant key should skip mint")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL)
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	repo := &fakeRepo{
		maskedKeys: map[string]string{
			"reliant": "sk-masked",
		},
		providerKeys: map[string]string{
			"reliant": "sk-existing-real-key",
		},
	}

	availableDrivers, err := BuildAvailableDrivers(context.Background(), repo, "user-2")
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["reliant"]
	require.True(t, ok, "reliant driver should be present from the existing stored key")
	assert.Equal(t, "sk-existing-real-key", cfg.APIKey)
	assert.True(t, cfg.Enabled)

	assert.Empty(t, repo.setCalls, "SetProviderAPIKey must not be called when a key already exists")
}

// TestBuildAvailableDrivers_LazyProvisionMintFailureOmitsReliant verifies that
// when MintReliantUserAPIKey fails (HTTP 500 from the admin endpoint), the
// function logs and continues: reliant is omitted from the driver map, no key
// is persisted, and no error is returned to the caller.
func TestBuildAvailableDrivers_LazyProvisionMintFailureOmitsReliant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL)
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	repo := &fakeRepo{
		maskedKeys: map[string]string{},
	}

	availableDrivers, err := BuildAvailableDrivers(context.Background(), repo, "user-3")
	require.NoError(t, err, "mint failure must not surface as an error from BuildAvailableDrivers")

	_, ok := availableDrivers.Drivers["reliant"]
	assert.False(t, ok, "reliant driver should be absent when mint fails")

	assert.Empty(t, repo.setCalls, "SetProviderAPIKey must not be called when mint fails")
}
