//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cpEndpoint struct {
	URL     string `json:"url"`
	OwnerID string `json:"owner_id"`
	secret  string
}

func startCP(t *testing.T, provider *githubBoundary) cpEndpoint {
	t.Helper()
	requirePrivatePipeline(t)
	root := os.Getenv("CP_CHECKOUT")
	if root == "" {
		t.Fatal("CP_CHECKOUT must explicitly name the isolated CP checkout")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/privatepipelineharness/review_check_server_test.go")); err != nil {
		t.Fatalf("CP helper prerequisite: %v", err)
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	// Own the lifetime through cleanup: t.Context is cancelled BEFORE cleanup,
	// which would kill the child before its stdin-driven graceful SQL teardown.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	// Execute the compiled test binary directly: the go-test runner does not
	// preserve the parent control stdin for its test child, and test caching
	// must never replay an earlier helper's readiness URL.
	binary := filepath.Join(t.TempDir(), "cp-private-fixture.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-tags", "integration", "-o", binary, "./internal/privatepipelineharness")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off")
	if err := build.Run(); err != nil {
		cancel()
		t.Fatalf("compile real CP helper: %v", err)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.v", "-test.run=^TestFutureChatBCPServer$", "-test.timeout=210s")
	cmd.Dir = root
	// Explicit GOWORK=off preserves the CP module pin. Never depend on a
	// workspace replace to smuggle either module's internal packages across.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOWORK=") || strings.HasPrefix(entry, "RUN_FUTURECHAT_B_CP_HELPER=") || strings.HasPrefix(entry, "CP_GITHUB_FIXTURE_URL=") || strings.HasPrefix(entry, "CP_PRIVATE_FIXTURE_SECRET=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "RUN_FUTURECHAT_B_CP_HELPER=1", "CP_GITHUB_FIXTURE_URL="+provider.server.URL, "CP_PRIVATE_FIXTURE_SECRET="+secret)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		t.Fatal(err)
	}
	// Helper diagnostics may mention SQL/provider errors. Do not echo raw
	// subprocess output into logs that could leak credentials.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		stdin.Close()
		cancel()
		t.Fatal(err)
	}
	ready := make(chan cpEndpoint, 1)
	scanned := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var line struct {
				Event string `json:"event"`
				cpEndpoint
			}
			if json.Unmarshal(scanner.Bytes(), &line) == nil && line.Event == "futurechat-b-cp-ready" {
				select {
				case ready <- line.cpEndpoint:
				default:
				}
			}
		}
		scanned <- scanner.Err()
	}()
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("owned CP helper failed: %v (raw diagnostics withheld)", err)
			}
		case <-time.After(15 * time.Second):
			// Context cancellation targets this exact child, never a named
			// process pattern or another agent's server.
			cancel()
			if err := <-finished; err != nil {
				t.Errorf("owned CP helper did not shut down gracefully: %v", err)
			}
		}
		cancel()
	})
	select {
	case endpoint := <-ready:
		if err := validateCPReady(endpoint); err != nil {
			t.Fatal(err)
		}
		endpoint.secret = secret
		return endpoint
	case <-scanned:
		t.Fatal("CP helper ended before readiness (raw diagnostics withheld)")
	case <-ctx.Done():
		t.Fatal("CP helper readiness deadline exceeded")
	}
	return cpEndpoint{}
}

func validateCPReady(endpoint cpEndpoint) error {
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("invalid private CP readiness URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" || endpoint.OwnerID != "futurechat-b-policy-owner" {
		return fmt.Errorf("CP readiness must be the owned loopback fixture and policy owner")
	}
	return nil
}

func TestCPReadinessRejectsNonlocalOrWrongOwner(t *testing.T) {
	for _, endpoint := range []cpEndpoint{
		{URL: "https://prod.example.test", OwnerID: "futurechat-b-policy-owner"},
		{URL: "http://127.0.0.1:12345", OwnerID: "other-owner"},
		{URL: "http://localhost:12345", OwnerID: "futurechat-b-policy-owner"},
	} {
		if err := validateCPReady(endpoint); err == nil {
			t.Fatal("accepted unowned CP readiness endpoint")
		}
	}
	if err := validateCPReady(cpEndpoint{URL: "http://127.0.0.1:12345", OwnerID: "futurechat-b-policy-owner"}); err != nil {
		t.Fatal(err)
	}
}
