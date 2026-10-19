package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCodexJWTForSettingsTest(t *testing.T, exp time.Time) string {
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

func newSettingsServiceTestContext() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

func findProviderStatus(t *testing.T, statuses []*reliantv1.ProviderStatus, provider string) *reliantv1.ProviderStatus {
	t.Helper()
	for _, status := range statuses {
		if status.Provider == provider {
			return status
		}
	}
	t.Fatalf("provider %q not found", provider)
	return nil
}

func TestSettingsService_ValidateProviderAPIKey_SubscriptionProvidersReportTheirAccounts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := newTestService(t, Deps{Repo: repo})
	ctx := newSettingsServiceTestContext()
	userID := "test-user"
	validate := func(provider string) *reliantv1.ValidateProviderAPIKeyResponse {
		t.Helper()
		resp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{Provider: provider}))
		require.NoError(t, err)
		return resp.Msg
	}

	// No account => not connected.
	missing := validate("codex")
	assert.False(t, missing.Valid)
	assert.Equal(t, "Codex is not connected", missing.Message)

	// An account waiting for sign-in => session expired.
	acct, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "codex", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.CodexIdentityKey("A", "U"),
		Secrets:     core.AccountSecrets{Secret: "codex-access", Refresh: "codex-refresh"},
	})
	require.NoError(t, err)
	reason := "refresh token rejected"
	_, err = repo.PatchProviderAccount(ctx, userID, acct.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	expired := validate("codex")
	assert.False(t, expired.Valid)
	assert.Contains(t, expired.Message, "session expired")

	// Signing in again clears it.
	_, _, err = repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "codex", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.CodexIdentityKey("A", "U"),
		Secrets:     core.AccountSecrets{Secret: "codex-access-2", Refresh: "codex-refresh-2"},
	})
	require.NoError(t, err)
	ok := validate("codex")
	assert.True(t, ok.Valid)
	assert.Equal(t, "Connected to Codex", ok.Message)
}

func TestSettingsService_GetProviderStatuses_ConfiguredMeansAnAccountCanServe(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := newTestService(t, Deps{Repo: repo})
	ctx := newSettingsServiceTestContext()
	userID := "test-user"
	statusOf := func(provider string) *reliantv1.ProviderStatus {
		t.Helper()
		resp, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
		require.NoError(t, err)
		return findProviderStatus(t, resp.Msg.Providers, provider)
	}

	assert.False(t, statusOf("codex").Configured)
	assert.Zero(t, statusOf("codex").AccountCount)

	first, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "codex", AuthKind: core.AuthKindOAuth, IdentityKey: core.CodexIdentityKey("A", "U1"),
		Secrets: core.AccountSecrets{Secret: "a1", Refresh: "r1"},
	})
	require.NoError(t, err)
	assert.True(t, statusOf("codex").Configured)
	assert.EqualValues(t, 1, statusOf("codex").AccountCount)

	// One account waiting for sign-in does not unconfigure the provider while another can serve.
	_, _, err = repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "codex", AuthKind: core.AuthKindOAuth, IdentityKey: core.CodexIdentityKey("A", "U2"),
		Secrets: core.AccountSecrets{Secret: "a2", Refresh: "r2"},
	})
	require.NoError(t, err)
	reason := "refresh token rejected"
	_, err = repo.PatchProviderAccount(ctx, userID, first.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	assert.True(t, statusOf("codex").Configured)
	assert.EqualValues(t, 2, statusOf("codex").AccountCount)

	// Every account waiting for sign-in: not configured, but still counted so the UI can offer Reconnect.
	accounts, err := repo.ListProviderAccounts(ctx, userID)
	require.NoError(t, err)
	for _, a := range accounts {
		_, err = repo.PatchProviderAccount(ctx, userID, a.ID, core.AccountPatch{NeedsReauth: &reason})
		require.NoError(t, err)
	}
	assert.False(t, statusOf("codex").Configured)
	assert.EqualValues(t, 2, statusOf("codex").AccountCount)

	// Providers are independent.
	assert.False(t, statusOf("claude").Configured)
}
