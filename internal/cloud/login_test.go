package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeControlPlane plays the control plane's /oauth/authorize (approving
// immediately, as if the user had) and /oauth/token (checking the verifier
// against the recorded challenge).
func fakeControlPlane(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	var req url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/authorize":
			req = r.URL.Query()
			back, _ := url.Parse(req.Get("redirect_uri"))
			back.RawQuery = url.Values{"code": {"c1"}, "state": {req.Get("state")}}.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		case "/oauth/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != req.Get("code_challenge") ||
				r.PostForm.Get("client_id") != ClientID {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "rlat_ABCDEFGHijklmnop", "token_type": "Bearer",
				"expires_in": 3600, "scope": "deploy:read deploy:write",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &req
}

func followBrowser(u string) error {
	go func() {
		if resp, err := http.Get(u); err == nil {
			_ = resp.Body.Close()
		}
	}()
	return nil
}

// TestBrowserLogin_PKCECodeFlow drives the whole interactive flow against
// the control plane's two endpoints, with a fake "browser" that follows the
// redirects the way a real one would.
func TestBrowserLogin_PKCECodeFlow(t *testing.T) {
	cp, req := fakeControlPlane(t)
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: cp.URL})

	cred, err := BrowserLogin{Endpoint: ep, Timeout: 10 * time.Second, Out: io.Discard, OpenURL: followBrowser}.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cred.Token != "rlat_ABCDEFGHijklmnop" || cred.TokenPrefix != "rlat_ABCDEFGH" {
		t.Errorf("credential: %+v", cred)
	}
	if strings.Join(cred.Scopes, " ") != "deploy:read deploy:write" {
		t.Errorf("scopes must be what the server GRANTED; got %v", cred.Scopes)
	}
	if cred.ExpiresAt == nil || time.Until(*cred.ExpiresAt) < 50*time.Minute {
		t.Errorf("expiry from expires_in: %v", cred.ExpiresAt)
	}
	if req.Get("client_id") != ClientID || req.Get("code_challenge_method") != "S256" || req.Get("device") == "" {
		t.Errorf("authorize request: %v", *req)
	}
}

// TestBrowserLogin_TimesOutWithTheCITip — a login that hangs forever
// reads the same as one about to succeed, and the way out for a
// headless machine is --token.
func TestBrowserLogin_TimesOutWithTheCITip(t *testing.T) {
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: "https://api.example.com"})
	_, err := BrowserLogin{
		Endpoint: ep, Timeout: 50 * time.Millisecond, Out: io.Discard,
		OpenURL: func(string) error { return nil },
	}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--token") {
		t.Fatalf("the timeout should point at the non-interactive path; got %v", err)
	}
}

func TestLoginScopes_PinsTheRequestedSet(t *testing.T) {
	want := "deploy:read deploy:write secret:read secret:write domain:read domain:write"
	if got := strings.Join(LoginScopes(), " "); got != want {
		t.Errorf("LoginScopes = %q, want %q", got, want)
	}
}
