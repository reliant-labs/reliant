package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/auth/authtest"
)

const resolveSecret = "internal-service-secret-at-least-32-bytes"

// The fixed clock the sessions run on: no assertion depends on today. The
// identity's tokens are valid until 2100 (authtest), so they verify on the
// real clock too.
var resolveNow = time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

type resolveHarness struct {
	handler   http.Handler
	sessions  *session.Manager
	id        *authtest.Identity
	supa      *authtest.Supabase
	refreshes atomic.Int32
}

func newResolveHarness(t *testing.T) *resolveHarness {
	t.Helper()
	h := &resolveHarness{id: authtest.NewIdentity(t), supa: authtest.NewSupabase(t)}
	var err error
	h.sessions, err = session.NewManager(session.Config{
		Store: session.NewMemoryStore(),
		Now:   func() time.Time { return resolveNow },
		Refresher: session.RefresherFunc(func(_ context.Context, refresh string) (session.Tokens, error) {
			h.refreshes.Add(1)
			// The rotated tokens: a fresh access token for the same person.
			return session.Tokens{
				AccessToken:  h.id.Sign(t, strings.TrimPrefix(refresh, "refresh-"), map[string]any{"jti": "refreshed"}),
				RefreshToken: refresh + "-rotated",
				Expiry:       resolveNow.Add(time.Hour),
			}, nil
		}),
	})
	require.NoError(t, err)
	authenticator, err := auth.NewAuthenticator(context.Background(), auth.Settings{
		PublicKeyPEM: h.supa.PublicKeyPEM,
		Identity:     auth.IdentitySettings{Issuer: h.id.Issuer, Audience: h.id.Audience, JWKSURL: h.id.JWKSURL},
		Sessions:     h.sessions,
	})
	require.NoError(t, err)
	h.handler = NewSessionResolveHandler(h.sessions, authenticator, resolveSecret)
	return h
}

// signIn is what the login broker does; expiry is the access token's.
func (h *resolveHarness) signIn(t *testing.T, sub string, expiry time.Time) (id, token string) {
	t.Helper()
	token = h.id.Sign(t, sub, nil)
	issued, err := h.sessions.Create(context.Background(), session.Tokens{
		AccessToken: token, RefreshToken: "refresh-" + sub, Expiry: expiry,
	}, session.Meta{Subject: sub})
	require.NoError(t, err)
	return issued.ID, token
}

// serviceToken is control-plane's internal-service JWT, as
// auth.SignInternalServiceToken (and control-plane's own signer) mints it.
func serviceToken(t *testing.T, secret string) string {
	t.Helper()
	tok, err := auth.SignInternalServiceToken(secret)
	require.NoError(t, err)
	return tok
}

func (h *resolveHarness) post(t *testing.T, header http.Header, sessionID string) (int, sessionResolveResponse) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"session_id": sessionID})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, SessionResolvePath, bytes.NewReader(body))
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out sessionResolveResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec.Code, out
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

func TestSessionResolverHappyPath(t *testing.T) {
	h := newResolveHarness(t)
	sid, token := h.signIn(t, "user-1", resolveNow.Add(time.Hour))
	code, out := h.post(t, bearer(serviceToken(t, resolveSecret)), sid)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, token, out.AccessToken)
	require.Equal(t, "user-1", out.Subject)
	require.Zero(t, h.refreshes.Load(), "a token far from expiry is not renewed")
}

// A session whose access token is about to expire is renewed first: the
// answer is the fresh token, never the one about to die.
func TestSessionResolverRefreshesANearlyExpiredSession(t *testing.T) {
	h := newResolveHarness(t)
	sid, old := h.signIn(t, "user-1", resolveNow.Add(30*time.Second))
	code, out := h.post(t, bearer(serviceToken(t, resolveSecret)), sid)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, int32(1), h.refreshes.Load())
	require.NotEqual(t, old, out.AccessToken)
	require.Equal(t, "user-1", out.Subject)
}

func TestSessionResolverUnknownSessionIsNotFound(t *testing.T) {
	h := newResolveHarness(t)
	unknown, err := session.NewID()
	require.NoError(t, err)
	for name, id := range map[string]string{"never issued": unknown, "not a session id": "hello", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			code, _ := h.post(t, bearer(serviceToken(t, resolveSecret)), id)
			require.Equal(t, http.StatusNotFound, code)
		})
	}
}

func TestSessionResolverRevokedSessionIsNotFound(t *testing.T) {
	h := newResolveHarness(t)
	sid, _ := h.signIn(t, "user-1", resolveNow.Add(time.Hour))
	code, _ := h.post(t, bearer(serviceToken(t, resolveSecret)), sid)
	require.Equal(t, http.StatusOK, code)
	_, _, err := h.sessions.Revoke(context.Background(), sid)
	require.NoError(t, err)
	code, _ = h.post(t, bearer(serviceToken(t, resolveSecret)), sid)
	require.Equal(t, http.StatusNotFound, code)
}

// Only control-plane's internal-service JWT calls it. Every credential a
// user — or software holding a user's credential — can present is refused,
// and nothing about the session is revealed: not even whether it exists.
func TestSessionResolverRefusesUserCredentials(t *testing.T) {
	h := newResolveHarness(t)
	sid, token := h.signIn(t, "user-1", resolveNow.Add(time.Hour))
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": auth.InternalServiceSubject, "role": auth.InternalServiceRole, "iss": "control-plane", "aud": "authenticated",
		"iat": time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
		"exp": time.Date(2020, time.January, 1, 0, 4, 0, 0, time.UTC).Unix(),
	})
	expiredToken, err := expired.SignedString([]byte(resolveSecret))
	require.NoError(t, err)
	asUser := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-1", "role": "authenticated", "iss": "control-plane", "aud": "authenticated",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), // the service-token check reads the wall clock (iat within 5m)
	})
	userSigned, err := asUser.SignedString([]byte(resolveSecret))
	require.NoError(t, err)

	for name, header := range map[string]http.Header{
		"no credential":                        {},
		"the identity's access token":          bearer(token),
		"a Supabase session":                   bearer(h.supa.Sign(t, "user-1", nil)),
		"an rlat_ access token":                bearer(fat.Prefix + strings.Repeat("A1b2", 8)),
		"the session id itself":                bearer(sid),
		"the session cookie":                   {"Cookie": {auth.SecureSessionCookieName + "=" + sid}},
		"an internal-service token, wrong key": bearer(serviceToken(t, "another-secret-of-at-least-32-bytes!")),
		"an expired internal-service token":    bearer(expiredToken),
		"the secret's key, a user's subject":   bearer(userSigned),
		"the secret itself":                    bearer(resolveSecret),
	} {
		t.Run(name, func(t *testing.T) {
			code, out := h.post(t, header, sid)
			require.Equal(t, http.StatusUnauthorized, code)
			require.Empty(t, out.AccessToken)
		})
	}

	t.Run("not a POST", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, SessionResolvePath+"?session_id="+sid, nil)
		req.Header = bearer(serviceToken(t, resolveSecret))
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
	t.Run("no secret configured: closed", func(t *testing.T) {
		closed := NewSessionResolveHandler(h.sessions, nil, "")
		req := httptest.NewRequest(http.MethodPost, SessionResolvePath, strings.NewReader(`{"session_id":"`+sid+`"}`))
		req.Header = bearer(serviceToken(t, resolveSecret))
		rec := httptest.NewRecorder()
		closed.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// A session holding something that does not verify as the managed
// identity's token for its own subject is never handed out.
func TestSessionResolverVerifiesWhatItHandsOut(t *testing.T) {
	h := newResolveHarness(t)
	for name, tokens := range map[string]session.Tokens{
		"a Supabase token":       {AccessToken: h.supa.Sign(t, "user-1", nil)},
		"another person's token": {AccessToken: h.id.Sign(t, "user-2", nil)},
		"another project's":      {AccessToken: h.id.Sign(t, "user-1", map[string]any{"aud": []string{"other"}})},
		// Never a token with no expiry: it would be valid forever.
		"a token with no exp": {AccessToken: h.id.Sign(t, "user-1", map[string]any{"exp": nil})},
		"a token with exp 0":  {AccessToken: h.id.Sign(t, "user-1", map[string]any{"exp": 0})},
		// The session thinks it is fresh, the token says it has expired.
		"an expired token": {AccessToken: h.id.Sign(t, "user-1", map[string]any{"exp": time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()})},
	} {
		t.Run(name, func(t *testing.T) {
			tokens.Expiry = resolveNow.Add(time.Hour)
			issued, err := h.sessions.Create(context.Background(), tokens, session.Meta{Subject: "user-1"})
			require.NoError(t, err)
			code, out := h.post(t, bearer(serviceToken(t, resolveSecret)), issued.ID)
			require.Equal(t, http.StatusServiceUnavailable, code)
			require.Empty(t, out.AccessToken)
		})
	}
}
