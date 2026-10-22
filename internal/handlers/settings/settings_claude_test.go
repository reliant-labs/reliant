package settings

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accountsRepo programs only the read the provider-status and validation paths touch. Any other
// method the code under test starts calling answers the mock's "Func not set" error rather than a
// silent zero value. No database: the question under test is purely how stored account state is
// read.
func accountsRepo(accounts []core.ProviderAccount) *MockRepository {
	return &MockRepository{
		ListProviderAccountsFunc: func(context.Context, string) ([]core.ProviderAccount, error) {
			return accounts, nil
		},
	}
}

func claudeAccount(needsReauth bool) core.ProviderAccount {
	a := core.ProviderAccount{ID: "c1", Provider: "claude", AuthKind: core.AuthKindOAuth}
	if needsReauth {
		now := a.CreatedAt
		a.NeedsReauthAt = &now
		a.NeedsReauthReason = "refresh token rejected"
	}
	return a
}

func claudeStatus(t *testing.T, accounts ...core.ProviderAccount) *reliantv1.ProviderStatus {
	t.Helper()
	svc := newTestService(t, Deps{Repo: accountsRepo(accounts)})
	resp, err := svc.GetProviderStatuses(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)
	return findProviderStatus(t, resp.Msg.Providers, "claude")
}

// A Claude account is connected until a refresh proves its session dead: the account's access token
// expiring (or sitting inside the proactive refresh window) says nothing, because the transport
// refreshes it with the stored refresh token. Only the account's needs-sign-in state — set when a
// refresh fails with no newer lineage — means the user must reconnect.
func TestSettingsService_GetProviderStatuses_ClaudeIsConnectedUntilARefreshFails(t *testing.T) {
	t.Run("a stored account is configured", func(t *testing.T) {
		status := claudeStatus(t, claudeAccount(false))
		assert.True(t, status.Configured)
		assert.EqualValues(t, 1, status.AccountCount)
	})
	t.Run("an account waiting for sign-in is not configured but is counted", func(t *testing.T) {
		status := claudeStatus(t, claudeAccount(true))
		assert.False(t, status.Configured)
		assert.EqualValues(t, 1, status.AccountCount)
	})
	t.Run("no account is not configured", func(t *testing.T) {
		status := claudeStatus(t)
		assert.False(t, status.Configured)
		assert.Zero(t, status.AccountCount)
	})
}

// The explicit Validate action must not contradict the status the settings list already shows.
func TestSettingsService_ValidateProviderAPIKey_ClaudeMatchesConfiguredSemantics(t *testing.T) {
	validate := func(t *testing.T, accounts ...core.ProviderAccount) *reliantv1.ValidateProviderAPIKeyResponse {
		t.Helper()
		svc := newTestService(t, Deps{Repo: accountsRepo(accounts)})
		resp, err := svc.ValidateProviderAPIKey(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
			Provider: "claude",
		}))
		require.NoError(t, err)
		return resp.Msg
	}

	msg := validate(t, claudeAccount(false))
	assert.True(t, msg.Valid)
	assert.Equal(t, "Connected to Claude", msg.Message)

	msg = validate(t, claudeAccount(true))
	assert.False(t, msg.Valid)
	assert.Contains(t, msg.Message, "session expired")

	msg = validate(t)
	assert.False(t, msg.Valid)
	assert.Equal(t, "Claude is not connected", msg.Message)
}
