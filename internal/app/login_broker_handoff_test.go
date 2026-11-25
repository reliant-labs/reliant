package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/pkg/config"
)

// handoffBroker is a broker with server-side sessions in memory, the shape
// reliant-api runs once an identity is configured.
func handoffBroker(t *testing.T, sessions bool) *loginBroker {
	t.Helper()
	b := &loginBroker{
		client:  brokerClient(&config.ApiServerConfig{IdpBase: "http://idp.invalid"}),
		flow:    devidp.FlowConfig{ClientID: "reliant-api", RedirectURI: "https://app.example.com/auth/callback"},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		appBase: "https://app.example.com",
	}
	if sessions {
		m, err := session.NewManager(session.Config{
			Store: session.NewMemoryStore(),
			Refresher: session.RefresherFunc(func(context.Context, string) (session.Tokens, error) {
				return session.Tokens{}, session.ErrRefreshRejected
			}),
		})
		require.NoError(t, err)
		b.sessions = m
	}
	return b
}

func handoffMux(b *loginBroker, redirects string) http.Handler {
	mux := http.NewServeMux()
	b.mountHandoff(devidp.GuardMux(mux, b.appBase), redirects)
	return mux
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func postStart(t *testing.T, h http.Handler, redirect string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"code_challenge":        s256(strings.Repeat("v", 64)),
		"code_challenge_method": "S256",
		"redirect_uri":          redirect,
		"state":                 "state-1",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/auth/handoff/start", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The handoff is served only when handoff_redirects lists a usable redirect
// AND sessions are kept server-side: empty, an entry forge refuses, or no
// session store all leave every handoff route unmounted (404).
func TestHandoffIsServedOnlyWithAnAllowlistAndSessions(t *testing.T) {
	const loopback = "http://127.0.0.1/callback"
	for name, tc := range map[string]struct {
		redirects string
		sessions  bool
	}{
		"empty allowlist":           {redirects: "", sessions: true},
		"localhost is refused":      {redirects: "http://localhost/callback", sessions: true},
		"a wildcard is refused":     {redirects: "https://*.example.com/cb", sessions: true},
		"one bad entry refuses all": {redirects: loopback + ",http://evil.example.com/cb", sessions: true},
		"no server-side sessions":   {redirects: loopback, sessions: false},
	} {
		t.Run(name, func(t *testing.T) {
			h := handoffMux(handoffBroker(t, tc.sessions), tc.redirects)
			require.Equal(t, http.StatusNotFound, postStart(t, h, "http://127.0.0.1:5555/callback").Code)
		})
	}
}

// With the desktop loopback listed, a start naming it on any port is
// accepted and answers the web app's consent page; any other redirect is
// refused without being echoed.
func TestHandoffStartAcceptsOnlyTheListedLoopback(t *testing.T) {
	h := handoffMux(handoffBroker(t, true), "http://127.0.0.1/callback")

	rec := postStart(t, h, "http://127.0.0.1:53124/callback")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.True(t, strings.HasPrefix(started.AuthorizeURL, "https://app.example.com/auth/handoff?req="), started.AuthorizeURL)

	for _, redirect := range []string{
		"http://127.0.0.1:53124/other",
		"http://localhost:53124/callback",
		"https://evil.example.com/callback",
	} {
		rec := postStart(t, h, redirect)
		require.Equal(t, http.StatusBadRequest, rec.Code, redirect)
		require.NotContains(t, rec.Body.String(), redirect, "a refused redirect is never echoed")
	}
}

// The desktop app signs out with its session as a bearer as well as a
// cookie: either way the server-side session ends.
func TestLogoutEndsASessionHeldAsABearer(t *testing.T) {
	b := handoffBroker(t, true)
	issued, err := b.sessions.Create(context.Background(),
		session.Tokens{AccessToken: "at", RefreshToken: "", Expiry: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)},
		session.Meta{Subject: "user-1"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+issued.ID)
	b.handleLogout(httptest.NewRecorder(), req)

	_, err = b.sessions.Info(context.Background(), issued.ID)
	require.ErrorIs(t, err, session.ErrNoSession, "the bearer's session survived sign-out")
}

// RegisterLoginBroker mounts the handoff from config: handoff_redirects set
// serves it, unset does not.
func TestRegisterLoginBrokerMountsTheHandoffFromConfig(t *testing.T) {
	for _, tc := range []struct {
		redirects string
		want      int
	}{{"http://127.0.0.1/callback", http.StatusOK}, {"", http.StatusNotFound}} {
		cfg := &config.ApiServerConfig{
			IdpBase:          "http://idp.invalid",
			IdpLoginUri:      "https://app.example.com/auth/sign-in",
			OidcClientId:     "reliant-api",
			OidcRedirectUri:  "https://app.example.com/auth/callback",
			HandoffRedirects: tc.redirects,
		}
		mux := http.NewServeMux()
		RegisterLoginBroker(mux, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSessions(handoffBroker(t, true).sessions))
		require.Equal(t, tc.want, postStart(t, mux, "http://127.0.0.1:41000/callback").Code, "handoff_redirects=%q", tc.redirects)
	}
}
