// Copyright (c) 2025 Reliant Labs
// Package oauthcallback provides shared OAuth callback handling for both the
// daemon runtime and the standalone `reliant auth serve` HTTP server.
package oauthcallback

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// CallbackConfig describes how to listen for and construct the OAuth redirect URI.
type CallbackConfig struct {
	ListenHost   string
	RedirectHost string
	CallbackPath string
	FixedPort    int // 0 = OS-assigned
}

// Result is the data returned from a successful OAuth callback.
type Result struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	RedirectURI string `json:"redirect_uri"`
	CallbackURL string `json:"callback_url"`
}

var openBrowser = OpenBrowser

// InferConfig inspects the authorize URL to determine callback settings.
func InferConfig(authorizeURLTemplate string) CallbackConfig {
	cfg := CallbackConfig{
		ListenHost:   "127.0.0.1",
		RedirectHost: "localhost",
		CallbackPath: "/auth/callback",
		FixedPort:    0,
	}

	parsed, err := url.Parse(authorizeURLTemplate)
	if err != nil {
		return cfg
	}

	host := strings.ToLower(parsed.Hostname())
	switch {
	case strings.Contains(host, "claude.ai"):
		cfg.RedirectHost = "localhost"
		cfg.CallbackPath = "/callback"
	case strings.Contains(host, "auth.openai.com"):
		cfg.RedirectHost = "localhost"
		cfg.CallbackPath = "/auth/callback"
		cfg.FixedPort = 1455
	}

	return cfg
}

// Run starts a temporary HTTP server, opens the browser, and waits for the
// OAuth callback or timeout. It returns the authorization code, state and
// redirect URI.
func Run(ctx context.Context, authorizeURLTemplate string, timeoutSeconds int) (*Result, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := InferConfig(authorizeURLTemplate)
	listenAddr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.FixedPort)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://%s:%d%s", cfg.RedirectHost, port, cfg.CallbackPath)

	authorizeURL := strings.ReplaceAll(authorizeURLTemplate, "{redirect_uri}", url.QueryEscape(redirectURI))

	callbackCh := make(chan *url.URL, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		callbackCh <- r.URL
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0"><div style="text-align:center"><h2>Authentication Successful</h2><p>You can close this tab and return to Reliant.</p></div></body></html>`)
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener) //nolint:errcheck // best-effort serve in background goroutine
	defer func() { _ = server.Close() }()

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	if err := openBrowser(authorizeURL); err != nil {
		return nil, fmt.Errorf("failed to open browser: %w", err)
	}

	timeoutTimer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timeoutTimer.Stop()

	select {
	case cbURL := <-callbackCh:
		query := cbURL.Query()
		return &Result{
			Code:        query.Get("code"),
			State:       query.Get("state"),
			RedirectURI: redirectURI,
			CallbackURL: cbURL.String(),
		}, nil
	case <-timeoutTimer.C:
		return nil, fmt.Errorf("OAuth callback timed out after %d seconds", timeoutSeconds)
	case <-ctx.Done():
		return nil, fmt.Errorf("OAuth callback cancelled: %w", ctx.Err())
	}
}

// OpenBrowser opens the given URL in the user's default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
