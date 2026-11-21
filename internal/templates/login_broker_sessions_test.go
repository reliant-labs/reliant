package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/scratchmod"
)

// Server-side sessions are an OPTION of RegisterLoginBroker, so a
// providers.go scaffolded before they existed (which passes none) still
// compiles against a broker scaffolded after, and gets the access-token
// cookie it always had.
func TestLoginBrokerScaffoldTakesSessionsAsAnOption(t *testing.T) {
	src := renderBroker(t, brokerData(map[string]any{"ScopesField": true}))
	for _, want := range []string{
		"func RegisterLoginBroker(appMux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...BrokerOption) {",
		"func WithSessions(m *session.Manager) BrokerOption {",
		"broker.flow.Scopes = sessionScopes(cfg.OidcScopes)",
		"func SessionRefresher(cfg *config.Config) session.Refresher {",
		"SignedInRequest: func(w http.ResponseWriter, r *http.Request, tokens devidp.Tokens) (any, error) {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the broker lacks %q", want)
		}
	}
	if strings.Contains(src, "SCAFFOLD-GRADE") {
		t.Error("the broker still calls its session scaffold-grade")
	}
	// A config with no oidc_scopes still compiles: the standard set.
	if src := renderBroker(t, brokerData(nil)); !strings.Contains(src, `broker.flow.Scopes = sessionScopes("")`) ||
		strings.Contains(src, "cfg.OidcScopes") {
		t.Error("a config without oidc_scopes must not be read for it")
	}
}

// TestLoginBrokerScaffoldKeepsSessionsServerSide builds the rendered broker
// against this checkout of forge and drives it with a session Manager: the
// cookie names a session, not a token; GET /auth/session renews a token about
// to expire; sign-out deletes the session and revokes its refresh token at
// the issuer; and an ended session's cookie is dropped.
func TestLoginBrokerScaffoldKeepsSessionsServerSide(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module with the go tool; runs in task test")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := scratchmod.Write(root, dir, "example.com/app"); err != nil {
		t.Fatal(err)
	}
	write("pkg/config/config.go", `package config

type Config struct {
	Environment, IdpBase, IdpHost, IdpBrokerToken, IdpBrokerTokenFile string
	OidcClientId, OidcRedirectUri, IdpLoginUri, OidcScopes string
	AuthSelfSignup bool
}
`)
	write("pkg/middleware/middleware.go", "package middleware\n\nconst SessionCookieName = \"app_session\"\n\nconst SecureSessionCookieName = \"__Host-\" + SessionCookieName\n")
	write("internal/app/login_broker.go", renderBroker(t, brokerData(map[string]any{"ScopesField": true,
		"MiddlewareDeclaresSecureSessionCookie": true})))
	write("internal/app/login_broker_session_test.go", `package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/session"

	"example.com/app/pkg/config"
)

func jwt(sub string, exp time.Time) string {
	claims, _ := json.Marshal(map[string]any{"sub": sub, "email": sub + "@app.test", "name": "User " + sub, "exp": exp.Unix()})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func TestSessionsAreServerSide(t *testing.T) {
	var mu sync.Mutex
	var refreshed, revoked []string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/oauth/v2/token":
			refreshed = append(refreshed, form.Get("refresh_token"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwt("u1", time.Now().Add(time.Hour)),
				"refresh_token": "rt-2", "expires_in": 3600})
		case "/oauth/v2/revoke":
			revoked = append(revoked, form.Get("token"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()

	cfg := &config.Config{Environment: "production", IdpBase: issuer.URL, IdpBrokerToken: "t", OidcClientId: "c",
		OidcRedirectUri: "https://app.test/cb", IdpLoginUri: "https://app.test/auth/sign-in", OidcScopes: "openid,profile,email"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := session.NewManager(session.Config{Store: session.NewMemoryStore(), Refresher: SessionRefresher(cfg), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterLoginBroker(mux, cfg, logger, WithSessions(m))

	// Sign-in: the cookie holds a session id, never the token.
	b := &loginBroker{sessions: m, client: brokerClient(cfg), logger: logger, secureCookie: true}
	rec := httptest.NewRecorder()
	signIn := httptest.NewRequest("POST", "/auth/login", nil)
	signIn.Header.Set("User-Agent", "test-browser")
	almostExpired := jwt("u1", time.Now().Add(30*time.Second))
	if err := b.setSession(rec, signIn, devidp.Tokens{AccessToken: almostExpired, RefreshToken: "rt-1", IDToken: jwt("u1", time.Now().Add(time.Hour)), ExpiresIn: 30}); err != nil {
		t.Fatal(err)
	}
	cookie := rec.Result().Cookies()[0]
	if cookie.Name != "__Host-app_session" || !session.IsID(cookie.Value) || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" || cookie.MaxAge < 29*24*3600 {
		t.Fatalf("session cookie = %+v", cookie)
	}
	for _, tok := range []string{almostExpired, "rt-1"} {
		if strings.Contains(cookie.Value, tok) {
			t.Fatal("the cookie carries a token")
		}
	}
	list, _ := m.List(context.Background(), "u1")
	if len(list) != 1 || list[0].UserAgent != "test-browser" {
		t.Fatalf("sessions of u1 = %+v", list)
	}

	get := func(path string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", path, nil)
		if path == "/auth/logout" {
			req = httptest.NewRequest("POST", path, nil)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}

	// The token is due: GET /auth/session renews it with the refresh token.
	if _, body := get("/auth/session"); body["authenticated"] != true || body["subject"] != "u1" {
		t.Fatalf("GET /auth/session = %v", body)
	}
	if !reflect.DeepEqual(refreshed, []string{"rt-1"}) {
		t.Fatalf("refreshed with %v, want [rt-1]", refreshed)
	}

	// Sign-out deletes the session and revokes the ROTATED refresh token.
	if rec, _ := get("/auth/logout"); rec.Code != 200 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout: %d %v", rec.Code, rec.Result().Cookies())
	}
	if !reflect.DeepEqual(revoked, []string{"rt-2"}) {
		t.Errorf("revoked %v at the issuer, want [rt-2]", revoked)
	}
	if list, _ := m.List(context.Background(), "u1"); len(list) != 0 {
		t.Errorf("the session survived sign-out: %+v", list)
	}
	// The old cookie is signed out, and the broker tells the browser to drop it.
	rec, body := get("/auth/session")
	if body["authenticated"] != false || len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Errorf("an ended session: %v, cookies %v", body, rec.Result().Cookies())
	}

	if got := sessionScopes("openid, profile,email"); !reflect.DeepEqual(got, []string{"openid", "profile", "email", "offline_access"}) {
		t.Errorf("sessionScopes = %v", got)
	}
	if got := sessionScopes(""); got[len(got)-1] != "offline_access" || got[0] != "openid" {
		t.Errorf("sessionScopes(\"\") = %v", got)
	}
	if got := sessionScopes("openid,offline_access"); len(got) != 2 {
		t.Errorf("offline_access added twice: %v", got)
	}
}

// Without WithSessions the cookie is the access token, as it always was.
func TestNoSessionsKeepsTheTokenCookie(t *testing.T) {
	b := &loginBroker{secureCookie: true}
	rec := httptest.NewRecorder()
	if err := b.setSession(rec, httptest.NewRequest("POST", "/auth/login", nil), devidp.Tokens{AccessToken: "at", ExpiresIn: 60}); err != nil {
		t.Fatal(err)
	}
	if c := rec.Result().Cookies()[0]; c.Value != "at" || c.MaxAge != 60 {
		t.Errorf("cookie = %+v", c)
	}
}
`)
	cmd := exec.Command("go", "test", "-count=1", "./internal/app/")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the rendered broker does not keep sessions server-side:\n%s", out)
	}
}
