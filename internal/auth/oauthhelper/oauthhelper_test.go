// Copyright (c) 2025 Reliant Labs
package oauthhelper

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort reserves a port and releases it, so a test can bind it without
// colliding with the real 19284 (which a developer's daemon may well hold).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func startTestServer(t *testing.T, source string) *Server {
	t.Helper()
	srv, err := Start(Options{Port: freePort(t), Source: source})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

// /health must IDENTIFY the service, not merely return 200. A bare status
// leaves the web app unable to tell reliant from any other process that
// happens to hold the port — and offering an OAuth flow to that process fails
// silently.
func TestHealth_IdentifiesServiceAndVersion(t *testing.T) {
	srv := startTestServer(t, "daemon")

	resp, err := http.Get("http://" + srv.Addr() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Service != ServiceName {
		t.Errorf("service = %q, want %q — this field is what proves reliant answered", body.Service, ServiceName)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if !body.Ready {
		t.Error("ready = false, want true")
	}
	if body.Version == "" {
		t.Error("version must not be empty")
	}
	if body.Source != "daemon" {
		t.Errorf("source = %q, want daemon", body.Source)
	}
}

// The daemon embeds this in a long-running process. A second binder must get a
// typed, recognizable error so the daemon can log-and-continue rather than
// refuse to start — trading all tool execution for an auth convenience.
func TestStart_PortInUseIsTyped(t *testing.T) {
	first := startTestServer(t, "daemon")

	_, port, err := net.SplitHostPort(first.Addr())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	var portNum int
	if _, err := fmtSscan(port, &portNum); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	_, err = Start(Options{Port: portNum, Source: "auth-serve"})
	if err == nil {
		t.Fatal("expected an error binding an already-held port")
	}
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("error should be ErrPortInUse so callers can degrade gracefully, got: %v", err)
	}
}

// The allowlist must honour RELIANT_WEB_ORIGIN in BOTH hosts (daemon and
// `auth serve`). Resolving it in only one is how the daemon path would 403 a
// request `auth serve` allows, with an error naming the origin and not the
// reason.
func TestAllowedOrigins_HonoursEnvAndFrontendPort(t *testing.T) {
	t.Setenv("RELIANT_WEB_ORIGIN", "http://localhost:4321, http://localhost:4322")
	t.Setenv("FRONTEND_PORT", "3600")

	allowed := buildAllowedOrigins(nil)

	for _, want := range []string{
		"http://localhost:4321",      // from RELIANT_WEB_ORIGIN
		"http://localhost:4322",      // second entry, whitespace trimmed
		"http://localhost:3600",      // derived from FRONTEND_PORT
		"http://127.0.0.1:3600",      // loopback twin
		"https://app.reliantlabs.io", // hosted default
		"http://localhost:5173",      // vite default
	} {
		if !allowed[want] {
			t.Errorf("origin %q should be allowed", want)
		}
	}
	if allowed["http://evil.example.com"] {
		t.Error("an undeclared origin must not be allowed")
	}
}

// CORS must be reflected only for allowed origins — never a blanket "*", which
// would let any page on the internet drive a local OAuth flow.
func TestCORS_OnlyReflectsAllowedOrigins(t *testing.T) {
	t.Setenv("RELIANT_WEB_ORIGIN", "http://localhost:4321")
	srv := startTestServer(t, "daemon")

	for name, tc := range map[string]struct{ origin, want string }{
		"allowed":    {"http://localhost:4321", "http://localhost:4321"},
		"disallowed": {"http://evil.example.com", ""},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+"/health", nil)
			req.Header.Set("Origin", tc.origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != tc.want {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tc.want)
			}
		})
	}
}

// A POST from a disallowed origin must be refused before any browser opens.
func TestOAuthStart_RejectsDisallowedOrigin(t *testing.T) {
	srv := startTestServer(t, "daemon")

	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/oauth/start", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// The helper must bind loopback ONLY: it starts browsers and returns
// authorization codes, so network reachability would be a real exposure.
func TestStart_BindsLoopbackOnly(t *testing.T) {
	srv := startTestServer(t, "daemon")

	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("listening on %q, want 127.0.0.1 — must not be reachable off-box", host)
	}
}

// The port must NOT outlive the linking session. An always-open localhost
// listener that starts browsers and returns authorization codes is a standing
// surface; opening it per session keeps the exposure equal to the task.
func TestIdleTimeout_ClosesThePort(t *testing.T) {
	idled := make(chan struct{})
	srv, err := Start(Options{
		Port:        freePort(t),
		Source:      "daemon",
		IdleTimeout: 1 * time.Second,
		OnIdle:      func() { close(idled) },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := srv.Addr()

	select {
	case <-idled:
	case <-time.After(15 * time.Second):
		t.Fatal("helper never closed itself after the idle timeout")
	}

	// The listener must actually be gone, not merely reported closed.
	if _, err := http.Get("http://" + addr + "/health"); err == nil {
		t.Fatal("port still accepting connections after idle close")
	}
}

// A request must RESET the idle clock, or a user reading the consent screen
// would have the port closed under them.
func TestIdleTimeout_RequestKeepsItAlive(t *testing.T) {
	srv, err := Start(Options{
		Port:        freePort(t),
		Source:      "daemon",
		IdleTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	addr := srv.Addr()

	// Poke it past the would-be deadline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			t.Fatalf("helper closed while still being used: %v", err)
		}
		_ = resp.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

// Shutdown must be safe to call twice — the idle watcher and the owner's
// explicit close race by design.
func TestShutdown_Idempotent(t *testing.T) {
	srv := startTestServer(t, "daemon")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown should be a no-op, got: %v", err)
	}
}

// fmtSscan is a tiny indirection so the test file needs no fmt import alias
// dance; kept local to avoid widening the package's own imports.
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number: " + s)
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}
