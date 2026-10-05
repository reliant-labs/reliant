// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

// asUser stands in for the auth middleware: it puts userID on the context the
// way RequireAuth does for a verified token.
func asUser(userID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if userID == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.UserIDContextKey, userID)))
		})
	}
}

func newMux(e *env, userID string) *http.ServeMux {
	mux := http.NewServeMux()
	connections.NewOAuthHTTP(e.broker, asUser(userID), "https://reliant.example").Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	return mux
}

func do(mux http.Handler, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHTTP_StartRequiresAuth(t *testing.T) {
	e := newEnv(t)
	rec := do(newMux(e, ""), http.MethodGet, "/integrations/oauth/github/start")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))
}

func TestHTTP_FullFlowWithCookieBinding(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e, "alice")

	start := do(mux, http.MethodGet, "/integrations/oauth/github/start?name=work&redirect_after=/settings/connections")
	require.Equal(t, http.StatusFound, start.Code)
	loc, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "github.com", loc.Host)
	state := loc.Query().Get("state")

	var binder *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "__Host-reliant_oauth_github" {
			binder = c
		}
	}
	require.NotNil(t, binder, "https deployments bind with a __Host- cookie")
	require.True(t, binder.HttpOnly)
	require.True(t, binder.Secure)
	require.Equal(t, http.SameSiteLaxMode, binder.SameSite)
	require.Equal(t, "/", binder.Path)
	require.Empty(t, binder.Domain, "__Host- cookies must not set Domain")

	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+state, binder)
	require.Equal(t, http.StatusFound, cb.Code)
	landing, _ := url.Parse(cb.Header().Get("Location"))
	require.Equal(t, "/settings/connections", landing.Path)
	require.NotEmpty(t, landing.Query().Get("connection"))
	require.Empty(t, landing.Query().Get("connection_error"))
	require.NotContains(t, cb.Header().Get("Location"), "abc")
	require.NotContains(t, cb.Header().Get("Location"), state)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE user_id='alice'`))

	// The callback clears the binder.
	var cleared bool
	for _, c := range cb.Result().Cookies() {
		if c.Name == binder.Name && c.MaxAge < 0 {
			cleared = true
		}
	}
	require.True(t, cleared)
}

func TestHTTP_CallbackWithoutCookieIsRejected(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e, "alice")
	start := do(mux, http.MethodGet, "/integrations/oauth/github/start")
	state := mustQuery(t, start.Header().Get("Location"), "state")

	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+state) // attacker's browser: no cookie
	require.Equal(t, http.StatusFound, cb.Code)
	require.Contains(t, cb.Header().Get("Location"), "connection_error=session")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
	require.Zero(t, e.gh.tokenCalls.Load())
}

func TestHTTP_CallbackWithAnotherBrowsersCookieIsRejected(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e, "alice")
	start := do(mux, http.MethodGet, "/integrations/oauth/github/start")
	state := mustQuery(t, start.Header().Get("Location"), "state")
	other := do(mux, http.MethodGet, "/integrations/oauth/github/start") // attacker starts their own flow
	var attackerCookie *http.Cookie
	for _, c := range other.Result().Cookies() {
		attackerCookie = c
	}
	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+state, attackerCookie)
	require.Contains(t, cb.Header().Get("Location"), "connection_error=")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
	require.Zero(t, e.gh.tokenCalls.Load())
}

func TestHTTP_ProviderErrorBurnsStateAndNeverEchoes(t *testing.T) {
	e := newEnv(t)
	mux := newMux(e, "alice")
	start := do(mux, http.MethodGet, "/integrations/oauth/github/start?redirect_after=/back")
	state := mustQuery(t, start.Header().Get("Location"), "state")
	cookie := start.Result().Cookies()[0]

	cb := do(mux, http.MethodGet, "/integrations/oauth/github/callback?error=access_denied&error_description=<script>&state="+state, cookie)
	loc := cb.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, "/back?"), loc)
	require.Contains(t, loc, "connection_error=denied")
	require.NotContains(t, loc, "script")

	again := do(mux, http.MethodGet, "/integrations/oauth/github/callback?code=abc&state="+state, cookie)
	require.Contains(t, again.Header().Get("Location"), "connection_error=")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

func TestHTTP_StartRejectsExternalRedirectAfter(t *testing.T) {
	e := newEnv(t)
	rec := do(newMux(e, "alice"), http.MethodGet, "/integrations/oauth/github/start?redirect_after=https://evil.example/")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows`))
}

func TestHTTP_UnknownProvider(t *testing.T) {
	e := newEnv(t)
	rec := do(newMux(e, "alice"), http.MethodGet, "/integrations/oauth/nope/start")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHTTP_PlainHTTPDeploymentUsesPlainCookie(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	connections.NewOAuthHTTP(e.broker, asUser("alice"), "http://localhost:8080").Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	rec := do(mux, http.MethodGet, "/integrations/oauth/github/start")
	require.Equal(t, http.StatusFound, rec.Code)
	c := rec.Result().Cookies()[0]
	require.Equal(t, "reliant_oauth_github", c.Name, "__Host- requires Secure, which a plain-http deployment cannot set")
	require.False(t, c.Secure)
}
