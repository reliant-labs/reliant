// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
)

const (
	defaultSupabaseURL  = "https://dash.reliantlabs.io"
	defaultLoginTimeout = 120 * time.Second
)

// LoginOptions configures the OAuth PKCE login flow.
type LoginOptions struct {
	// ServerURL is the Supabase project URL. Falls back to SUPABASE_URL env var,
	// then to the compiled-in default.
	ServerURL string

	// AnonKey is the Supabase publishable/anon key. Falls back to SUPABASE_ANON_KEY env var.
	AnonKey string

	// Timeout is the maximum time to wait for the browser callback.
	// Defaults to 120s.
	Timeout time.Duration
}

// LoginResult holds the tokens and user info returned after a successful login.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	Email        string
}

type oauthProvider struct {
	name string
	url  string
}

// Login performs login via a local web page that supports email/password and OAuth providers.
func Login(ctx context.Context, opts LoginOptions) (*LoginResult, error) {
	serverURL := firstNonEmpty(opts.ServerURL, defaultSupabaseURL)
	serverURL = strings.TrimRight(serverURL, "/")

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultLoginTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// PKCE parameters (used by OAuth providers).
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generating code verifier: %w", err)
	}
	challenge := computeCodeChallenge(verifier)

	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	// Start a temporary HTTP server on a random port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting callback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)

	// Channel to receive a completed login result (from either OAuth or email/password).
	type loginEvent struct {
		result *LoginResult
		err    error
	}
	resultCh := make(chan loginEvent, 1)

	// Build OAuth authorization URLs.
	var providers []oauthProvider
	for _, p := range []string{"github", "google"} {
		u, err := buildAuthURL(serverURL, redirectURI, challenge, state, p)
		if err != nil {
			return nil, fmt.Errorf("building auth URL for %s: %w", p, err)
		}
		providers = append(providers, oauthProvider{name: p, url: u})
	}

	mux := http.NewServeMux()

	// Login page.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, buildLoginHTML(providers))
	})

	// Email/password sign-in endpoint — calls Supabase REST API directly.
	mux.HandleFunc("/auth/email", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		if email == "" || password == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"Email and password are required"}`))
			return
		}

		result, err := signInWithPassword(ctx, serverURL, opts.AnonKey, email, password)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			msg := err.Error()
			// Make common Supabase errors friendlier.
			if strings.Contains(msg, "Invalid login credentials") {
				msg = "Invalid email or password"
			}
			resp, _ := json.Marshal(map[string]string{"error": msg})
			_, _ = w.Write(resp)
			return
		}

		// Show success page and send result to the CLI.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successHTML)
		resultCh <- loginEvent{result: result}
	})

	// OAuth callback handler.
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != state {
			resultCh <- loginEvent{err: fmt.Errorf("state mismatch: expected %s, got %s", state, got)}
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
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

		// Exchange code for tokens.
		result, err := exchangeCodeForTokens(ctx, serverURL, opts.AnonKey, code, verifier, redirectURI)
		if err != nil {
			resultCh <- loginEvent{err: fmt.Errorf("exchanging code for tokens: %w", err)}
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successHTML)
		resultCh <- loginEvent{result: result}
	})

	srv := &http.Server{Handler: mux}

	// Serve in background; shut down when ctx is done.
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	// Open the browser to the local login page.
	loginPageURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Printf("Opening browser to log in...\n")
	if err := openBrowser(loginPageURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease visit:\n  %s\n", loginPageURL)
	}

	// Wait for result or timeout.
	select {
	case ev := <-resultCh:
		if ev.err != nil {
			return nil, ev.err
		}
		return ev.result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("login timed out after %s — no callback received", timeout)
	}
}

// --- PKCE helpers ---

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

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- URL builder ---

func buildAuthURL(serverURL, redirectURI, challenge, state, provider string) (string, error) {
	u, err := url.Parse(serverURL + "/auth/v1/authorize")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("provider", provider)
	q.Set("redirect_to", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// --- Browser opener ---

func openBrowser(url string) error {
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
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

// supabaseError mirrors error responses from Supabase auth endpoints.
type supabaseError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Msg              string `json:"msg"`
}

func exchangeCodeForTokens(ctx context.Context, serverURL, anonKey, code, verifier, redirectURI string) (*LoginResult, error) {
	body := url.Values{}
	body.Set("grant_type", "pkce")
	body.Set("auth_code", code)
	body.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/auth/v1/token?grant_type=pkce", strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		UserID:       tok.User.ID,
		Email:        tok.User.Email,
	}, nil
}

// signInWithPassword authenticates with Supabase using email/password (grant_type=password).
func signInWithPassword(ctx context.Context, serverURL, anonKey, email, password string) (*LoginResult, error) {
	payload := map[string]string{
		"email":    email,
		"password": password,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/auth/v1/token?grant_type=password", strings.NewReader(string(jsonBody)))
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
		var sErr supabaseError
		if json.Unmarshal(respBody, &sErr) == nil {
			msg := firstNonEmpty(sErr.ErrorDescription, sErr.Msg, sErr.Error)
			if msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
		}
		return nil, fmt.Errorf("sign in failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tok tokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	return &LoginResult{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		UserID:       tok.User.ID,
		Email:        tok.User.Email,
	}, nil
}

// --- Helpers ---

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- HTML templates ---

func buildLoginHTML(providers []oauthProvider) string {
	var buttons string
	for _, p := range providers {
		icon := providerIcons[p.name]
		label := providerLabels[p.name]
		buttons += fmt.Sprintf(
			`<a href="%s" class="btn oauth-btn">%s %s</a>`,
			p.url, icon, label,
		)
	}
	return fmt.Sprintf(loginHTML, buttons)
}

var providerLabels = map[string]string{
	"google": "Continue with Google",
	"github": "Continue with GitHub",
}

var providerIcons = map[string]string{
	"google": `<svg width="20" height="20" viewBox="0 0 24 24" fill="none"><path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/><path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/><path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05"/><path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/></svg>`,
	"github": `<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M12 1C5.937 1 1 5.937 1 12c0 4.863 3.15 8.987 7.525 10.438.55.1.75-.238.75-.525 0-.263-.013-1.125-.013-2.038-2.762.513-3.475-.675-3.7-1.3-.125-.312-.662-1.3-1.137-1.562-.387-.2-.937-.7-.012-.713.875-.012 1.5.813 1.712 1.15 1 1.687 2.6 1.212 3.238.912.1-.712.388-1.2.712-1.475-2.488-.275-5.088-1.244-5.088-5.525 0-1.225.438-2.225 1.15-3.013-.112-.275-.5-1.425.113-2.975 0 0 .937-.3 3.075 1.15A10.5 10.5 0 0 1 12 6.8c.95 0 1.9.125 2.788.375 2.137-1.462 3.075-1.15 3.075-1.15.612 1.55.225 2.7.112 2.975.713.788 1.15 1.775 1.15 3.013 0 4.3-2.612 5.25-5.1 5.525.4.35.75 1.025.75 2.075 0 1.5-.013 2.713-.013 3.088 0 .287.2.637.75.525C19.85 20.988 23 16.85 23 12c0-6.063-4.938-11-11-11z"/></svg>`,
}

// loginHTML is the login page template. %%s is replaced with OAuth provider buttons.
const loginHTML = `<!DOCTYPE html>
<html>
<head><title>Reliant – Sign In</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex; justify-content: center; align-items: center; min-height: 100vh;
    background: #0a0a0a; color: #e5e5e5;
  }
  .card {
    padding: 2.5rem; width: 380px;
    background: #141414; border: 1px solid #282828; border-radius: 12px;
  }
  h1 { font-size: 1.4rem; margin-bottom: 1.5rem; font-weight: 600; text-align: center; }
  label { display: block; font-size: 0.85rem; font-weight: 500; margin-bottom: 6px; color: #ccc; }
  input[type="email"], input[type="password"] {
    width: 100%%; padding: 10px 12px;
    background: #1a1a1a; border: 1px solid #333; border-radius: 8px;
    color: #e5e5e5; font-size: 0.95rem; outline: none;
    transition: border-color 0.15s;
  }
  input:focus { border-color: #666; }
  .field { margin-bottom: 14px; }
  .submit-btn {
    width: 100%%; padding: 11px 16px; margin-top: 4px;
    background: #fff; color: #000; border: none; border-radius: 8px;
    font-size: 0.95rem; font-weight: 600; cursor: pointer;
    transition: opacity 0.15s;
  }
  .submit-btn:hover { opacity: 0.9; }
  .submit-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .divider {
    display: flex; align-items: center; gap: 12px;
    margin: 20px 0; color: #555; font-size: 0.85rem;
  }
  .divider::before, .divider::after {
    content: ""; flex: 1; height: 1px; background: #333;
  }
  .oauth-btn {
    display: flex; align-items: center; justify-content: center; gap: 10px;
    width: 100%%; padding: 11px 16px; margin-bottom: 10px;
    border: 1px solid #333; border-radius: 8px;
    background: #1a1a1a; color: #e5e5e5;
    font-size: 0.95rem; font-weight: 500;
    text-decoration: none; cursor: pointer;
    transition: background 0.15s, border-color 0.15s;
  }
  .oauth-btn:hover { background: #222; border-color: #555; }
  .oauth-btn:last-child { margin-bottom: 0; }
  .oauth-btn svg { flex-shrink: 0; }
  .error {
    background: #2d1215; border: 1px solid #5c2d2d; border-radius: 8px;
    padding: 10px 12px; margin-bottom: 14px; font-size: 0.85rem; color: #f87171;
    display: none;
  }
  .footer { margin-top: 1.5rem; font-size: 0.78rem; color: #555; text-align: center; }
</style>
</head>
<body>
  <div class="card">
    <h1>Sign in to Reliant</h1>

    <div id="error" class="error"></div>

    <form id="email-form" onsubmit="return handleSubmit(event)">
      <div class="field">
        <label for="email">Email address</label>
        <input type="email" id="email" name="email" placeholder="you@example.com" required autofocus />
      </div>
      <div class="field">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" placeholder="Password" required />
      </div>
      <button type="submit" class="submit-btn" id="submit-btn">Sign In</button>
    </form>

    <div class="divider">or continue with</div>

    %s

    <div class="footer">This page was opened by the Reliant CLI</div>
  </div>

  <script>
    async function handleSubmit(e) {
      e.preventDefault();
      const btn = document.getElementById('submit-btn');
      const errEl = document.getElementById('error');
      errEl.style.display = 'none';
      btn.disabled = true;
      btn.textContent = 'Signing in...';

      try {
        const form = document.getElementById('email-form');
        const data = new URLSearchParams(new FormData(form));
        const resp = await fetch('/auth/email', { method: 'POST', body: data });

        if (!resp.ok) {
          const body = await resp.json().catch(() => ({}));
          throw new Error(body.error || 'Sign in failed');
        }

        // Success — server will show the success page via redirect/replace
        document.open();
        document.write(await resp.text());
        document.close();
      } catch (err) {
        errEl.textContent = err.message;
        errEl.style.display = 'block';
        btn.disabled = false;
        btn.textContent = 'Sign In';
      }
      return false;
    }
  </script>
</body>
</html>`

// successHTML is shown to the user in the browser after a successful login callback.
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
