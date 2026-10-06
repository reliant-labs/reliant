// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

func newMux(e *env) *http.ServeMux {
	mux := http.NewServeMux()
	connections.NewOAuthHTTP(e.broker).Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	return mux
}

// do sends a cookie-less request, which is what the callback receives in
// production: the web app is a different site from the API, and on the
// desktop the consent ran in the system browser, so no cookie of ours is ever
// there.
func do(mux http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func startAlice(t *testing.T, e *env, p connections.StartParams) string {
	t.Helper()
	p.UserID, p.IntegrationID = "alice", "github"
	if p.ClientOrigin == "" && p.LoopbackRedirect == "" {
		p.ClientOrigin = testAppOrigin
	}
	authURL, err := e.svc.StartOAuth(context.Background(), p)
	require.NoError(t, err)
	return mustQuery(t, authURL, "state")
}

// The whole browser flow, with no cookie anywhere: the callback relays the
// code to the web app's callback route on the app's own origin (absolute, and
// a different site from the API), and the signed-in app finishes it.
func TestHTTP_CrossSiteFlowCompletesWithoutCookies(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e)
	state := startAlice(t, e, connections.StartParams{Name: "work", RedirectAfter: "/workflow/builder?x=1"})

	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusFound, cb.Code)
	require.Empty(t, cb.Result().Cookies(), "the flow must not depend on a cookie")
	require.Equal(t, "no-referrer", cb.Header().Get("Referrer-Policy"))
	require.Equal(t, "no-store", cb.Header().Get("Cache-Control"))

	landing, err := url.Parse(cb.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https", landing.Scheme)
	require.Equal(t, "app.reliant.example", landing.Host, "the browser lands on the app, not the API")
	require.Equal(t, connections.AppCallbackPath, landing.Path)
	require.Equal(t, "abc", landing.Query().Get("code"))
	require.Equal(t, state, landing.Query().Get("state"))
	require.Equal(t, "/workflow/builder?x=1", landing.Query().Get("redirect_after"))

	require.Zero(t, e.gh.tokenCalls.Load(), "the callback relays; it exchanges nothing")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))

	done, err := e.svc.CompleteOAuth(context.Background(), "alice", landing.Query().Get("state"), landing.Query().Get("code"))
	require.NoError(t, err)
	require.Equal(t, "work", done.Connection.Name)
	require.Equal(t, "/workflow/builder?x=1", done.RedirectAfter)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE user_id='alice'`))
}

// The desktop app's consent runs in the system browser; the callback relays
// to the app's loopback receiver, which only ever reaches the user's machine.
func TestHTTP_CallbackRelaysToDesktopLoopback(t *testing.T) {
	e := newEnv(t)
	state := startAlice(t, e, connections.StartParams{LoopbackRedirect: "http://127.0.0.1:49152/callback"})

	cb := do(newMux(e), http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusFound, cb.Code)
	landing, err := url.Parse(cb.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:49152/callback", landing.Scheme+"://"+landing.Host+landing.Path)
	require.Equal(t, "abc", landing.Query().Get("code"))

	_, err = e.svc.CompleteOAuth(context.Background(), "alice", state, "abc")
	require.NoError(t, err)
}

// A relayed code is useless to anyone but the user who started the flow. This
// is the attack the old cookie guarded against, now closed by the user binding:
// the victim's code reaches the attacker (or the attacker's reaches the
// victim's session), and finishing it as the wrong user fails and burns it.
func TestHTTP_RelayedCodeCannotBeFinishedByAnotherUser(t *testing.T) {
	e := newEnv(t)
	state := startAlice(t, e, connections.StartParams{})
	cb := do(newMux(e), http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+url.QueryEscape(state))
	landing, _ := url.Parse(cb.Header().Get("Location"))

	_, err := e.svc.CompleteOAuth(context.Background(), "mallory", landing.Query().Get("state"), landing.Query().Get("code"))
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.gh.tokenCalls.Load())

	_, err = e.svc.CompleteOAuth(context.Background(), "alice", state, "abc")
	require.ErrorIs(t, err, connections.ErrFailedPrecondition, "the attempt burned the flow")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

// With no flow there is nowhere safe to send the browser, so the callback
// answers with a page and never redirects.
func TestHTTP_CallbackForUnknownOrExpiredStateRendersAPage(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e)
	for _, target := range []string{
		"/integrations/oauth/github/callback?code=abc&state=never-issued",
		"/integrations/oauth/github/callback?code=abc",
	} {
		rec := do(mux, http.MethodGet, target)
		require.Equal(t, http.StatusBadRequest, rec.Code, target)
		require.Empty(t, rec.Header().Get("Location"), target)
		require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	}

	state := startAlice(t, e, connections.StartParams{})
	_, err := e.raw.Exec(`UPDATE oauth_flows SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	rec := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=<script>x</script>&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))
	body, _ := io.ReadAll(rec.Body)
	require.Contains(t, string(body), "expired")
	require.NotContains(t, string(body), "<script>", "nothing from the request is echoed")
}

func TestHTTP_CallbackForAnotherIntegrationIsRefused(t *testing.T) {
	e := newEnv(t)
	state := startAlice(t, e, connections.StartParams{})
	rec := do(newMux(e), http.MethodGet, "/integrations/oauth/slack/callback?code=abc&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))
}

func TestHTTP_ProviderErrorBurnsStateAndNeverEchoes(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e)
	state := startAlice(t, e, connections.StartParams{RedirectAfter: "/back"})

	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?error=access_denied&error_description=<script>&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusFound, cb.Code)
	landing, err := url.Parse(cb.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, testAppOrigin+connections.AppCallbackPath, landing.Scheme+"://"+landing.Host+landing.Path)
	require.Equal(t, "denied", landing.Query().Get("error"))
	require.Equal(t, "/back", landing.Query().Get("redirect_after"))
	require.Empty(t, landing.Query().Get("code"))
	require.NotContains(t, cb.Header().Get("Location"), "script")

	again := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusBadRequest, again.Code, "a refused flow cannot be replayed")
	_, err = e.svc.CompleteOAuth(context.Background(), "alice", state, "abc")
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

// A flow starts only through the authenticated StartOAuth RPC. There is no
// browser start route to reach with a cross-site fetch or a forged link.
func TestHTTP_ThereIsNoStartRoute(t *testing.T) {
	e := newEnv(t)
	rec := do(newMux(e), http.MethodGet, "/integrations/oauth/github/start?mode=json&redirect_after=/")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))
}
