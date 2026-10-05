// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/stretchr/testify/require"
)

// The Slack tests drive the manifest the binary ships (catalog.MustBuiltin)
// through the real broker and token source, against a fake of slack.com that
// answers the way Slack's OAuth v2 does: oauth.v2.access returns the BOT
// token at the top level and nests a user token under authed_user, and every
// failure is HTTP 200 with {"ok": false, "error": "..."}.

var slackCreds = map[string]string{
	"RELIANT_OAUTH_SLACK_CLIENT_ID":     "1234.5678",
	"RELIANT_OAUTH_SLACK_CLIENT_SECRET": "slack-client-secret-canary",
}

// slackGranted is the scope string the fake grants, comma-joined as Slack
// reports it.
const slackGranted = "app_mentions:read,channels:history,chat:write"

type fakeSlack struct {
	srv *httptest.Server

	mu         sync.Mutex
	forms      []url.Values
	probeAuths []string
	teamID     string
	teamName   string
	// rotate makes the fake behave like an app with token rotation on: an
	// expiring access token plus a refresh token.
	rotate        bool
	exchangeError string
	refreshError  string
	seq           atomic.Int64
	refreshCalls  atomic.Int32
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{teamID: "T0ACME", teamName: "Acme Corp"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth.v2.access", f.token)
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.probeAuths = append(f.probeAuths, r.Header.Get("Authorization"))
		team, name := f.teamID, f.teamName
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer xoxb-") && !strings.HasPrefix(auth, "Bearer xoxe.xoxb-") {
			// A user token (or none) is not the bot: Slack would answer for a
			// different identity; the fake refuses so a mix-up cannot pass.
			_, _ = w.Write([]byte(`{"ok":false,"error":"not_authed"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "url": "https://acme.slack.com/", "team": name, "user": "reliant",
			"team_id": team, "user_id": "U0BOT", "bot_id": "B0BOT", "is_enterprise_install": false,
		})
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	team, name, rotate := f.teamID, f.teamName, f.rotate
	exchangeErr, refreshErr := f.exchangeError, f.refreshError
	f.mu.Unlock()
	n := itoa(f.seq.Add(1))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if exchangeErr != "" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"` + exchangeErr + `"}`))
			return
		}
		resp := map[string]any{
			"ok": true, "access_token": "xoxb-bot-" + n, "token_type": "bot", "scope": slackGranted,
			"bot_user_id": "U0BOT", "app_id": "A0APP",
			"team":       map[string]any{"id": team, "name": name},
			"enterprise": nil, "is_enterprise_install": false,
			// The user half of the install. Reliant acts as the bot, so none of
			// this may be stored or used.
			"authed_user": map[string]any{
				"id": "U0HUMAN", "scope": "search:read", "access_token": "xoxp-user-DECOY-" + n, "token_type": "user",
			},
		}
		if rotate {
			resp["access_token"] = "xoxe.xoxb-bot-" + n
			resp["expires_in"] = 43200
			resp["refresh_token"] = "xoxe-1-refresh-" + n
			resp["authed_user"].(map[string]any)["refresh_token"] = "xoxe-1-user-DECOY-" + n
		}
		_ = json.NewEncoder(w).Encode(resp)
	case "refresh_token":
		f.refreshCalls.Add(1)
		if refreshErr != "" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"` + refreshErr + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "access_token": "xoxe.xoxb-bot-" + n, "token_type": "bot", "scope": slackGranted,
			"expires_in": 43200, "refresh_token": "xoxe-1-refresh-" + n,
			"team": map[string]any{"id": team, "name": name},
		})
	default:
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_grant_type"}`))
	}
}

func (f *fakeSlack) lastForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[len(f.forms)-1]
}

func (f *fakeSlack) lastProbeAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeAuths[len(f.probeAuths)-1]
}

// shippedManifest is the embedded catalog's declaration of an integration.
func shippedManifest(t *testing.T, id string) *reliantv1.IntegrationManifest {
	t.Helper()
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() == id {
			return m
		}
	}
	t.Fatalf("the catalog ships no %q manifest", id)
	return nil
}

// slackEnv is the standard env plus the shipped Slack provider, whose calls
// reach the fake at whatever slack.com URL the manifest names.
func slackEnv(t *testing.T) (*env, *fakeSlack) {
	t.Helper()
	e := newEnv(t)
	f := newFakeSlack(t)
	providers, err := connections.ProvidersFromCatalog(append(testManifests(t), shippedManifest(t, "slack")), oauthEnv(slackCreds))
	require.NoError(t, err)
	doer := hostRouter{"slack.com": f.srv, "": e.gh.srv}
	e.providers = providers
	e.tokens = connections.NewTokenSource(e.store, e.vault, providers, doer)
	e.broker = connections.NewBroker(e.store, e.vault, providers, doer, "https://reliant.example")
	e.svc = connections.NewService(e.store, e.vault, providers, e.tokens, e.broker, doer)
	e.resolver = connections.NewResolver(e.repo, e.store, e.tokens)
	return e, f
}

func (e *env) connectSlack(userID string) *connections.Completion {
	e.t.Helper()
	ctx := context.Background()
	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: userID, IntegrationID: "slack"})
	require.NoError(e.t, err)
	done, err := e.svc.CompleteOAuth(ctx, userID, mustQuery(e.t, authURL, "state"), "slack-code")
	require.NoError(e.t, err)
	return done
}

func TestSlackOAuth_AuthorizeURLCarriesCommaJoinedBotScopes(t *testing.T) {
	e, _ := slackEnv(t)
	authURL, err := e.svc.StartOAuth(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "slack"})
	require.NoError(t, err)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, "https", u.Scheme)
	require.Equal(t, "slack.com", u.Host)
	require.Equal(t, "/oauth/v2/authorize", u.Path)

	q := u.Query()
	declared := shippedManifest(t, "slack").GetConnection().GetAuth()[0].GetOauth2().GetScopes()
	require.NotEmpty(t, declared)
	require.Equal(t, strings.Join(declared, ","), q.Get("scope"), "Slack v2 bot scopes are comma-separated")
	require.NotContains(t, q.Get("scope"), " ")
	require.Contains(t, u.RawQuery, "scope="+url.QueryEscape(strings.Join(declared, ",")))
	require.Empty(t, q.Get("user_scope"), "reliant acts as the bot; it asks for no user token")
	require.Equal(t, "1234.5678", q.Get("client_id"))
	require.Equal(t, "https://reliant.example/integrations/oauth/slack/callback", q.Get("redirect_uri"))
}

// The code exchange answers with the bot token at the top level and a user
// token nested under authed_user. The connection stores and uses the bot
// token, and is the WORKSPACE: its external account is auth.test's team_id,
// which is what Slack events route on.
func TestSlackOAuth_ExchangeKeepsTheBotTokenAndRecordsTheTeam(t *testing.T) {
	e, f := slackEnv(t)
	ctx := context.Background()
	done := e.connectSlack("alice")
	conn := done.Connection

	form := f.lastForm()
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "slack-code", form.Get("code"))
	require.Equal(t, "1234.5678", form.Get("client_id"))
	require.Equal(t, "slack-client-secret-canary", form.Get("client_secret"))
	require.Equal(t, "https://reliant.example/integrations/oauth/slack/callback", form.Get("redirect_uri"))

	require.Equal(t, "Bearer xoxb-bot-1", f.lastProbeAuth(), "the identity probe runs as the bot")
	require.Equal(t, "oauth2", conn.AuthKind)
	require.NotNil(t, conn.ExternalAccountID)
	require.Equal(t, "T0ACME", *conn.ExternalAccountID, "external_account_id is the Slack team_id")
	require.Equal(t, "Acme Corp", *conn.AccountLabel)
	require.Equal(t, "Acme Corp", conn.Name)
	require.Equal(t, []string{"app_mentions:read", "channels:history", "chat:write"}, conn.Scopes)
	require.Nil(t, conn.AccessExpiresAt, "a bot token without rotation does not expire")

	// The resolved credential is the bot token, never the nested user token.
	run := e.newRun("alice")
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{IntegrationID: "slack"})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodPost, "https://slack.com/api/chat.postMessage", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "Bearer xoxb-bot-1", req.Header.Get("Authorization"))
	require.Zero(t, e.count(`SELECT count(*) FROM connection_secrets WHERE connection_id=$1 AND field='refresh_token'`, conn.ID))

	// TestConnection re-probes as the bot.
	res, err := e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Zero(t, f.refreshCalls.Load())
}

// With token rotation on, Slack issues a 12-hour token plus a refresh token.
// The refresh presents the stored bot refresh token (not the user's) and
// sends the granted scopes comma-joined, as the manifest declares.
func TestSlackOAuth_RotatingTokenRefreshesWithCommaScopes(t *testing.T) {
	e, f := slackEnv(t)
	f.rotate = true
	ctx := context.Background()
	conn := e.connectSlack("alice").Connection
	require.NotNil(t, conn.AccessExpiresAt)
	require.WithinDuration(t, time.Now().Add(12*time.Hour), *conn.AccessExpiresAt, time.Minute)

	e.expireAccessToken(conn.ID)
	tok, err := e.tokens.Token(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, int32(1), f.refreshCalls.Load())
	_ = tok.Use(func(b []byte) error {
		require.Equal(t, "xoxe.xoxb-bot-2", string(b))
		return nil
	})

	form := f.lastForm()
	require.Equal(t, "refresh_token", form.Get("grant_type"))
	require.Equal(t, "xoxe-1-refresh-1", form.Get("refresh_token"), "the bot's refresh token, not authed_user's")
	require.Equal(t, "1234.5678", form.Get("client_id"))
	require.Equal(t, "slack-client-secret-canary", form.Get("client_secret"))
	require.Equal(t, slackGranted, form.Get("scope"), "refresh presents the granted scopes, comma-joined")

	// The rotated refresh token is the one presented next.
	e.expireAccessToken(conn.ID)
	e.tokens.Forget(conn.ID)
	_, err = e.tokens.Token(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, "xoxe-1-refresh-2", f.lastForm().Get("refresh_token"))
}

// Slack reports a dead refresh token as HTTP 200 {"ok":false,
// "error":"invalid_refresh_token"}. That is a dead grant: reconnect.
func TestSlackOAuth_DeadRefreshTokenNeedsReconnect(t *testing.T) {
	e, f := slackEnv(t)
	f.rotate = true
	conn := e.connectSlack("alice").Connection
	e.expireAccessToken(conn.ID)
	f.refreshError = "invalid_refresh_token"
	_, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, "needs_reauth", mustStatus(t, e, conn.ID))
}

// A refused exchange is HTTP 200 with ok:false; it never becomes a connection.
func TestSlackOAuth_RefusedExchangeStoresNothing(t *testing.T) {
	e, f := slackEnv(t)
	f.exchangeError = "invalid_code"
	ctx := context.Background()
	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: "alice", IntegrationID: "slack"})
	require.NoError(t, err)
	_, err = e.svc.CompleteOAuth(ctx, "alice", mustQuery(t, authURL, "state"), "stale-code")
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.count(`SELECT count(*) FROM connections WHERE integration_id='slack'`))
}

// One user, two workspaces: two connections, one per team. Connecting the
// same workspace again updates that one in place.
func TestSlackOAuth_OneConnectionPerWorkspace(t *testing.T) {
	e, f := slackEnv(t)
	first := e.connectSlack("alice").Connection
	again := e.connectSlack("alice").Connection
	require.Equal(t, first.ID, again.ID, "the same workspace dedupes on team_id")

	f.mu.Lock()
	f.teamID, f.teamName = "T0OTHER", "Other Co"
	f.mu.Unlock()
	other := e.connectSlack("alice").Connection
	require.NotEqual(t, first.ID, other.ID)
	require.Equal(t, "T0OTHER", *other.ExternalAccountID)
	require.Equal(t, 2, e.count(`SELECT count(*) FROM connections WHERE integration_id='slack' AND user_id='alice'`))
}
