// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/oauth2"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

func startFlow(t *testing.T, e *env, p connections.StartParams) string {
	t.Helper()
	if p.UserID == "" {
		p.UserID = "alice"
	}
	if p.IntegrationID == "" {
		p.IntegrationID = "github"
	}
	if p.Binder == "" {
		p.Binder = "binder-1"
	}
	u, err := e.broker.Start(context.Background(), p)
	require.NoError(t, err)
	return mustQuery(t, u, "state")
}

func TestOAuth_StateIsSingleUse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	state := startFlow(t, e, connections.StartParams{})

	done, err := e.broker.Complete(ctx, connections.CompleteParams{ProviderID: "github", State: state, Code: "c1", Binder: "binder-1"})
	require.NoError(t, err)
	require.Equal(t, "alice", done.Connection.UserID)

	_, err = e.broker.Complete(ctx, connections.CompleteParams{ProviderID: "github", State: state, Code: "c1", Binder: "binder-1"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition, "a replayed callback must fail")
	require.Equal(t, 1, int(e.gh.tokenCalls.Load()), "the replay must not reach the token endpoint")
}

func TestOAuth_ConcurrentReplayExchangesOnce(t *testing.T) {
	e := newEnv(t)
	state := startFlow(t, e, connections.StartParams{})
	const n = 8
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := e.broker.Complete(context.Background(), connections.CompleteParams{State: state, Code: "c", Binder: "binder-1"})
			results <- err
		}()
	}
	ok := 0
	for i := 0; i < n; i++ {
		if <-results == nil {
			ok++
		}
	}
	require.Equal(t, 1, ok, "exactly one concurrent callback may win")
	require.Equal(t, 1, int(e.gh.tokenCalls.Load()))
}

func TestOAuth_DifferentSessionIsRejectedAndBurnsState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	state := startFlow(t, e, connections.StartParams{Binder: "victim-browser"})

	_, err := e.broker.Complete(ctx, connections.CompleteParams{State: state, Code: "attacker-code", Binder: "attacker-browser"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "different session")
	require.Zero(t, e.gh.tokenCalls.Load(), "the code must never be exchanged for the wrong session")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))

	// The state is burned: the real owner cannot use it afterwards either.
	_, err = e.broker.Complete(ctx, connections.CompleteParams{State: state, Code: "c", Binder: "victim-browser"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

func TestOAuth_ExpiredStateIsRejected(t *testing.T) {
	e := newEnv(t)
	state := startFlow(t, e, connections.StartParams{})
	_, err := e.raw.Exec(`UPDATE oauth_flows SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	_, err = e.broker.Complete(context.Background(), connections.CompleteParams{State: state, Code: "c", Binder: "binder-1"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "expired")
	require.Zero(t, e.gh.tokenCalls.Load())
}

func TestOAuth_FlowExpiresInTenMinutes(t *testing.T) {
	e := newEnv(t)
	startFlow(t, e, connections.StartParams{})
	var secs float64
	require.NoError(t, e.raw.QueryRow(`SELECT extract(epoch FROM expires_at - now()) FROM oauth_flows`).Scan(&secs))
	require.InDelta(t, (10 * time.Minute).Seconds(), secs, 15)
}

func TestOAuth_UnknownStateIsRejected(t *testing.T) {
	e := newEnv(t)
	_, err := e.broker.Complete(context.Background(), connections.CompleteParams{State: "never-issued", Code: "c", Binder: "binder-1"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestOAuth_RedirectAfterMustBeRelative(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, bad := range []string{
		"https://evil.example/x", "//evil.example/x", "http://evil.example", "javascript:alert(1)",
		`/\evil.example`, "evil.example/x", "/ok\nSet-Cookie: a=b", "/\t//evil.example",
	} {
		_, err := e.broker.Start(ctx, connections.StartParams{UserID: "alice", IntegrationID: "github", Binder: "b", RedirectAfter: bad})
		require.ErrorIs(t, err, connections.ErrInvalidArgument, "redirect_after %q must be rejected", bad)
	}
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))

	state := startFlow(t, e, connections.StartParams{RedirectAfter: "/settings/connections?x=1"})
	done, err := e.broker.Complete(ctx, connections.CompleteParams{State: state, Code: "c", Binder: "binder-1"})
	require.NoError(t, err)
	require.Equal(t, "/settings/connections?x=1", done.RedirectAfter)
}

func TestOAuth_PKCEVerifierMatchesChallenge(t *testing.T) {
	e := newEnv(t)
	authURL, err := e.broker.Start(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "github", Binder: "b"})
	require.NoError(t, err)
	challenge := mustQuery(t, authURL, "code_challenge")
	state := mustQuery(t, authURL, "state")
	require.Equal(t, clientID, mustQuery(t, authURL, "client_id"))
	require.Equal(t, "https://reliant.example/integrations/oauth/github/callback", mustQuery(t, authURL, "redirect_uri"))

	_, err = e.broker.Complete(context.Background(), connections.CompleteParams{State: state, Code: "the-code", Binder: "b"})
	require.NoError(t, err)

	e.gh.mu.Lock()
	form := e.gh.lastForm
	e.gh.mu.Unlock()
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "the-code", form.Get("code"))
	require.Equal(t, clientSecret, form.Get("client_secret"))
	verifier, err := oauth2.ParseVerifier(form.Get("code_verifier"))
	require.NoError(t, err)
	require.Equal(t, challenge, verifier.Challenge().Value, "the verifier sent to the token endpoint must hash to the challenge")
}

func TestOAuth_CompleteStoresConnectionAndSealedTokens(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "work")
	require.Equal(t, "work", conn.Name)
	require.Equal(t, "octocat", *conn.AccountLabel)
	require.Equal(t, "583231", *conn.ExternalAccountID)
	require.Equal(t, "oauth2", conn.AuthKind)
	require.Equal(t, "active", conn.Status)
	require.NotNil(t, conn.AccessExpiresAt)
	require.Equal(t, clientID, *conn.OAuthClient)
	require.Equal(t, 2, e.count(`SELECT count(*) FROM connection_secrets WHERE connection_id=$1`, conn.ID))
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='created' AND actor='user:alice'`, conn.ID))

	tok, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, len("ghu_access_1"), tok.Len())
}

func TestOAuth_ReconnectSameAccountUpdatesInPlace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "work")
	_, err := e.raw.Exec(`UPDATE connections SET status='needs_reauth', status_reason='invalid_grant' WHERE id=$1`, conn.ID)
	require.NoError(t, err)

	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: "alice", IntegrationID: "github", ReconnectID: conn.ID})
	require.NoError(t, err)
	done, err := e.svc.CompleteOAuth(ctx, "alice", mustQuery(t, authURL, "state"), "c")
	require.NoError(t, err)
	require.Equal(t, conn.ID, done.Connection.ID)
	require.Equal(t, "active", done.Connection.Status)
	require.Nil(t, done.Connection.StatusReason)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections`))
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='reconnected'`, conn.ID))
}

func TestOAuth_ReconnectAsDifferentAccountIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "work")
	e.gh.accountID, e.gh.login = 4242, "someone-else"
	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: "alice", IntegrationID: "github", ReconnectID: conn.ID})
	require.NoError(t, err)
	_, err = e.svc.CompleteOAuth(ctx, "alice", mustQuery(t, authURL, "state"), "c")
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	got, err := e.svc.Get(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, "583231", *got.ExternalAccountID)
}

func TestOAuth_SameAccountConnectedTwiceDedupes(t *testing.T) {
	e := newEnv(t)
	a := e.connect("alice", "first")
	b := e.connect("alice", "second")
	require.Equal(t, a.ID, b.ID, "connecting the same GitHub account again reuses the connection")
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections`))
}

func TestOAuth_CannotReconnectSomeoneElsesConnection(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "work")
	_, err := e.broker.Start(context.Background(), connections.StartParams{UserID: "bob", IntegrationID: "github", Binder: "b", ReconnectID: conn.ID})
	require.ErrorIs(t, err, connections.ErrNotFound)
}

func TestOAuth_ProviderWithoutCredentialsIsUnavailableNotFatal(t *testing.T) {
	reg, err := connections.ProvidersFromCatalog(testManifests(t), oauthEnv(nil))
	require.NoError(t, err, "unset credentials must not fail boot")
	p, ok := reg.Get("github")
	require.True(t, ok)
	require.False(t, p.OAuthAvailable())
	reason := p.MethodStatus(connections.MethodOAuth2).Reason
	require.Contains(t, reason, "RELIANT_OAUTH_GITHUB_CLIENT_ID")
	require.Contains(t, reason, "RELIANT_OAUTH_GITHUB_CLIENT_SECRET")
	require.True(t, p.MethodStatus(connections.MethodAPIKey).Available, "a pasted token needs no deployment config")

	reg, err = connections.ProvidersFromCatalog(testManifests(t), oauthEnv(map[string]string{
		"RELIANT_OAUTH_GITHUB_CLIENT_ID": "id", "RELIANT_OAUTH_GITHUB_CLIENT_SECRET": "sec",
	}))
	require.NoError(t, err)
	p, _ = reg.Get("github")
	require.True(t, p.OAuthAvailable())
}

func TestOAuth_UnavailableProviderCannotStart(t *testing.T) {
	e := newEnv(t)
	reg, err := connections.ProvidersFromCatalog(testManifests(t), oauthEnv(nil))
	require.NoError(t, err)
	b := connections.NewBroker(e.store, e.vault, reg, nil, "https://reliant.example")
	_, err = b.Start(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "github", Binder: "b"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	_, err = b.Start(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "nope", Binder: "b"})
	require.ErrorIs(t, err, connections.ErrNotFound)
}

// The loader is the only way into the registry, and it refuses a non-https
// endpoint; a hand-built spec that skips it is refused by the registry too.
func TestOAuth_RegistryRejectsNonHTTPSEndpoints(t *testing.T) {
	m := testManifests(t)[0]
	m.GetConnection().GetAuth()[0].GetOauth2().TokenUrl = "http://github.com/login/oauth/access_token"
	_, err := connections.ProvidersFromCatalog([]*reliantv1.IntegrationManifest{m}, oauthEnv(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "token_url")
}

func TestOAuth_CorruptSealedVerifierFailsWithoutLeakingCode(t *testing.T) {
	e := newEnv(t)
	state := startFlow(t, e, connections.StartParams{})
	// An unsupported grant makes the fake answer 400 with an error body.
	e.gh.refreshStatus = 0
	_, err := e.raw.Exec(`UPDATE oauth_flows SET pkce_verifier_sealed = pkce_verifier_sealed || '\x00'::bytea`)
	require.NoError(t, err)
	_, err = e.broker.Complete(context.Background(), connections.CompleteParams{State: state, Code: "SECRET-AUTH-CODE", Binder: "binder-1"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET-AUTH-CODE")
}
