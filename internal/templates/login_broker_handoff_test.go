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

// The broker's part of the sign-in handoff: the routes are devidp's
// (handoff_test.go drives them against a fake issuer); the scaffold mounts
// them only when handoff_redirects lists a redirect and sessions are
// server-side, keeps compiling against a config without the field, seals the
// issuer session at every sign-in, and lets a native client sign out with its
// bearer credential.
func TestLoginBrokerScaffoldMountsTheHandoffFromItsAllowlist(t *testing.T) {
	src := renderBroker(t, brokerData(map[string]any{"HandoffRedirectsField": true}))
	for _, want := range []string{
		"broker.mountHandoff(mux, cfg.HandoffRedirects)",
		"func (b *loginBroker) mountHandoff(mux *devidp.GuardedMux, redirects string) {",
		"store, ok := b.sessions.Store().(session.HandoffStore)",
		"session.Meta{Subject: subject, UserAgent: r.UserAgent(), IssuerSession: tokens.IssuerSession.ID}",
		`strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the broker lacks %q", want)
		}
	}
	at := strings.Index(src, "func (b *loginBroker) mountHandoff")
	block := src[at:]
	for _, r := range []string{"HandoffStartPath", "HandoffAuthorizePath", "HandoffConsentPath", "HandoffRedeemPath"} {
		if !strings.Contains(src, r+" ") || !strings.Contains(src, "= devidp."+r) {
			t.Errorf("the broker does not declare %s from devidp", r)
		}
		if !strings.Contains(block, "mux.Handle("+r+",") || strings.Count(src, "mux.Handle("+r+",") != 1 {
			t.Errorf("%s is not mounted exactly once, inside mountHandoff", r)
		}
	}
	// A config without the field still compiles and serves no handoff.
	src = renderBroker(t, brokerData(nil))
	if strings.Contains(src, "cfg.HandoffRedirects") || strings.Contains(src, "func (b *loginBroker) mountHandoff") {
		t.Error("the broker reads handoff_redirects its config does not declare")
	}
}

// TestLoginBrokerScaffoldServesTheHandoffOnlyWhenListed builds the rendered
// broker against this checkout of forge and checks which configurations mount
// the handoff routes.
func TestLoginBrokerScaffoldServesTheHandoffOnlyWhenListed(t *testing.T) {
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
	OidcClientId, OidcRedirectUri, IdpLoginUri, HandoffRedirects string
	AuthSelfSignup bool
}
`)
	write("pkg/middleware/middleware.go", "package middleware\n\nconst SessionCookieName = \"app_session\"\n\nconst SecureSessionCookieName = \"__Host-\" + SessionCookieName\n")
	write("internal/app/login_broker.go", renderBroker(t, brokerData(map[string]any{"HandoffRedirectsField": true,
		"MiddlewareDeclaresSecureSessionCookie": true})))
	write("internal/app/login_broker_handoff_test.go", `package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/session"

	"example.com/app/pkg/config"
)

func serves(mux *http.ServeMux, pattern string) bool {
	method, path, _ := strings.Cut(pattern, " ")
	_, p := mux.Handler(httptest.NewRequest(method, path, nil))
	return p == pattern
}

func TestHandoffMounts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := func(list string) *config.Config {
		return &config.Config{Environment: "production", IdpBase: "http://127.0.0.1:1", IdpBrokerToken: "t",
			OidcClientId: "c", OidcRedirectUri: "https://app.test/cb", IdpLoginUri: "https://app.test/auth/sign-in",
			HandoffRedirects: list}
	}
	// A fixed clock: the bearer session below must not depend on the wall.
	epoch := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	m, err := session.NewManager(session.Config{Store: session.NewMemoryStore(), Logger: logger, Now: func() time.Time { return epoch }})
	if err != nil {
		t.Fatal(err)
	}
	routes := []string{HandoffStartPath, HandoffAuthorizePath, HandoffConsentPath, HandoffRedeemPath}
	for name, tc := range map[string]struct {
		list     string
		sessions bool
		want     bool
	}{
		"empty: off":                     {"", true, false},
		"listed with sessions: on":       {"http://127.0.0.1/callback, com.app.desktop:/cb", true, true},
		"listed without sessions: off":   {"http://127.0.0.1/callback", false, false},
		"one bad entry turns it all off": {"http://127.0.0.1/callback,https://*.app.test/cb", true, false},
	} {
		mux := http.NewServeMux()
		var opts []BrokerOption
		if tc.sessions {
			opts = append(opts, WithSessions(m))
		}
		RegisterLoginBroker(mux, cfg(tc.list), logger, opts...)
		for _, r := range routes {
			if serves(mux, r) != tc.want {
				t.Errorf("%s: %s served = %v, want %v", name, r, !tc.want, tc.want)
			}
		}
	}

	// A native client signs out with its bearer credential.
	issued, err := m.Create(context.Background(), session.Tokens{AccessToken: "at", Expiry: epoch.Add(time.Hour)}, session.Meta{Subject: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterLoginBroker(mux, cfg("http://127.0.0.1/callback"), logger, WithSessions(m))
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+issued.ID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if _, err := m.Info(context.Background(), issued.ID); err != session.ErrNoSession {
		t.Errorf("a bearer sign-out left the session: %v", err)
	}
}
`)
	cmd := exec.Command("go", "test", "-count=1", "./internal/app/")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the rendered broker does not serve the handoff as configured:\n%s", out)
	}
}
