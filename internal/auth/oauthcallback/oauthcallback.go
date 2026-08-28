// Package oauthcallback provides shared OAuth callback handling for both the
// daemon runtime and the standalone `reliant auth serve` HTTP server.
package oauthcallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	probePath  = "/__reliant/oauth/probe"
	resultPath = "/__reliant/oauth/result"

	listenerKind    = "reliant-oauth-callback"
	listenerVersion = 1

	// waiterDrainBudget bounds how long the owning flow waits for queued flows
	// to receive the result. Local writes, so this is generous by an order of
	// magnitude; it exists only so a waiter that vanishes cannot hang the owner.
	waiterDrainBudget = 5 * time.Second

	// shutdownBudget bounds the graceful shutdown that follows. The browser
	// holds a keep-alive connection open after the callback, and Shutdown waits
	// for idle connections, so this must be short enough not to stall the
	// caller — Close() is the fallback when it expires.
	shutdownBudget = 2 * time.Second
)

// CallbackConfig describes how to listen for and construct the OAuth redirect URI.
type CallbackConfig struct {
	ListenHost   string
	RedirectHost string
	CallbackPath string
	FixedPort    int // 0 = OS-assigned
}

// Result is the data returned from an OAuth redirect to our callback.
type Result struct {
	Code                  string `json:"code"`
	State                 string `json:"state"`
	RedirectURI           string `json:"redirect_uri"`
	CallbackURL           string `json:"callback_url"`
	OAuthError            string `json:"oauth_error,omitempty"`
	OAuthErrorDescription string `json:"oauth_error_description,omitempty"`
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

	// done is closed when the callback lands, and result holds what arrived.
	//
	// A channel delivers to exactly ONE receiver, which is wrong here: the
	// flow that owns the listener AND every later flow that queued on /result
	// are all waiting for the same single callback. Closing a channel is a
	// broadcast, so everyone waiting is released with the same answer.
	mu     sync.Mutex
	result *Result
	done   chan struct{}

	// inFlight counts queued flows currently blocked in handleResult, and
	// drained is closed once the last one has been written back to.
	//
	// Releasing every waiter (close(done)) is not the same as every waiter
	// having RECEIVED its answer. The owning flow returns from awaitResult the
	// instant the callback lands and its deferred Close() kills the listener,
	// which severs the /result responses still being written to the queued
	// flows. Those flows then fall out of tryReuseExistingListener with a
	// transport error, and Run reports the ORIGINAL bind error — "address
	// already in use" — which is precisely the failure the queue exists to
	// prevent. So the owner must wait for the handoff to complete, not merely
	// for the answer to exist.
	inFlight int
	drained  chan struct{}
}

// addWaiter registers a queued flow that is about to block on the result.
func (s *callbackServer) addWaiter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight++
}

// doneWaiter marks a queued flow as fully served, closing drained when the
// last one finishes so the owner's shutdown can proceed.
func (s *callbackServer) doneWaiter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	if s.inFlight == 0 && s.drained != nil {
		close(s.drained)
		s.drained = nil
	}
}

// waitForWaiters blocks until every queued flow has been written back to, or
// the budget expires. A queued flow that never arrives cannot hold the owner
// open indefinitely.
func (s *callbackServer) waitForWaiters(budget time.Duration) {
	s.mu.Lock()
	if s.inFlight == 0 {
		s.mu.Unlock()
		return
	}
	if s.drained == nil {
		s.drained = make(chan struct{})
	}
	drained := s.drained
	s.mu.Unlock()

	select {
	case <-drained:
	case <-time.After(budget):
	}
}

// newResult records the callback and wakes every waiter, exactly once.
// Duplicate deliveries (a provider that redirects twice, a user who reloads
// the callback URL) are ignored rather than racing a second answer in.
func (s *callbackServer) newResult(result *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result != nil {
		return
	}
	s.result = result
	close(s.done)
}

// awaitResult blocks until the callback lands or ctx ends. Safe for any number
// of concurrent callers.
func (s *callbackServer) awaitResult(ctx context.Context) (*Result, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("OAuth callback cancelled: %w", ctx.Err())
	}
}

var openBrowser = OpenBrowser

// reuseProbeClient asks "is a compatible listener there?" — a local,
// immediate question, so a short timeout is right.
var reuseProbeClient = &http.Client{Timeout: 2 * time.Second}

// reuseResultClient waits for the USER to finish signing in on the incumbent
// listener. That spans human time — logging in, choosing an org, clearing 2FA
// — so it must NOT carry the probe's timeout.
//
// It previously shared the 2s client, which made every overlapping attempt
// fail: the probe succeeded, /result blocked waiting for a human, the client
// gave up after two seconds, and Run() reported the ORIGINAL bind error
// ("address already in use") instead of waiting its turn. Bounded only by the
// caller's context, which is what actually knows when the user gave up.
var reuseResultClient = &http.Client{}

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

	// Hand off to any queued flows before dropping the listener, then shut
	// down gracefully so their in-flight /result responses — and the browser's
	// own callback response — finish writing. Close() severs both mid-write.
	defer func() {
		server.waitForWaiters(waiterDrainBudget)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
		}
	}()

	go func() {
		<-ctx.Done()
		// A cancelled flow has no answer to hand anyone, so there is nothing
		// to drain — but still shut down gracefully rather than severing the
		// socket, so the port is released cleanly for the next attempt.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
		}
	}()

	if err := openBrowser(authorizeURL); err != nil {
		return nil, fmt.Errorf("failed to open browser: %w", err)
	}

	result, err := server.awaitResult(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateOAuthResult(result); err != nil {
		return nil, err
	}
	return result, nil
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
		done:        make(chan struct{}),
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
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	oauthErr := strings.TrimSpace(r.URL.Query().Get("error"))
	desc := strings.TrimSpace(r.URL.Query().Get("error_description"))

	result := &Result{
		Code:                  code,
		State:                 r.URL.Query().Get("state"),
		RedirectURI:           s.redirectURI,
		CallbackURL:           r.URL.String(),
		OAuthError:            oauthErr,
		OAuthErrorDescription: desc,
	}
	s.newResult(result)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if code != "" {
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0"><div style="text-align:center"><h2>Authentication Successful</h2><p>You can close this tab and return to Reliant.</p></div></body></html>`)
		return
	}
	const title = "Authentication failed"
	var body string
	switch {
	case oauthErr != "" && desc != "":
		body = html.EscapeString(oauthErr) + ": " + html.EscapeString(desc)
	case oauthErr != "":
		body = html.EscapeString(oauthErr)
	case desc != "":
		body = html.EscapeString(desc)
	default:
		body = html.EscapeString("No authorization code was returned. You can close this tab and try again.")
	}
	fmt.Fprintf(w, `<!DOCTYPE html><html><body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0"><div style="text-align:center"><h2>%s</h2><p>%s</p></div></body></html>`,
		html.EscapeString(title), body)
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
	s.addWaiter()
	defer s.doneWaiter()

	result, err := s.awaitResult(r.Context())
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

func validateOAuthResult(res *Result) error {
	if res == nil {
		return fmt.Errorf("OAuth callback returned no result")
	}
	if strings.TrimSpace(res.Code) != "" {
		return nil
	}
	if res.OAuthError != "" {
		msg := res.OAuthError
		if strings.TrimSpace(res.OAuthErrorDescription) != "" {
			msg = msg + ": " + res.OAuthErrorDescription
		}
		return fmt.Errorf("OAuth authorization failed: %s", msg)
	}
	return fmt.Errorf("OAuth callback did not include an authorization code")
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

	probeResp, err := reuseProbeClient.Do(probeReq)
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
	// Name the field that disagreed. A bare "incompatible" is discarded by the
	// caller in favour of the original bind error, so a mismatch here used to
	// surface as "address already in use" with no way to tell which of five
	// conditions rejected the handoff.
	switch {
	case probe.Kind != listenerKind:
		return nil, fmt.Errorf("existing listener kind %q != %q", probe.Kind, listenerKind)
	case probe.Version != listenerVersion:
		return nil, fmt.Errorf("existing listener version %d != %d", probe.Version, listenerVersion)
	case probe.CallbackPath != cfg.CallbackPath:
		return nil, fmt.Errorf("existing listener path %q != %q", probe.CallbackPath, cfg.CallbackPath)
	case probe.RedirectURI != redirectURI:
		return nil, fmt.Errorf("existing listener redirect %q != %q", probe.RedirectURI, redirectURI)
	case !probe.Active:
		return nil, fmt.Errorf("existing listener reports inactive")
	}

	resultURL := fmt.Sprintf("http://%s:%d%s", cfg.ListenHost, cfg.FixedPort, resultPath)
	resultReq, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}

	resultResp, err := reuseResultClient.Do(resultReq)
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
	if err := validateOAuthResult(&result); err != nil {
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
