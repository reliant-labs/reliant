package devidp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// tokenEndpoint is a stub issuer's /oauth/v2/token and /oauth/v2/revoke.
type tokenEndpoint struct {
	mu      sync.Mutex
	forms   []url.Values
	paths   []string
	status  int
	body    string
	revoked []string
}

func (e *tokenEndpoint) serve(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		e.mu.Lock()
		defer e.mu.Unlock()
		e.forms = append(e.forms, form)
		e.paths = append(e.paths, r.URL.Path)
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if r.URL.Path == "/oauth/v2/revoke" {
			e.revoked = append(e.revoked, form.Get("token"))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.status)
		_, _ = io.WriteString(w, e.body)
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL}
}

func TestRefreshRedeemsTheRefreshToken(t *testing.T) {
	e := &tokenEndpoint{status: 200, body: `{"access_token":"at-2","refresh_token":"rt-2","id_token":"it-2","token_type":"Bearer","expires_in":3600}`}
	c := e.serve(t)
	cfg := FlowConfig{ClientID: "client-1", RedirectURI: "https://app.test/cb"}
	got, err := c.Refresh(context.Background(), cfg, "rt-1")
	if err != nil || got.AccessToken != "at-2" || got.RefreshToken != "rt-2" || got.ExpiresIn != 3600 {
		t.Fatalf("Refresh = %+v, %v", got, err)
	}
	f := e.forms[0]
	if e.paths[0] != "/oauth/v2/token" || f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt-1" ||
		f.Get("client_id") != "client-1" || f.Has("client_secret") {
		t.Errorf("refresh request = %s %v", e.paths[0], f)
	}
	if _, err := c.Refresh(context.Background(), cfg, " "); err == nil {
		t.Error("Refresh with no token asked the issuer")
	}
}

func TestRefreshTellsARefusalFromAnOutage(t *testing.T) {
	cfg := FlowConfig{ClientID: "c"}
	refused := (&tokenEndpoint{status: 400, body: `{"error":"invalid_grant","error_description":"refresh token invalid"}`}).serve(t)
	_, err := refused.Refresh(context.Background(), cfg, "rt")
	if !errors.Is(err, ErrRefreshRejected) || !errors.Is(err, session.ErrRefreshRejected) {
		t.Errorf("invalid_grant: %v, want ErrRefreshRejected (and session's)", err)
	}
	for name, e := range map[string]*tokenEndpoint{
		"server error":   {status: 500, body: `oops`},
		"invalid_client": {status: 401, body: `{"error":"invalid_client"}`},
		"no token":       {status: 200, body: `{"token_type":"Bearer"}`},
	} {
		_, err := e.serve(t).Refresh(context.Background(), cfg, "rt")
		if err == nil || errors.Is(err, session.ErrRefreshRejected) {
			t.Errorf("%s: %v, want an error that does NOT end the session", name, err)
		}
	}
}

func TestRevokeToken(t *testing.T) {
	e := &tokenEndpoint{status: 200}
	c := e.serve(t)
	if err := c.RevokeToken(context.Background(), FlowConfig{ClientID: "c"}, "rt-9", "refresh_token"); err != nil {
		t.Fatal(err)
	}
	f := e.forms[0]
	if e.paths[0] != "/oauth/v2/revoke" || f.Get("token") != "rt-9" || f.Get("token_type_hint") != "refresh_token" || f.Get("client_id") != "c" {
		t.Errorf("revoke request = %s %v", e.paths[0], f)
	}
	if err := c.RevokeToken(context.Background(), FlowConfig{ClientID: "c"}, "", ""); err != nil || len(e.forms) != 1 {
		t.Errorf("revoking nothing asked the issuer: %v", err)
	}
}

func TestSessionTokensExpiry(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	jwt := func(exp int64) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":`+strconv.FormatInt(exp, 10)+`}`)) + ".sig"
	}
	for name, tc := range map[string]struct {
		in   Tokens
		want time.Time
	}{
		"expires_in":          {Tokens{AccessToken: jwt(now.Add(time.Minute).Unix()), ExpiresIn: 3600}, now.Add(time.Hour)},
		"exp claim":           {Tokens{AccessToken: jwt(now.Add(30 * time.Minute).Unix())}, now.Add(30 * time.Minute)},
		"opaque, unknown":     {Tokens{AccessToken: "opaque"}, time.Time{}},
		"malformed exp claim": {Tokens{AccessToken: "e30.bm90LWpzb24.sig"}, time.Time{}},
	} {
		got := SessionTokens(tc.in, now)
		if !got.Expiry.Equal(tc.want) || got.AccessToken != tc.in.AccessToken {
			t.Errorf("%s: %+v, want expiry %v", name, got.Expiry, tc.want)
		}
	}
	got := SessionTokens(Tokens{AccessToken: "a", RefreshToken: "r", IDToken: "i", ExpiresIn: 60}, now)
	if got.RefreshToken != "r" || got.IDToken != "i" {
		t.Errorf("tokens not carried: %+v", got)
	}
}

func TestSessionRefresherDrivesAManager(t *testing.T) {
	e := &tokenEndpoint{status: 200, body: `{"access_token":"at-2","expires_in":3600}`}
	c := e.serve(t)
	now := time.Now()
	m, err := session.NewManager(session.Config{Store: session.NewMemoryStore(), Refresher: c.SessionRefresher(FlowConfig{ClientID: "c"}),
		Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Create(context.Background(), session.Tokens{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: now.Add(time.Minute)}, session.Meta{Subject: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := m.AccessToken(context.Background(), s.ID); err != nil || tok != "at-2" {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	if e.forms[0].Get("refresh_token") != "rt-1" {
		t.Errorf("refreshed with %v", e.forms[0])
	}
}

// A sign-in whose session cannot be stored answers 502 and sets no cookie,
// rather than answering 200 with nothing behind it.
func TestSignedInRequestFailureIssuesNoSession(t *testing.T) {
	var stored int
	h := newHarness(t, policyWith(SignupInvite, allMethods), func(c *SignInRoutesConfig) {
		c.SignedIn = nil
		c.SignedInRequest = func(w http.ResponseWriter, req *http.Request, tokens Tokens) (any, error) {
			if req.Header.Get("User-Agent") == "" {
				t.Error("SignedInRequest got no request")
			}
			stored++
			return nil, errors.New("session store unreachable")
		}
	})
	u := invitee(h)
	b := h.browser()
	status, _ := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "a-long-password"})
	if status != http.StatusBadGateway || stored != 1 || b.signedIn() != "" {
		t.Fatalf("accept with an unreachable session store: %d, stored %d, cookie %q; want 502 and no session", status, stored, b.signedIn())
	}

	h = newHarness(t, policyWith(SignupInvite, allMethods), func(c *SignInRoutesConfig) {
		c.SignedIn = nil
		c.SignedInRequest = func(w http.ResponseWriter, _ *http.Request, tokens Tokens) (any, error) {
			http.SetCookie(w, &http.Cookie{Name: "app_session", Value: tokens.AccessToken, Path: "/"})
			return map[string]any{"subject": "via-request"}, nil
		}
	})
	u = invitee(h)
	b = h.browser()
	status, body := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "a-long-password"})
	if status != 200 || body["subject"] != "via-request" || b.signedIn() == "" {
		t.Fatalf("accept through SignedInRequest: %d %v", status, body)
	}
}

func TestNewSignInRoutesNeedsOneSignedInCallback(t *testing.T) {
	cfg := SignInRoutesConfig{Client: &Client{}, AppBase: testAppBase, SessionToken: func(*http.Request) string { return "" }}
	if _, err := NewSignInRoutes(cfg); err == nil {
		t.Error("accepted neither SignedIn nor SignedInRequest")
	}
	cfg.SignedInRequest = func(http.ResponseWriter, *http.Request, Tokens) (any, error) { return nil, nil }
	if _, err := NewSignInRoutes(cfg); err != nil {
		t.Errorf("SignedInRequest alone: %v", err)
	}
}
