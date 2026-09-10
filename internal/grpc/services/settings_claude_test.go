package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeTokenRepo embeds db.Repository so it satisfies the whole interface
// while overriding only the reads the provider-status and validation paths
// touch. Any other method the code under test starts calling will nil-panic
// loudly rather than silently returning a zero value.
//
// These tests deliberately avoid a real database: the question under test is
// purely how persisted Claude token fields are interpreted, and a stub keeps
// the assertions runnable without a Postgres instance.
type claudeTokenRepo struct {
	db.Repository

	claudeTokens *db.ClaudeAuthTokens
	apiKeys      map[string]string
}

func (r *claudeTokenRepo) GetClaudeAuthTokens(context.Context, string) (*db.ClaudeAuthTokens, error) {
	return r.claudeTokens, nil
}

func (r *claudeTokenRepo) GetCodexAuthTokens(context.Context, string) (*db.CodexAuthTokens, error) {
	return nil, nil
}

func (r *claudeTokenRepo) GetCopilotAuthTokens(context.Context, string) (*db.CopilotAuthTokens, error) {
	return nil, nil
}

func (r *claudeTokenRepo) GetProviderAPIKeys(context.Context, string) (map[string]string, error) {
	if r.apiKeys == nil {
		return map[string]string{}, nil
	}
	return r.apiKeys, nil
}

func newClaudeSettingsService(tokens *db.ClaudeAuthTokens) *SettingsService {
	return NewSettingsService(&claudeTokenRepo{
		claudeTokens: tokens,
		apiKeys:      map[string]string{"claude": "oauth"},
	}, nil)
}

// claudeStatus runs GetProviderStatuses and returns the Claude entry.
func claudeStatus(t *testing.T, tokens *db.ClaudeAuthTokens) *reliantv1.ProviderStatus {
	t.Helper()

	svc := newClaudeSettingsService(tokens)
	resp, err := svc.GetProviderStatuses(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)
	return findProviderStatus(t, resp.Msg.Providers, "claude")
}

// TestSettingsService_GetProviderStatuses_ClaudeRefreshableTokenStaysConfigured
// pins the distinction between "the access token needs refreshing" and "the
// user must reconnect Claude".
//
// claude.IsTokenExpired reports true a full TokenRefreshBuffer (5 minutes)
// BEFORE the real expiry, so it is a proactive "refresh before using" signal,
// not a statement that the session is dead. A stored refresh token is what
// makes the session recoverable without user action, and the transport
// interceptor refreshes it automatically — the same rule
// drivers.BuildAvailableDrivers already applies when deciding whether the
// credential is usable.
//
// Equating IsTokenExpired with disconnected therefore reported Claude as
// unconfigured while the credentials were persisted and refreshing normally.
func TestSettingsService_GetProviderStatuses_ClaudeRefreshableTokenStaysConfigured(t *testing.T) {
	t.Run("near expiry inside the refresh buffer stays configured", func(t *testing.T) {
		// Inside the 5-minute proactive refresh window, but not actually expired.
		status := claudeStatus(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(2 * time.Minute),
		})

		assert.True(t, status.Configured, "a token inside the refresh buffer with a refresh token is still connected")
		assert.True(t, status.HasApiKey)
	})

	t.Run("expired access token with a refresh token stays configured", func(t *testing.T) {
		status := claudeStatus(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})

		assert.True(t, status.Configured, "an expired token is refreshable while a refresh token is stored")
		assert.True(t, status.HasApiKey)
	})

	t.Run("expired access token without a refresh token is not configured", func(t *testing.T) {
		status := claudeStatus(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})

		assert.False(t, status.Configured, "no refresh token and an expired access token needs a reconnect")
		assert.False(t, status.HasApiKey)
	})

	t.Run("no stored access token is not configured", func(t *testing.T) {
		status := claudeStatus(t, nil)

		assert.False(t, status.Configured)
		assert.False(t, status.HasApiKey)
	})

	t.Run("blank access token with a refresh token is not configured", func(t *testing.T) {
		// A refresh token alone is not a connection: the access token is the
		// credential the driver registers.
		status := claudeStatus(t, &db.ClaudeAuthTokens{
			AccessToken:  "   ",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		})

		assert.False(t, status.Configured)
		assert.False(t, status.HasApiKey)
	})
}

// TestSettingsService_ValidateProviderAPIKey_ClaudeMatchesConfiguredSemantics
// keeps the explicit "Validate" action from contradicting the status the
// settings list already shows. Reporting "session expired, please reconnect"
// for a credential that GetProviderStatuses reports as connected — and that
// the driver refreshes without user action — sends the user to re-run an
// OAuth flow they do not need.
func TestSettingsService_ValidateProviderAPIKey_ClaudeMatchesConfiguredSemantics(t *testing.T) {
	validate := func(t *testing.T, tokens *db.ClaudeAuthTokens) *reliantv1.ValidateProviderAPIKeyResponse {
		t.Helper()

		svc := newClaudeSettingsService(tokens)
		resp, err := svc.ValidateProviderAPIKey(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
			Provider: "claude",
		}))
		require.NoError(t, err)
		return resp.Msg
	}

	t.Run("near expiry inside the refresh buffer is valid", func(t *testing.T) {
		msg := validate(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(2 * time.Minute),
		})

		assert.True(t, msg.Valid)
		assert.Equal(t, "Connected to Claude", msg.Message)
	})

	t.Run("expired access token with a refresh token is valid", func(t *testing.T) {
		msg := validate(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})

		assert.True(t, msg.Valid)
		assert.Equal(t, "Connected to Claude", msg.Message)
	})

	t.Run("expired access token without a refresh token asks for a reconnect", func(t *testing.T) {
		msg := validate(t, &db.ClaudeAuthTokens{
			AccessToken:  "access-token",
			RefreshToken: "",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})

		assert.False(t, msg.Valid)
		assert.Contains(t, msg.Message, "session expired")
	})

	t.Run("missing tokens report not connected", func(t *testing.T) {
		msg := validate(t, nil)

		assert.False(t, msg.Valid)
		assert.Equal(t, "Claude is not connected", msg.Message)
	})
}
