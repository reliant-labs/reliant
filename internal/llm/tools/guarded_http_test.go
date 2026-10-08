// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useTransport(t *testing.T, mk func() http.RoundTripper) {
	t.Helper()
	prev := webToolTransport
	webToolTransport = mk
	t.Cleanup(func() { webToolTransport = prev })
}

func hostedTransport() http.RoundTripper {
	return netguard.ForDeployment("https://control-plane.example").Transport()
}

// publicRedirector pretends public.example.test is a public host that 302s to
// Location; every other request goes through the real hosted guard.
type publicRedirector struct{ location string }

func (p publicRedirector) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "public.example.test" {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{p.location}},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	}
	return hostedTransport().RoundTrip(r)
}

func runFetch(t *testing.T, url string) ToolResponse {
	t.Helper()
	tool := NewFetchTool().(*ToolWrapper[FetchParams, ToolResponse])
	resp, err := tool.tool.Execute(&rctx.ToolContext{Context: context.Background()}, FetchParams{URL: url, Format: "text"})
	require.NoError(t, err)
	return resp
}

func TestFetch_HostedPolicyBlocksInternalDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer srv.Close()

	useTransport(t, func() http.RoundTripper {
		return publicRedirector{location: srv.URL + "/"}
	})

	for name, url := range map[string]string{
		"loopback":          srv.URL + "/",
		"metadata":          "http://169.254.169.254/latest/meta-data/",
		"redirect-to-local": "http://public.example.test/",
	} {
		t.Run(name, func(t *testing.T) {
			resp := runFetch(t, url)
			assert.True(t, resp.IsError, "must be an error result")
			assert.Contains(t, resp.Content, "address is not allowed")
			assert.NotContains(t, resp.Content, "secret")
		})
	}
}

func TestFetch_SelfHostedPolicyAllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()
	useTransport(t, func() http.RoundTripper {
		return netguard.ForDeployment("").Transport()
	})
	resp := runFetch(t, srv.URL+"/")
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "hello")
}

func TestWebTools_ClientsAreGuarded(t *testing.T) {
	var guarded bool
	useTransport(t, func() http.RoundTripper { guarded = true; return http.DefaultTransport })
	for name, ctor := range map[string]func() Tool{
		ToolFetch: NewFetchTool, ToolWebSearch: NewWebSearchTool, ToolSourcegraph: NewSourcegraphTool,
	} {
		guarded = false
		ctor()
		assert.True(t, guarded, "%s must build its client through newGuardedHTTPClient", name)
	}
}

// Any non-test file in this package that builds its own client, transport or
// dialer bypasses the guard that PlacementServer/PlacementAny tools rely on.
func TestNoUnguardedHTTPClientsInToolPackage(t *testing.T) {
	banned := regexp.MustCompile(`http\.Client\{|http\.DefaultClient|http\.DefaultTransport|http\.Transport\{|http\.(Get|Post|PostForm|Head)\(|net\.Dial|net\.Dialer`)
	entries, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") || f == "guarded_http.go" {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			assert.False(t, banned.MatchString(line), "%s:%d builds an unguarded HTTP client; use newGuardedHTTPClient: %s", f, i+1, line)
		}
	}
}
