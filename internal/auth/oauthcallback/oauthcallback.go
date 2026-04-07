// Package oauthcallback provides shared OAuth callback handling for both the
// daemon runtime and the standalone `reliant auth serve` HTTP server.
package oauthcallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	probePath  = "/__reliant/oauth/probe"
	resultPath = "/__reliant/oauth/result"

	listenerKind    = "reliant-oauth-callback"
	listenerVersion = 1
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

type probeResponse struct {
	Kind         string `json:"kind"`
	Version      int    `json:"version"`
	CallbackPath string `json:"callback_path"`
	RedirectURI  string `json:"redirect_uri"`
	Active       bool   `json:"active"`
}

type callbackServer struct {
	cfg         CallbackConfig
	redirectURI string
	resultCh    chan *Result
}

var openBrowser = OpenBrowser
var reuseHTTPClient = &http.Client{Timeout: 2 * time.Second}

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
// OAuth callback or context cancellation. It returns the authorization code,
// state and redirect URI.
func Run(ctx context.Context, authorizeURLTemplate string) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := InferConfig(authorizeURLTemplate)
	server, authorizeURL, err := newCallbackServer(authorizeURLTemplate, cfg)
	if err != nil {
		return nil, err
	}

	listenAddr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.FixedPort)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if reusedResult, reuseErr := tryReuseExistingListener(ctx, cfg, server.redirectURI, err); reuseErr == nil {
			return reusedResult, nil
		}
		return nil, fmt.Errorf("failed to start callback listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	if cfg.FixedPort == 0 {
		server.redirectURI = fmt.Sprintf("http://%s:%d%s", cfg.RedirectHost, port, cfg.CallbackPath)
		authorizeURL = strings.ReplaceAll(authorizeURLTemplate, "{redirect_uri}", url.QueryEscape(server.redirectURI))
	}

	httpServer := &http.Server{Handler: server.handler(), ReadHeaderTimeout: 10 * time.Second}
	go httpServer.Serve(listener) //nolint:errcheck // best-effort serve in background goroutine
	defer func() { _ = httpServer.Close() }()

	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()

	if err := openBrowser(authorizeURL); err != nil {
		return nil, fmt.Errorf("failed to open browser: %w", err)
	}

	return waitForResult(ctx, server.resultCh)
}

func newCallbackServer(authorizeURLTemplate string, cfg CallbackConfig) (*callbackServer, string, error) {
	redirectPort := cfg.FixedPort
	redirectURI := fmt.Sprintf("http://%s:%d%s", cfg.RedirectHost, redirectPort, cfg.CallbackPath)
	if cfg.FixedPort == 0 {
		redirectURI = fmt.Sprintf("http://%s:%s%s", cfg.RedirectHost, "{port}", cfg.CallbackPath)
	}

	authorizeURL := strings.ReplaceAll(authorizeURLTemplate, "{redirect_uri}", url.QueryEscape(redirectURI))
	server := &callbackServer{
		cfg:         cfg,
		redirectURI: redirectURI,
		resultCh:    make(chan *Result, 1),
	}
	return server, authorizeURL, nil
}

func (s *callbackServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.CallbackPath, s.handleCallback)
	mux.HandleFunc(probePath, s.handleProbe)
	mux.HandleFunc(resultPath, s.handleResult)
	return mux
}

func (s *callbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	result := &Result{
		Code:        r.URL.Query().Get("code"),
		State:       r.URL.Query().Get("state"),
		RedirectURI: s.redirectURI,
		CallbackURL: r.URL.String(),
	}
	select {
	case s.resultCh <- result:
	default:
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0"><div style="text-align:center"><h2>Authentication Successful</h2><p>You can close this tab and return to Reliant.</p></div></body></html>`)
}

func (s *callbackServer) handleProbe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(probeResponse{
		Kind:         listenerKind,
		Version:      listenerVersion,
		CallbackPath: s.cfg.CallbackPath,
		RedirectURI:  s.redirectURI,
		Active:       true,
	})
}

func (s *callbackServer) handleResult(w http.ResponseWriter, r *http.Request) {
	result, err := waitForResult(r.Context(), s.resultCh)
	if err != nil {
		status := http.StatusGatewayTimeout
		if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func waitForResult(ctx context.Context, resultCh <-chan *Result) (*Result, error) {
	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("OAuth callback cancelled: %w", ctx.Err())
	}
}

func tryReuseExistingListener(ctx context.Context, cfg CallbackConfig, redirectURI string, listenErr error) (*Result, error) {
	if cfg.FixedPort == 0 || !isAddressInUse(listenErr) {
		return nil, listenErr
	}

	probeURL := fmt.Sprintf("http://%s:%d%s", cfg.ListenHost, cfg.FixedPort, probePath)
	probeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, err
	}

	probeResp, err := reuseHTTPClient.Do(probeReq)
	if err != nil {
		return nil, err
	}
	defer probeResp.Body.Close()
	if probeResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe returned status %d", probeResp.StatusCode)
	}

	var probe probeResponse
	if err := json.NewDecoder(probeResp.Body).Decode(&probe); err != nil {
		return nil, err
	}
	if probe.Kind != listenerKind || probe.Version != listenerVersion || probe.CallbackPath != cfg.CallbackPath || probe.RedirectURI != redirectURI || !probe.Active {
		return nil, fmt.Errorf("incompatible existing OAuth listener")
	}

	resultURL := fmt.Sprintf("http://%s:%d%s", cfg.ListenHost, cfg.FixedPort, resultPath)
	resultReq, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}

	resultResp, err := reuseHTTPClient.Do(resultReq)
	if err != nil {
		return nil, err
	}
	defer resultResp.Body.Close()
	if resultResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resultResp.Body, 512))
		return nil, fmt.Errorf("reuse result returned status %d: %s", resultResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result Result
	if err := json.NewDecoder(resultResp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func isAddressInUse(err error) bool {
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) && errors.Is(syscallErr.Err, syscall.EADDRINUSE) {
		return true
	}
	return errors.Is(err, syscall.EADDRINUSE)
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
