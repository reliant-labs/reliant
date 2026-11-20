// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestResolveLaunchConfig_ResolvesEveryField(t *testing.T) {
	daemonEnv := lookupFrom(map[string]string{
		"HOME":         "/home/u",
		"GITHUB_TOKEN": "gh-from-daemon",
		"EMPTY":        "",
	})
	in := config.MCPServer{
		Type:    config.MCPStdio,
		Command: "${HOME}/bin/server",
		Args: []string{
			"--header", "key:${AUTH_TOKEN}", // the server's own env block wins
			"--home", "$HOME", // bare $VAR
			"--region", "${REGION:-us-east-1}", // unset: default
			"--empty", "${EMPTY:-fallback}", // set but empty: default
			"--missing", "x${NOPE}y", // unset, no default: empty
			"--workspace", "${projectPath}", // placeholder, not an env lookup
		},
		Env:     []string{"AUTH_TOKEN=${GITHUB_TOKEN}", "PLAIN=v"},
		URL:     "https://mcp.example.com/${GITHUB_TOKEN}",
		Headers: map[string]string{"Authorization": "Bearer ${AUTH_TOKEN}"},
		Dir:     "${HOME}/proj",
	}
	got := resolveLaunchConfig(in, daemonEnv)

	if want := "/home/u/bin/server"; got.Command != want {
		t.Errorf("Command = %q, want %q", got.Command, want)
	}
	wantArgs := []string{
		"--header", "key:gh-from-daemon",
		"--home", "/home/u",
		"--region", "us-east-1",
		"--empty", "fallback",
		"--missing", "xy",
		"--workspace", "/home/u/proj",
	}
	if len(got.Args) != len(wantArgs) {
		t.Fatalf("Args = %q, want %q", got.Args, wantArgs)
	}
	for i := range wantArgs {
		if got.Args[i] != wantArgs[i] {
			t.Errorf("Args[%d] = %q, want %q", i, got.Args[i], wantArgs[i])
		}
	}
	if want := []string{"AUTH_TOKEN=gh-from-daemon", "PLAIN=v"}; len(got.Env) != 2 || got.Env[0] != want[0] || got.Env[1] != want[1] {
		t.Errorf("Env = %q, want %q", got.Env, want)
	}
	if want := "https://mcp.example.com/gh-from-daemon"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
	if want := "Bearer gh-from-daemon"; got.Headers["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", got.Headers["Authorization"], want)
	}
	if want := "/home/u/proj"; got.Dir != want {
		t.Errorf("Dir = %q, want %q", got.Dir, want)
	}

	// The input is the stored config; resolving must not write through it.
	if in.Args[1] != "key:${AUTH_TOKEN}" || in.Env[0] != "AUTH_TOKEN=${GITHUB_TOKEN}" || in.Headers["Authorization"] != "Bearer ${AUTH_TOKEN}" {
		t.Errorf("resolveLaunchConfig modified its input: %+v", in)
	}
}

// The daemon launches an HTTP server with its URL and headers resolved from
// the daemon's own environment: the request that reaches the server carries
// the values, not the references.
func TestClientInitialize_ResolvesHTTPReferencesFromDaemonEnvironment(t *testing.T) {
	t.Setenv("RELIANT_TEST_MCP_PATH", "resolved-path")
	t.Setenv("RELIANT_TEST_MCP_TOKEN", "daemon-token")

	var (
		mu       sync.Mutex
		gotPath  string
		gotAuth  string
		gotExtra string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if gotPath == "" {
			gotPath, gotAuth, gotExtra = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Extra")
		}
		mu.Unlock()
		http.Error(w, "not an MCP server", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient("ref-http", config.MCPServer{
		Type: config.MCPHTTP,
		URL:  srv.URL + "/${RELIANT_TEST_MCP_PATH}",
		Env:  []string{"EXTRA=${RELIANT_TEST_MCP_TOKEN}-extra"},
		Headers: map[string]string{
			"Authorization": "Bearer ${RELIANT_TEST_MCP_TOKEN}",
			"X-Extra":       "${EXTRA}",
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Initialize(ctx) // the handshake fails; the request it sent is the evidence
	_ = c.Close()

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/resolved-path" {
		t.Errorf("request path = %q, want /resolved-path", gotPath)
	}
	if gotAuth != "Bearer daemon-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer daemon-token")
	}
	if gotExtra != "daemon-token-extra" {
		t.Errorf("X-Extra = %q, want %q", gotExtra, "daemon-token-extra")
	}
}

// A stdio server's command and args are resolved from the daemon's
// environment when it is launched.
func TestClientInitialize_ResolvesStdioReferencesFromDaemonEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses touch")
	}
	touch, err := exec.LookPath("touch")
	if err != nil {
		t.Skip("touch not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("RELIANT_TEST_MCP_TOUCH", touch)
	t.Setenv("RELIANT_TEST_MCP_DIR", dir)

	c, err := NewClient("ref-stdio", config.MCPServer{
		Type:    config.MCPStdio,
		Command: "${RELIANT_TEST_MCP_TOUCH}",
		Args:    []string{"${RELIANT_TEST_MCP_DIR}/${NAME}"},
		Env:     []string{"NAME=launched"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Initialize(ctx) // touch is no MCP server; that it ran is the evidence
	_ = c.Close()

	marker := filepath.Join(dir, "launched")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the launched command never created %s: command or args were not resolved", marker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
