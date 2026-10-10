package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Provider credentials are independent of each other. Settings → Providers
// sends one RPC per action — UpdateProviderAPIKey for a disconnect or a key
// change, Complete<Provider>OAuth for a sign-in — and each must change only
// the provider it names. These tests pin that at the RPC boundary: a user whose
// Codex vanished around a Claude sign-in (prod, 2026-10-09) must be able to
// rule the server out from the test suite alone.

// isolationProviders is every provider UpdateProviderAPIKey accepts.
var isolationProviders = []string{
	"claude", "codex", "copilot", "antigravity",
	"reliant", "anthropic", "openai", "gemini", "openrouter",
}

func isolationContext(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

// seedProviders connects every named provider for userID, each with a
// credential unique to it, so a cross-provider write shows up as a changed value.
func seedProviders(t *testing.T, ctx context.Context, repo *db.Repo, userID string, providers ...string) {
	t.Helper()
	for _, provider := range providers {
		marker := db.ProviderOAuthMarker
		switch provider {
		case "claude":
			require.NoError(t, repo.SetClaudeAuthTokens(ctx, userID, db.ClaudeAuthTokens{
				AccessToken:  "sk-ant-oat01-claude-access",
				RefreshToken: "claude-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))
		case "codex":
			require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
				AccessToken:  makeCodexJWTForSettingsTest(t, time.Now().Add(time.Hour)),
				RefreshToken: "codex-refresh",
				AccountID:    "codex-account",
			}))
		case "copilot":
			require.NoError(t, repo.SetCopilotAuthTokens(ctx, userID, db.CopilotAuthTokens{
				GitHubAccessToken:  "gho_copilot-access",
				GitHubRefreshToken: "copilot-refresh",
			}))
		case "antigravity":
			require.NoError(t, repo.SetAntigravityAuthTokens(ctx, userID, db.AntigravityAuthTokens{
				AccessToken:  "ya29.antigravity-access",
				RefreshToken: "antigravity-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))
		default:
			marker = "key-for-" + provider
		}
		require.NoError(t, repo.SetProviderAPIKey(ctx, userID, provider, marker))
	}
}

// providerCredentials reports, per provider, exactly what is stored for
// userID: the api_keys value and, for OAuth providers, the access token.
// A provider with nothing stored is absent.
func providerCredentials(t *testing.T, ctx context.Context, repo *db.Repo, userID string) map[string]string {
	t.Helper()
	keys, err := repo.GetProviderAPIKeys(ctx, userID)
	require.NoError(t, err)

	tokens := map[string]string{}
	claudeTokens, err := repo.GetClaudeAuthTokens(ctx, userID)
	require.NoError(t, err)
	if claudeTokens != nil {
		tokens["claude"] = claudeTokens.AccessToken
	}
	codexTokens, err := repo.GetCodexAuthTokens(ctx, userID)
	require.NoError(t, err)
	if codexTokens != nil {
		tokens["codex"] = codexTokens.AccessToken
	}
	copilotTokens, err := repo.GetCopilotAuthTokens(ctx, userID)
	require.NoError(t, err)
	if copilotTokens != nil {
		tokens["copilot"] = copilotTokens.GitHubAccessToken
	}
	antigravityTokens, err := repo.GetAntigravityAuthTokens(ctx, userID)
	require.NoError(t, err)
	if antigravityTokens != nil {
		tokens["antigravity"] = antigravityTokens.AccessToken
	}

	stored := map[string]string{}
	for _, provider := range isolationProviders {
		key, hasKey := keys[provider]
		token, hasToken := tokens[provider]
		if !hasKey && !hasToken {
			continue
		}
		stored[provider] = fmt.Sprintf("api_keys=%q token=%q", key, token)
	}
	return stored
}

func withoutProvider(credentials map[string]string, provider string) map[string]string {
	rest := make(map[string]string, len(credentials))
	for p, c := range credentials {
		if p != provider {
			rest[p] = c
		}
	}
	return rest
}

func TestSettingsService_DisconnectingOneProviderLeavesEveryOtherProviderIntact(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	svc := NewSettingsService(repo, nil)

	for _, target := range isolationProviders {
		t.Run(target, func(t *testing.T) {
			// One user per case: each starts with every provider connected.
			userID := "isolation-disconnect-" + target
			ctx := isolationContext(userID)
			seedProviders(t, ctx, repo, userID, isolationProviders...)
			before := providerCredentials(t, ctx, repo, userID)
			require.Len(t, before, len(isolationProviders), "every provider must be seeded")

			// Exactly what Settings → Providers sends for "Disconnect"/"Delete".
			resp, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
				Provider: target,
				ApiKey:   "",
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Success)

			after := providerCredentials(t, ctx, repo, userID)
			assert.NotContains(t, after, target, "the named provider must be disconnected")
			assert.Equal(t, withoutProvider(before, target), after,
				"disconnecting %s must not change any other provider's credentials", target)
		})
	}
}

func TestSettingsService_CompleteClaudeOAuth_LeavesCodexConnected(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil).WithClaudeCodeExchange(
		func(code, codeVerifier, redirectURI, state string) (*claude.ClaudeTokens, error) {
			return &claude.ClaudeTokens{
				AccessToken:  "sk-ant-oat01-new-claude-access",
				RefreshToken: "new-claude-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
				AccountEmail: "user@example.com",
			}, nil
		})

	// Codex-only before the sign-in (prod's user), and every provider but
	// Claude (a user with more connected than Codex).
	cases := map[string][]string{
		"codex only":           {"codex"},
		"every other provider": withoutClaude(isolationProviders),
		"claude already there": isolationProviders, // a re-sign-in
	}
	for name, seeded := range cases {
		t.Run(name, func(t *testing.T) {
			userID := "isolation-claude-oauth-" + name
			ctx := isolationContext(userID)
			seedProviders(t, ctx, repo, userID, seeded...)
			before := providerCredentials(t, ctx, repo, userID)

			resp, err := svc.CompleteClaudeOAuth(ctx, connect.NewRequest(&reliantv1.CompleteClaudeOAuthRequest{
				Code:         "auth-code",
				CodeVerifier: "verifier",
				RedirectUri:  "http://localhost:54545/callback",
				State:        "reliant:oauth:claude:state",
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Success)

			after := providerCredentials(t, ctx, repo, userID)
			assert.Equal(t, withoutProvider(before, "claude"), withoutProvider(after, "claude"),
				"connecting Claude must not change any other provider's credentials")
			assert.Equal(t,
				fmt.Sprintf("api_keys=%q token=%q", db.ProviderOAuthMarker, "sk-ant-oat01-new-claude-access"),
				after["claude"])

			// And the user sees both connected, which is what Settings renders.
			statuses, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
			require.NoError(t, err)
			assert.True(t, findProviderStatus(t, statuses.Msg.Providers, "claude").Configured)
			assert.True(t, findProviderStatus(t, statuses.Msg.Providers, "codex").Configured,
				"Codex must still be connected after a Claude sign-in")
		})
	}
}

func withoutClaude(providers []string) []string {
	rest := make([]string, 0, len(providers))
	for _, p := range providers {
		if p != "claude" {
			rest = append(rest, p)
		}
	}
	return rest
}
