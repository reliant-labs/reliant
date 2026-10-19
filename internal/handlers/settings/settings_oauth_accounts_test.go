package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sign-in is an account decision (see provider_accounts.go). These tests drive the Complete*
// RPCs with fake token exchanges against a real database.

func oauthRequestClaude(accountID string) *connect.Request[reliantv1.CompleteClaudeOAuthRequest] {
	req := &reliantv1.CompleteClaudeOAuthRequest{
		Code: "auth-code", CodeVerifier: "verifier", RedirectUri: "http://localhost:54545/callback", State: "s",
	}
	if accountID != "" {
		req.AccountId = &accountID
	}
	return connect.NewRequest(req)
}

type claudeLogin struct{ acct, org, email string }

// claudeService exchanges codes for whatever login the test sets next.
func claudeService(t *testing.T, repo *db.Repo, login *claudeLogin, profile func(context.Context, string) (providerusage.ClaudeAccountProfile, error)) *Service {
	t.Helper()
	return newTestService(t, Deps{
		Repo: repo,
		ExchangeClaudeCode: func(code, verifier, redirect, state string) (*claude.ClaudeTokens, error) {
			return &claude.ClaudeTokens{
				AccessToken: "access-" + login.acct + "-" + login.org, RefreshToken: "refresh-" + login.acct,
				ExpiresAt: time.Now().Add(time.Hour), AccountUUID: login.acct, AccountEmail: login.email,
				OrganizationUUID: login.org, OrganizationName: "Org " + login.org,
			}, nil
		},
		ClaudeProfile: profile,
	})
}

func accountsOfProvider(t *testing.T, ctx context.Context, repo *db.Repo, userID, provider string) []core.ProviderAccount {
	t.Helper()
	all, err := repo.ListProviderAccounts(ctx, userID)
	require.NoError(t, err)
	var out []core.ProviderAccount
	for _, a := range all {
		if a.Provider == provider {
			out = append(out, a)
		}
	}
	return out
}

func TestCompleteClaudeOAuth_SameIdentityTwiceIsOneAccountDifferentIdentityIsTwo(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := isolationContext("oauth-claude-dedupe")
	login := &claudeLogin{acct: "alice", org: "org1", email: "alice@example.com"}
	svc := claudeService(t, repo, login, nil)

	first, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.True(t, first.Msg.Created)
	assert.Nil(t, first.Msg.IdentityMismatch)
	assert.Equal(t, "alice@example.com", first.Msg.Account.Label)

	// The same upstream account signing in again refreshes its row, clears needs-sign-in, adds nothing.
	reason := "refresh token rejected"
	_, err = repo.PatchProviderAccount(ctx, "oauth-claude-dedupe", first.Msg.Account.Id, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	again, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.False(t, again.Msg.Created)
	assert.Equal(t, first.Msg.Account.Id, again.Msg.Account.Id)
	assert.Equal(t, reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE, again.Msg.Account.Status)
	assert.Contains(t, again.Msg.Message, "already connected")
	assert.Len(t, accountsOfProvider(t, ctx, repo, "oauth-claude-dedupe", "claude"), 1)

	// A different account is a second one, behind the first.
	login.acct, login.email = "bob", "bob@example.com"
	second, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.True(t, second.Msg.Created)
	accounts := accountsOfProvider(t, ctx, repo, "oauth-claude-dedupe", "claude")
	require.Len(t, accounts, 2)
	assert.Equal(t, "alice@example.com", accounts[0].DisplayLabel())
	assert.Equal(t, "bob@example.com", accounts[1].DisplayLabel())

	// One login in two organizations is two accounts: limits and cache are per organization.
	login.acct, login.org = "alice", "org2"
	_, err = svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.Len(t, accountsOfProvider(t, ctx, repo, "oauth-claude-dedupe", "claude"), 3)
}

func TestCompleteClaudeOAuth_ReconnectWithAnotherIdentityLeavesTheTargetUntouched(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "oauth-claude-mismatch"
	ctx := isolationContext(user)
	login := &claudeLogin{acct: "alice", org: "org1", email: "alice@example.com"}
	svc := claudeService(t, repo, login, nil)
	alice, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	reason := "refresh token rejected"
	_, err = repo.PatchProviderAccount(ctx, user, alice.Msg.Account.Id, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)

	// The user reconnects alice but signs in as bob.
	login.acct, login.email = "bob", "bob@example.com"
	res, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(alice.Msg.Account.Id))
	require.NoError(t, err)
	assert.True(t, res.Msg.Created, "bob was not connected, so he becomes his own account")
	require.NotNil(t, res.Msg.IdentityMismatch)
	assert.Contains(t, *res.Msg.IdentityMismatch, "bob@example.com")
	assert.Contains(t, *res.Msg.IdentityMismatch, "alice@example.com")
	assert.NotEqual(t, alice.Msg.Account.Id, res.Msg.Account.Id)
	for _, a := range accountsOfProvider(t, ctx, repo, user, "claude") {
		if a.ID == alice.Msg.Account.Id {
			assert.NotNil(t, a.NeedsReauthAt, "the target keeps waiting for its own sign-in")
		}
	}

	// Reconnecting alice as alice works and clears her.
	login.acct, login.email = "alice", "alice@example.com"
	res, err = svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(alice.Msg.Account.Id))
	require.NoError(t, err)
	assert.Nil(t, res.Msg.IdentityMismatch)
	assert.False(t, res.Msg.Created)
	assert.Equal(t, alice.Msg.Account.Id, res.Msg.Account.Id)
}

func TestCompleteClaudeOAuth_ReconnectTargetsAreScopedToTheCaller(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	login := &claudeLogin{acct: "alice", org: "org1", email: "alice@example.com"}
	svc := claudeService(t, repo, login, nil)
	owner, err := svc.CompleteClaudeOAuth(isolationContext("owner"), oauthRequestClaude(""))
	require.NoError(t, err)

	_, err = svc.CompleteClaudeOAuth(isolationContext("intruder"), oauthRequestClaude(owner.Msg.Account.Id))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = svc.CompleteClaudeOAuth(isolationContext("intruder"), oauthRequestClaude("no-such-account"))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Empty(t, accountsOfProvider(t, isolationContext("intruder"), repo, "intruder", "claude"))
}

func TestCompleteClaudeOAuth_PlanComesFromTheProfile(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := isolationContext("oauth-claude-plan")
	login := &claudeLogin{acct: "alice", org: "org1"} // the token response carried no email
	svc := claudeService(t, repo, login, func(_ context.Context, token string) (providerusage.ClaudeAccountProfile, error) {
		assert.Equal(t, "access-alice-org1", token)
		return providerusage.ClaudeAccountProfile{
			AccountUUID: "alice", Email: "alice@example.com", OrganizationUUID: "org1",
			OrganizationType: "claude_max", RateLimitTier: "default_claude_max_20x", Plan: "max_20x",
		}, nil
	})
	res, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", res.Msg.Account.Label, "the email falls back to the profile's")
	require.NotNil(t, res.Msg.Account.Plan)
	assert.Equal(t, "max_20x", *res.Msg.Account.Plan)
	require.NotNil(t, res.Msg.Account.PlanDisplay)
	assert.Equal(t, "Max 20×", *res.Msg.Account.PlanDisplay)

	// A failing profile read never fails the sign-in; the plan stays unknown.
	login.acct = "carol"
	svc = claudeService(t, repo, login, func(context.Context, string) (providerusage.ClaudeAccountProfile, error) {
		return providerusage.ClaudeAccountProfile{}, errors.New("profile endpoint is down")
	})
	res, err = svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	assert.Nil(t, res.Msg.Account.Plan)
}

func TestCompleteClaudeOAuth_ConnectIsSeenByTheDriversThroughAccounts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "oauth-claude-source"
	ctx := isolationContext(user)
	login := &claudeLogin{acct: "alice", org: "org1", email: "alice@example.com"}
	svc := claudeService(t, repo, login, nil)

	_, err := svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)

	// provider_accounts is the source of truth the drivers read: the default account carries the
	// signed-in credential, and a second account queues behind it.
	login.acct, login.email = "bob", "bob@example.com"
	_, err = svc.CompleteClaudeOAuth(ctx, oauthRequestClaude(""))
	require.NoError(t, err)
	all, err := repo.ListProviderAccountsWithSecrets(ctx, user)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "access-alice-org1", all[0].Secrets.Secret)
	assert.Equal(t, "refresh-alice", all[0].Secrets.Refresh)
	assert.Equal(t, "access-bob-org1", all[1].Secrets.Secret)
	assert.Len(t, accountsOfProvider(t, ctx, repo, user, "claude"), 2)
}

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	require.NoError(t, err)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func codexIDToken(t *testing.T, acct, user, email, plan string) string {
	return fakeJWT(t, map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": acct, "chatgpt_account_user_id": user, "chatgpt_plan_type": plan,
		},
	})
}

func TestCompleteCodexOAuth_EmailAndPlanComeFromTheIDToken(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "oauth-codex"
	ctx := isolationContext(user)
	next := &codex.CodexTokens{}
	svc := newTestService(t, Deps{
		Repo: repo,
		ExchangeCodexCode: func(code, verifier, redirect string) (*codex.CodexTokens, error) {
			tok := *next
			return &tok, nil
		},
	})
	complete := func(accountID string) *connect.Response[reliantv1.CompleteCodexOAuthResponse] {
		t.Helper()
		req := &reliantv1.CompleteCodexOAuthRequest{Code: "c", CodeVerifier: "v", RedirectUri: "http://localhost/cb"}
		if accountID != "" {
			req.AccountId = &accountID
		}
		resp, err := svc.CompleteCodexOAuth(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp
	}

	*next = codex.CodexTokens{AccessToken: "a1", RefreshToken: "r1", AccountID: "ws-1", IDToken: codexIDToken(t, "ws-1", "u-1", "me@example.com", "plus")}
	one := complete("")
	assert.True(t, one.Msg.Created)
	assert.Equal(t, "me@example.com", one.Msg.Account.Label)
	require.NotNil(t, one.Msg.Account.Plan)
	assert.Equal(t, "plus", *one.Msg.Account.Plan)

	// Same ChatGPT account and user: refreshed, not duplicated.
	next.AccessToken = "a1b"
	again := complete("")
	assert.False(t, again.Msg.Created)
	assert.Equal(t, one.Msg.Account.Id, again.Msg.Account.Id)

	// A second ChatGPT user is a second account.
	*next = codex.CodexTokens{AccessToken: "a2", RefreshToken: "r2", AccountID: "ws-2", IDToken: codexIDToken(t, "ws-2", "u-2", "you@example.com", "pro")}
	two := complete("")
	assert.True(t, two.Msg.Created)
	assert.Len(t, accountsOfProvider(t, ctx, repo, user, "codex"), 2)

	// A reconnect of the first as the second saves nothing new and says so.
	mismatch := complete(one.Msg.Account.Id)
	assert.False(t, mismatch.Msg.Created)
	require.NotNil(t, mismatch.Msg.IdentityMismatch)
	assert.Contains(t, *mismatch.Msg.IdentityMismatch, "you@example.com")

	// The default (first) account is the one the drivers use.
	accounts := accountsOfProvider(t, ctx, repo, user, "codex")
	require.Len(t, accounts, 2)
	assert.Equal(t, "ws-1", accounts[0].ExternalAccountID)
}

func TestCompleteAntigravityAndCopilot_AreKeyedByTheirIdentity(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "oauth-other"
	ctx := isolationContext(user)

	sub := "google-sub-1"
	svc := newTestService(t, Deps{
		Repo: repo,
		ExchangeAntigravityCode: func(string, string, string) (*antigravity.AntigravityTokens, error) {
			return &antigravity.AntigravityTokens{
				AccessToken: "ya29.x", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
				IDToken: fakeJWT(t, map[string]any{"sub": sub, "email": sub + "@gmail.com"}),
			}, nil
		},
		PollCopilotDevice: func(context.Context, string) (*copilot.GitHubTokens, error) {
			return &copilot.GitHubTokens{AccessToken: "gho_tok"}, nil
		},
		GitHubIdentity: func(context.Context, string) (GitHubUser, error) {
			return GitHubUser{ID: 42, Login: "octocat"}, nil
		},
	})
	anti := func() *connect.Response[reliantv1.CompleteAntigravityOAuthResponse] {
		resp, err := svc.CompleteAntigravityOAuth(ctx, connect.NewRequest(&reliantv1.CompleteAntigravityOAuthRequest{Code: "c", CodeVerifier: "v", RedirectUri: "http://localhost/cb"}))
		require.NoError(t, err)
		return resp
	}
	first := anti()
	assert.True(t, first.Msg.Created)
	assert.Equal(t, "google-sub-1@gmail.com", first.Msg.Account.Label)
	assert.False(t, anti().Msg.Created)
	sub = "google-sub-2"
	assert.True(t, anti().Msg.Created)
	assert.Len(t, accountsOfProvider(t, ctx, repo, user, "antigravity"), 2)

	poll := func() *connect.Response[reliantv1.PollCopilotDeviceAuthResponse] {
		resp, err := svc.PollCopilotDeviceAuth(ctx, connect.NewRequest(&reliantv1.PollCopilotDeviceAuthRequest{DeviceCode: "dev"}))
		require.NoError(t, err)
		return resp
	}
	cp := poll()
	assert.Equal(t, reliantv1.PollCopilotDeviceAuthResponse_STATUS_AUTHORIZED, cp.Msg.Status)
	assert.True(t, cp.Msg.Created)
	assert.Equal(t, "octocat", cp.Msg.Account.Label, "a Copilot account is labelled by its GitHub login")
	assert.False(t, poll().Msg.Created)
	assert.Len(t, accountsOfProvider(t, ctx, repo, user, "copilot"), 1)
}

func TestPollCopilotDeviceAuth_PendingStoresNothing(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := isolationContext("oauth-copilot-pending")
	svc := newTestService(t, Deps{
		Repo: repo,
		PollCopilotDevice: func(context.Context, string) (*copilot.GitHubTokens, error) {
			return nil, copilot.ErrAuthorizationPending
		},
	})
	resp, err := svc.PollCopilotDeviceAuth(ctx, connect.NewRequest(&reliantv1.PollCopilotDeviceAuthRequest{DeviceCode: "dev"}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.PollCopilotDeviceAuthResponse_STATUS_PENDING, resp.Msg.Status)
	assert.Nil(t, resp.Msg.Account)
	assert.Empty(t, accountsOfProvider(t, ctx, repo, "oauth-copilot-pending", "copilot"))
}

func TestSyncReliantProvider_WritesTheManagedAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "oauth-reliant"
	auth.SetUserJWT(user, "jwt")
	fake := &fakeControlPlaneClient{issueKey: "rlat_abcdef0123456789abcdef0123456789"}
	svc := newTestService(t, Deps{Repo: repo, ControlPlane: fake})
	ctx := isolationContext(user)

	for range 2 { // a re-sync rotates the one managed account, never adds a second
		_, err := svc.SyncReliantProvider(ctx, connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
		require.NoError(t, err)
	}
	accounts := accountsOfProvider(t, ctx, repo, user, "reliant")
	require.Len(t, accounts, 1)
	assert.Equal(t, core.AuthKindManaged, accounts[0].AuthKind)
	assert.Equal(t, "rlat_abcdef0123456789abcdef0123456789", managedKey(t, ctx, repo, user))
}

// managedKey is the key of the user's managed Reliant account, read from provider_accounts: the
// source of truth the drivers read.
func managedKey(t *testing.T, ctx context.Context, repo *db.Repo, userID string) string {
	t.Helper()
	all, err := repo.ListProviderAccountsWithSecrets(ctx, userID)
	require.NoError(t, err)
	for _, a := range all {
		if a.Provider == "reliant" {
			return a.Secrets.Secret
		}
	}
	return ""
}
