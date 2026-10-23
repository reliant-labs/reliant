package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDevStatusJSON_IngressDisabledEmitsEmptyArray: when
// features.ingress is off in forge.yaml, the --json output must contain
// an empty (non-null) ingress_urls array. Empty-not-null matters because
// dashboards iterate the field without a nil guard.
func TestRunDevStatusJSON_IngressDisabledEmitsEmptyArray(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `name: testproj
module_path: github.com/example/testproj
version: "0.1.0"
binary: shared
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	markServiceProject(t, dir)
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := runDevStatus(context.Background(), "deploy/k3d.yaml", true); err != nil {
			t.Fatalf("runDevStatus: %v", err)
		}
	})

	var got devStatusSummary
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v\nraw=%s", err, out)
	}
	if got.IngressURLs == nil {
		t.Fatalf("ingress_urls is nil; want empty slice. raw=%s", out)
	}
	if len(got.IngressURLs) != 0 {
		t.Errorf("ingress_urls = %+v; want empty", got.IngressURLs)
	}
	// JSON must spell the field as a snake_case array literal.
	if !strings.Contains(out, `"ingress_urls": []`) {
		t.Errorf("expected `\"ingress_urls\": []` in JSON, got:\n%s", out)
	}
}

// TestWriteIngressURLsSection_Disabled: covers the human-output branch
// when features.ingress is off — the disabled hint must render exactly
// so scripts grepping the output don't break on phrasing drift.
func TestWriteIngressURLsSection_Disabled(t *testing.T) {
	var buf bytes.Buffer
	writeIngressURLsSection(&buf, nil, false)
	got := buf.String()
	if !strings.Contains(got, "Ingress URLs:") {
		t.Errorf("missing section header; got:\n%s", got)
	}
	if !strings.Contains(got, "(ingress feature disabled)") {
		t.Errorf("missing disabled hint; got:\n%s", got)
	}
}

// TestWriteIngressURLsSection_EmptyEnabled: feature on but no routes —
// the hint should point at the canonical KCL ingress file.
func TestWriteIngressURLsSection_EmptyEnabled(t *testing.T) {
	var buf bytes.Buffer
	writeIngressURLsSection(&buf, nil, true)
	got := buf.String()
	if !strings.Contains(got, "(none — see deploy/kcl/dev/ingress.k)") {
		t.Errorf("missing empty hint; got:\n%s", got)
	}
}

// TestBuildDevStatusIngressURLs covers the URL-construction matrix:
// HTTP vs HTTPS scheme dispatch, GRPCRoute scheme, host fallback chain
// (route > gateway > localhost), and PathPrefix + Path concatenation
// with double-slash collapse + empty-path-to-"/".
func TestBuildDevStatusIngressURLs(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{
			{
				Name: "edge",
				Host: "gw.example.com",
				Listeners: []GatewayListenerEntity{
					{Name: "http", Port: 80, Protocol: "HTTP", PathPrefix: "/api"},
					{Name: "https", Port: 443, Protocol: "HTTPS"},
					{Name: "grpc", Port: 50051, Protocol: "H2C"},
				},
			},
			{
				Name: "internal",
				// Host empty → "localhost" fallback.
				Listeners: []GatewayListenerEntity{
					{Name: "http", Port: 8080, Protocol: "HTTP", PathPrefix: "/"},
				},
			},
		},
		HTTPRoutes: []HTTPRouteEntity{
			{Name: "api-http", Gateway: "edge", Listener: "http", Service: "api", Port: 8080, Path: "/v1"},
			{Name: "api-https", Gateway: "edge", Listener: "https", Service: "api", Port: 8080, Host: "api.example.com", Path: "/v1"},
			{Name: "root", Gateway: "internal", Listener: "http", Service: "metrics", Port: 9090},
			// Path-prefix ends with /, route.Path starts with / — must collapse.
			{Name: "collapse", Gateway: "internal", Listener: "http", Service: "x", Port: 1, Path: "/y"},
			// Orphan: unknown gateway should be dropped.
			{Name: "orphan", Gateway: "nope", Listener: "http", Service: "x", Port: 1},
			// Orphan: unknown listener should be dropped.
			{Name: "orphan-listener", Gateway: "edge", Listener: "nope", Service: "x", Port: 1},
		},
		GRPCRoutes: []GRPCRouteEntity{
			{Name: "rpc", Gateway: "edge", Listener: "grpc", Service: "api", Port: 50051, Path: "/svc"},
		},
	}

	urls := buildDevStatusIngressURLs(entities)
	byName := map[string]ingressURLEntry{}
	for _, u := range urls {
		byName[u.Route] = u
	}

	cases := []struct {
		route   string
		wantURL string
		wantKnd string
	}{
		{"api-http", "http://gw.example.com:80/api/v1", "HTTPRoute"},
		{"api-https", "https://api.example.com:443/v1", "HTTPRoute"},
		{"root", "http://localhost:8080/", "HTTPRoute"},
		{"collapse", "http://localhost:8080/y", "HTTPRoute"},
		{"rpc", "grpc://gw.example.com:50051/svc", "GRPCRoute"},
	}
	for _, c := range cases {
		got, ok := byName[c.route]
		if !ok {
			t.Errorf("route %q missing from output", c.route)
			continue
		}
		if got.URL != c.wantURL {
			t.Errorf("route %q URL = %q; want %q", c.route, got.URL, c.wantURL)
		}
		if got.Kind != c.wantKnd {
			t.Errorf("route %q Kind = %q; want %q", c.route, got.Kind, c.wantKnd)
		}
	}
	if _, ok := byName["orphan"]; ok {
		t.Error("orphan route with unknown gateway should be dropped")
	}
	if _, ok := byName["orphan-listener"]; ok {
		t.Error("orphan route with unknown listener should be dropped")
	}
}

// TestBuildDevStatusIngressURLs_Nil exercises the nil-safety contract —
// runDevStatus passes nil when KCL render fails, and the helper must
// not panic.
func TestBuildDevStatusIngressURLs_Nil(t *testing.T) {
	if got := buildDevStatusIngressURLs(nil); got != nil {
		t.Errorf("buildDevStatusIngressURLs(nil) = %+v; want nil", got)
	}
}

// captureStdout redirects os.Stdout for the duration of f and returns
// what was written. Used so tests can assert on functions that
// `fmt.Printf` directly to os.Stdout without refactoring their public
// signature.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	_ = w.Close()
	return <-done
}
