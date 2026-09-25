package oauthcallback

import (
	"fmt"
	"net"
	"testing"
)

// The Codex callback port is a CONTRACT with OpenAI, not a choice: the
// redirect URI http://localhost:1455/auth/callback is registered on their side,
// so InferConfig must produce exactly it. This pins that contract without
// binding the port — nothing here opens a socket, so it cannot contend with any
// other test on the machine.
func TestInferConfigCodexUsesRegisteredFixedPort(t *testing.T) {
	got := InferConfig(codexAuthorizeTemplate)
	want := CallbackConfig{
		ListenHost:   "127.0.0.1",
		RedirectHost: "localhost",
		CallbackPath: "/auth/callback",
		FixedPort:    1455,
	}
	if got != want {
		t.Fatalf("InferConfig(codex) = %+v, want %+v", got, want)
	}
}

// Claude takes an OS-assigned port: its redirect URI carries the port, so any
// free one works and there is nothing to contend for.
func TestInferConfigClaudeUsesOSAssignedPort(t *testing.T) {
	got := InferConfig("https://claude.ai/oauth/authorize?redirect_uri={redirect_uri}")
	if got.FixedPort != 0 {
		t.Fatalf("Claude FixedPort = %d, want 0 (OS-assigned)", got.FixedPort)
	}
	if got.CallbackPath != "/callback" {
		t.Fatalf("Claude CallbackPath = %q, want /callback", got.CallbackPath)
	}
}

// codexShapedConfig is InferConfig's Codex configuration — a FIXED port, with
// everything that implies (no fallback, reuse handshake, listen retry) — moved
// to a port nothing else is using.
//
// The contention tests exercise the fixed-port logic, not the number 1455.
// Binding the real port made them contend with every other test binary that
// did the same: `go test ./...` runs packages concurrently, and oauthhelper's
// parallel tests also took 1455, so this package flaked on CI with
// "connection refused" / "address already in use". The contract itself is
// pinned above by TestInferConfigCodexUsesRegisteredFixedPort.
func codexShapedConfig(t *testing.T) CallbackConfig {
	t.Helper()
	cfg := InferConfig(codexAuthorizeTemplate)
	if cfg.FixedPort == 0 {
		t.Fatal("InferConfig(codex) no longer returns a fixed port — these tests exercise the fixed-port path")
	}
	cfg.FixedPort = freePort(t)
	return cfg
}

// freePort returns a port the OS just handed out and released.
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

// listenAddr is where a flow with cfg binds.
func listenAddr(cfg CallbackConfig) string {
	return fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.FixedPort)
}

// baseURL is how a client (the browser, a queued flow) reaches cfg's listener.
func baseURL(cfg CallbackConfig) string {
	return "http://" + listenAddr(cfg)
}
