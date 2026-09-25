// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/builddefaults"
)

// ErrAuthNotConfigured is returned when OAuth is attempted without required env vars.
var ErrAuthNotConfigured = fmt.Errorf("auth provider not configured")

func getAuthURL() string {
	return builddefaults.Value("RELIANT_AUTH_URL", builddefaults.AuthURL, "")
}

func getAuthKey() string {
	return builddefaults.Value("RELIANT_AUTH_KEY", builddefaults.AuthKey, "")
}

func requireAuthConfig() (serverURL string, anonKey string, err error) {
	serverURL = getAuthURL()
	anonKey = getAuthKey()
	if serverURL == "" {
		return "", "", fmt.Errorf("%w: RELIANT_AUTH_URL must be set for OAuth login. See docs for auth provider setup", ErrAuthNotConfigured)
	}
	if anonKey == "" {
		return "", "", fmt.Errorf("%w: RELIANT_AUTH_KEY must be set for OAuth login. See docs for auth provider setup", ErrAuthNotConfigured)
	}
	serverURL = strings.TrimRight(serverURL, "/")
	return serverURL, anonKey, nil
}

// LoginResult holds the tokens and user info returned after a successful login.
type LoginResult struct {
	AccessToken   string
	RefreshToken  string
	UserID        string
	Email         string
	ProviderToken string // OAuth provider access token (e.g. GitHub PAT)
}

// LoginWithOAuthProvider performs a direct OAuth PKCE login for a single provider
// using a localhost callback listener and returns the resulting Supabase tokens.
//
// This is the DESKTOP app's own end-user sign-in (SystemService.StartOAuthSignIn,
// driven by Electron): it yields a Supabase session for the renderer, which
// Electron stores encrypted. It is not a CLI credential. The CLI logs in to the
// control plane for an rlat_ access token (internal/cliauth), and nothing in
// this package writes a session file any more.
func LoginWithOAuthProvider(ctx context.Context, provider string) (*LoginResult, error) {
	serverURL, anonKey, err := requireAuthConfig()
	if err != nil {
		return nil, err
	}

	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generating code verifier: %w", err)
	}
	challenge := computeCodeChallenge(verifier)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting callback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)

	type loginEvent struct {
		result *LoginResult
		err    error
	}
	resultCh := make(chan loginEvent, 1)

	authorizeURL, err := buildAuthURL(serverURL, redirectURI, challenge, provider)
	if err != nil {
		return nil, fmt.Errorf("building auth URL for %s: %w", provider, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errMsg := q.Get("error_description"); errMsg != "" {
			resultCh <- loginEvent{err: fmt.Errorf("auth error: %s", errMsg)}
			http.Error(w, errMsg, http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			resultCh <- loginEvent{err: fmt.Errorf("no code in callback")}
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		result, err := exchangeCodeForTokens(ctx, serverURL, anonKey, code, verifier, redirectURI)
		if err != nil {
			resultCh <- loginEvent{err: fmt.Errorf("exchanging code for tokens: %w", err)}
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successHTML)
		resultCh <- loginEvent{result: result}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Printf("Opening browser to log in with %s...\n", provider)
	if err := openBrowser(authorizeURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease visit:\n  %s\n", authorizeURL)
	}

	select {
	case ev := <-resultCh:
		if ev.err != nil {
			return nil, ev.err
		}
		return ev.result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("login cancelled: %w", ctx.Err())
	}
}

func generateCodeVerifier() (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

func computeCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// --- URL builder ---

func buildAuthURL(serverURL, redirectURI, challenge, provider string) (string, error) {
	u, err := url.Parse(serverURL + "/auth/v1/authorize")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("provider", provider)
	q.Set("redirect_to", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	// Request repo scope for GitHub so the provider_token can be used
	// for git operations (clone, push) without a separate OAuth flow.
	if provider == "github" {
		q.Set("scopes", "user:email repo")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// --- Browser opener ---

// openBrowser is a var, not a plain func, so tests can substitute a fake and
// assert it is never called — the same seam internal/auth/oauthcallback uses
// for the same reason (see its openBrowser var).
var openBrowser = defaultOpenBrowser

func defaultOpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

// --- Token exchange ---

// tokenResponse mirrors the Supabase /auth/v1/token JSON response.
type tokenResponse struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int    `json:"expires_in"`
	ProviderToken string `json:"provider_token"`
	User          struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

func exchangeCodeForTokens(ctx context.Context, serverURL, anonKey, code, verifier, redirectURI string) (*LoginResult, error) {
	jsonBody, err := json.Marshal(map[string]string{
		"auth_code":     code,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/auth/v1/token?grant_type=pkce", strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tok tokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	return &LoginResult{
		AccessToken:   tok.AccessToken,
		RefreshToken:  tok.RefreshToken,
		UserID:        tok.User.ID,
		Email:         tok.User.Email,
		ProviderToken: tok.ProviderToken,
	}, nil
}

// --- HTML templates ---

const successHTML = `<!DOCTYPE html>
<html>
<head><title>Reliant – Logged In</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex; justify-content: center; align-items: center; height: 100vh;
    background: #0a0a0a; color: #e5e5e5;
  }
  .card {
    text-align: center; padding: 2.5rem;
    background: #141414; border: 1px solid #282828; border-radius: 12px;
  }
  h1 { font-size: 1.4rem; margin-bottom: 0.4rem; font-weight: 600; color: #34D399; }
  p { color: #888; font-size: 0.9rem; }
</style>
</head>
<body>
  <div class="card">
    <h1>&#10003; Logged in to Reliant</h1>
    <p>You can close this tab and return to your terminal.</p>
  </div>
</body>
</html>`
