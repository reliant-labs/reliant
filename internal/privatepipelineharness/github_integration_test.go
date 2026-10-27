//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type providerRequest struct {
	Method string
	Host   string
	Path   string
}

// githubBoundary redirects only canonical provider requests to an owned
// loopback fake. It neither implements a successful internal service nor
// changes any production hostname/SSRF/redirect policy. Credentials are never
// recorded. Callers program GitHub's actual HTTP API in Handler.
type githubBoundary struct {
	server    *httptest.Server
	transport *http.Transport
	mu        sync.Mutex
	requests  []providerRequest
}

func newGitHubBoundary(t *testing.T, handler http.Handler) *githubBoundary {
	t.Helper()
	b := &githubBoundary{server: httptest.NewServer(handler)}
	b.transport = http.DefaultTransport.(*http.Transport).Clone()
	// This client connects only to our fake, never through inherited proxies.
	b.transport.Proxy = nil
	t.Cleanup(func() {
		b.transport.CloseIdleConnections()
		b.server.Close()
	})
	return b
}

func (b *githubBoundary) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil {
		return nil, fmt.Errorf("private fixture refused noncanonical provider destination")
	}
	base, err := url.Parse(b.server.URL)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.requests = append(b.requests, providerRequest{Method: req.Method, Host: req.URL.Host, Path: req.URL.EscapedPath()})
	b.mu.Unlock()
	forward := req.Clone(req.Context())
	forward.URL.Scheme, forward.URL.Host = base.Scheme, base.Host
	forward.Host = "api.github.com"
	return b.transport.RoundTrip(forward)
}

func (b *githubBoundary) Requests() []providerRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]providerRequest(nil), b.requests...)
}

// This is a boundary-fixture test, not a claim of any real adapter flow.
func TestGitHubBoundaryRejectsOtherDestinations(t *testing.T) {
	b := newGitHubBoundary(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.github.com" || r.URL.Path != "/repos/acme/example" {
			t.Errorf("unexpected provider request: %s %s", r.Host, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	c := &http.Client{Transport: b}
	for _, address := range []string{
		"http://api.github.com/repos/acme/example",
		"https://github.com/repos/acme/example",
		"https://api.github.com.attacker.test/repos/acme/example",
		"https://user:secret@api.github.com/repos/acme/example",
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
			t.Fatalf("unexpected acceptance: %s", address)
		}
	}
	if len(b.Requests()) != 0 {
		t.Fatal("rejected destination reached provider fixture")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/example", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(b.Requests()) != 1 {
		t.Fatalf("unexpected fixture response: %d requests=%d", resp.StatusCode, len(b.Requests()))
	}
}
