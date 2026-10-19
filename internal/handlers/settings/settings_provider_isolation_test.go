package settings

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
// sends one RPC per action — ProviderAccountService.DeleteProviderAccount for a disconnect,
// Complete<Provider>OAuth for a sign-in — and each must change only
// the provider it names. These tests pin that at the RPC boundary: a user whose
// Codex vanished around a Claude sign-in (prod, 2026-10-09) must be able to
// rule the server out from the test suite alone.

// isolationProviders is every provider an account can belong to.
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
		secret := "key-for-" + provider
		switch provider {
		case "claude":
			secret = "sk-ant-oat01-claude-access"
		case "codex":
			secret = makeCodexJWTForSettingsTest(t, time.Now().Add(time.Hour))
		case "copilot":
			secret = "gho_copilot-access"
		case "antigravity":
			secret = "ya29.antigravity-access"
		}
		db.SeedProviderAccount(t, repo, userID, provider, secret)
	}
}

// providerCredentials reports, per provider, exactly what is stored for userID: each account's
// access token or key. A provider with nothing stored is absent.
func providerCredentials(t *testing.T, ctx context.Context, repo *db.Repo, userID string) map[string]string {
	t.Helper()
	accounts, err := repo.ListProviderAccountsWithSecrets(ctx, userID)
	require.NoError(t, err)
	stored := map[string]string{}
	for _, a := range accounts {
		stored[a.Provider] += fmt.Sprintf("%q ", a.Secrets.Secret)
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

func TestSettingsService_CompleteClaudeOAuth_LeavesCodexConnected(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := newTestService(t, Deps{
		Repo: repo,
		ExchangeClaudeCode: func(code, codeVerifier, redirectURI, state string) (*claude.ClaudeTokens, error) {
			return &claude.ClaudeTokens{
				AccessToken:  "sk-ant-oat01-new-claude-access",
				RefreshToken: "new-claude-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
				AccountUUID:  "acct-1", OrganizationUUID: "org-1",
				AccountEmail: "user@example.com",
			}, nil
		},
	})

	// Codex-only before the sign-in, and every provider but Claude (a user with more connected than Codex).
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
			require.NotNil(t, resp.Msg.Account)
			assert.Equal(t, "user@example.com", resp.Msg.Account.Label, "the account is labelled by its detected email")

			after := providerCredentials(t, ctx, repo, userID)
			assert.Equal(t, withoutProvider(before, "claude"), withoutProvider(after, "claude"),
				"connecting Claude must not change any other provider's credentials")

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
