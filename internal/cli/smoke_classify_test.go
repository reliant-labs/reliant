package cli

import (
	"net/http"
	"testing"
)

// sampleSmokeBundle is the ROUTE-HOSTED topology (single `edge` gateway,
// host="", each route carries its own host) — prod/preprod's shape. The
// frontend declares its API host via NEXT_PUBLIC_API_URL so CORS is
// scoped to exactly the admin route; the other routes (apex API, wildcard
// proxy) serve no CORS and must not carry an origin. A truly hostless
// route on a hostless gateway is still skipped (no addressable host).
const sampleSmokeBundle = `{
  "output": {
    "frontends": [
      {
        "name": "admin-web",
        "type": "nextjs",
        "path": "frontend",
        "env_vars": [
          {
            "name": "NEXT_PUBLIC_API_URL",
            "value": "https://admin-preprod.reliantapi.com"
          }
        ],
        "runtime": {
          "type": "firebase",
          "project": "p",
          "site": "reliant-preprod"
        }
      }
    ],
    "gateways": [
      {
        "name": "edge",
        "host": ""
      }
    ],
    "http_routes": [
      {
        "name": "api",
        "gateway": "edge",
        "service": "admin-server",
        "port": 8080,
        "host": "preprod.reliantapi.com",
        "path": "/"
      },
      {
        "name": "admin",
        "gateway": "edge",
        "service": "admin-server",
        "port": 8080,
        "host": "admin-preprod.reliantapi.com",
        "path": "/admin"
      },
      {
        "name": "workspace-proxy",
        "gateway": "edge",
        "service": "proxy",
        "port": 8080,
        "host": "*.workspaces-preprod.reliantapi.com",
        "path": "/"
      },
      {
        "name": "prefix-mount",
        "gateway": "edge",
        "service": "admin-server",
        "port": 8080,
        "path": "/internal"
      }
    ],
    "grpc_routes": [
      {
        "name": "grpc-api",
        "gateway": "edge",
        "service": "admin-server",
        "port": 8080,
        "host": "preprod.reliantapi.com"
      }
    ],
    "workloads": [
      {
        "name": "admin-server",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "gke_x",
          "namespace": "ns"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`

func TestExtractSmokeTargets(t *testing.T) {
	e, err := parseKCLEntities([]byte(sampleSmokeBundle))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	targets := extractSmokeTargets(e)

	// 3 http with host + 1 grpc with host = 4; the hostless route on the
	// hostless gateway (prefix-mount) is skipped.
	if len(targets) != 4 {
		t.Fatalf("want 4 targets (hostless-on-hostless-gw skipped), got %d: %+v", len(targets), targets)
	}

	byName := map[string]smokeTarget{}
	for _, tgt := range targets {
		byName[tgt.RouteName] = tgt
	}

	// CORS scoping: only the route whose host matches the frontend's
	// NEXT_PUBLIC_API_URL host (admin-preprod.reliantapi.com) carries the
	// origin — the apex API host, the wildcard proxy host, and the grpc
	// host do NOT (they serve no CORS by design).
	wantOrigin := "https://reliant-preprod.web.app"
	if got := byName["admin"].Origin; got != wantOrigin {
		t.Errorf("admin route origin = %q, want %q (it IS the frontend API host)", got, wantOrigin)
	}
	for _, name := range []string{"api", "workspace-proxy", "grpc-api"} {
		if got := byName[name].Origin; got != "" {
			t.Errorf("route %s: origin = %q, want empty (not a frontend API host)", name, got)
		}
	}

	// grpc route with empty path normalizes to "/".
	if got := byName["grpc-api"].Path; got != "/" {
		t.Errorf("grpc-api path = %q, want %q", got, "/")
	}
	if byName["grpc-api"].RouteKind != "grpc" {
		t.Errorf("grpc-api kind = %q, want grpc", byName["grpc-api"].RouteKind)
	}
	if got := byName["admin"].Path; got != "/admin" {
		t.Errorf("admin path = %q, want /admin", got)
	}
	if _, ok := byName["prefix-mount"]; ok {
		t.Errorf("hostless route on hostless gateway prefix-mount should be skipped")
	}
}

// TestExtractSmokeTargets_GatewayHosted covers staging's topology: the
// host lives on the GATEWAY and the routes attached to it carry an empty
// host. Every gateway's route must still be probed (against its own
// gateway), and CORS scoped to the gateway whose host matches the
// frontend API URL.
func TestExtractSmokeTargets_GatewayHosted(t *testing.T) {
	bundle := `{
	  "frontends": [
	    {"name": "admin-web", "type": "nextjs", "path": "frontend",
	     "env_vars": [{"name": "NEXT_PUBLIC_API_URL", "value": "https://admin-staging.reliantapi.com"}],
	     "runtime": {"type": "firebase", "project": "p", "site": "reliant-staging"}}
	  ],
	  "gateways": [
	    {"name": "public", "host": "staging.reliantapi.com"},
	    {"name": "public-admin", "host": "admin-staging.reliantapi.com"},
	    {"name": "public-grpc", "host": "gateway-staging.reliantapi.com"}
	  ],
	  "http_routes": [
	    {"name": "reliant-api", "gateway": "public", "service": "reliant", "port": 9090},
	    {"name": "admin-server", "gateway": "public-admin", "service": "admin", "port": 8090},
	    {"name": "workspace-proxy", "gateway": "public", "service": "proxy", "port": 8080, "host": "*.workspaces-staging.reliantapi.com"}
	  ],
	  "grpc_routes": [
	    {"name": "daemon-gateway", "gateway": "public-grpc", "service": "gw", "port": 9190}
	  ]
	}`
	e, err := parseKCLEntities([]byte(bundle))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	targets := extractSmokeTargets(e)
	if len(targets) != 4 {
		t.Fatalf("want 4 targets (all gateways probed), got %d: %+v", len(targets), targets)
	}
	byName := map[string]smokeTarget{}
	gwByHost := map[string]string{}
	for _, tgt := range targets {
		byName[tgt.RouteName] = tgt
		gwByHost[tgt.Host] = tgt.Gateway
	}
	// Hostless routes inherited their gateway's host.
	if got := byName["reliant-api"].Host; got != "staging.reliantapi.com" {
		t.Errorf("reliant-api inherited host = %q, want gateway host", got)
	}
	if got := byName["admin-server"].Host; got != "admin-staging.reliantapi.com" {
		t.Errorf("admin-server inherited host = %q, want gateway host", got)
	}
	if got := byName["daemon-gateway"].Host; got != "gateway-staging.reliantapi.com" {
		t.Errorf("daemon-gateway inherited host = %q, want gateway host", got)
	}
	// All three gateways are represented (multi-gateway extraction).
	gws := map[string]bool{}
	for _, tgt := range targets {
		gws[tgt.Gateway] = true
	}
	for _, want := range []string{"public", "public-admin", "public-grpc"} {
		if !gws[want] {
			t.Errorf("gateway %s has no probed route", want)
		}
	}
	// CORS scoped to the admin gateway's host only.
	if got := byName["admin-server"].Origin; got != "https://reliant-staging.web.app" {
		t.Errorf("admin-server origin = %q, want frontend origin", got)
	}
	for _, name := range []string{"reliant-api", "workspace-proxy", "daemon-gateway"} {
		if got := byName[name].Origin; got != "" {
			t.Errorf("route %s origin = %q, want empty", name, got)
		}
	}
}

// TestExtractSmokeTargets_NoFrontendAPIURL: when no frontend declares an
// API URL env var, CORS is skipped for every route (no origin set), even
// though a Firebase origin is derivable — we never guess the API host.
func TestExtractSmokeTargets_NoFrontendAPIURL(t *testing.T) {
	bundle := `{
	  "frontends": [
	    {"name": "admin-web", "type": "nextjs", "path": "frontend",
	     "runtime": {"type": "firebase", "project": "p", "site": "reliant-preprod"}}
	  ],
	  "gateways": [{"name": "edge", "host": ""}],
	  "http_routes": [
	    {"name": "admin", "gateway": "edge", "service": "admin", "port": 8090, "host": "admin.example.com", "path": "/"}
	  ]
	}`
	e, _ := parseKCLEntities([]byte(bundle))
	targets := extractSmokeTargets(e)
	if len(targets) != 1 {
		t.Fatalf("want 1 target, got %d", len(targets))
	}
	if targets[0].Origin != "" {
		t.Errorf("origin = %q, want empty (no frontend API URL to scope CORS)", targets[0].Origin)
	}
}

func TestFrontendAPIHosts(t *testing.T) {
	bundle := `{"frontends":[
	  {"name":"a","type":"vite-spa","path":"f","env_vars":[
	    {"name":"VITE_API_URL","value":"https://api.example.com"},
	    {"name":"VITE_CONTROL_PLANE_API_URL","value":"https://cp.example.com:8443"},
	    {"name":"NEXT_PUBLIC_API_URL","value":"admin.example.com"},
	    {"name":"OTHER","value":"https://ignored.example.com"}]}]}`
	e, _ := parseKCLEntities([]byte(bundle))
	hosts := frontendAPIHosts(e)
	for _, want := range []string{"api.example.com", "cp.example.com", "admin.example.com"} {
		if _, ok := hosts[want]; !ok {
			t.Errorf("missing API host %q in %v", want, hosts)
		}
	}
	if _, ok := hosts["ignored.example.com"]; ok {
		t.Errorf("non-API env var host should be ignored")
	}
}

func TestFrontendOrigin(t *testing.T) {
	e, _ := parseKCLEntities([]byte(sampleSmokeBundle))
	if got := frontendOrigin(e); got != "https://reliant-preprod.web.app" {
		t.Errorf("frontendOrigin = %q", got)
	}

	// No frontend -> no origin (CORS skipped, not failed).
	none, _ := parseKCLEntities([]byte(`{"gateways":[{"name":"g"}]}`))
	if got := frontendOrigin(none); got != "" {
		t.Errorf("frontendOrigin (no frontend) = %q, want empty", got)
	}
}

func TestNormalizeSmokePath(t *testing.T) {
	cases := map[string]string{
		"":        "/",
		"/":       "/",
		"/v1/foo": "/v1/foo",
		"v1/foo":  "/v1/foo",
		"  /x  ":  "/x",
	}
	for in, want := range cases {
		if got := normalizeSmokePath(in); got != want {
			t.Errorf("normalizeSmokePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeHostFor_Wildcard(t *testing.T) {
	cases := map[string]string{
		"preprod.reliantapi.com":              "preprod.reliantapi.com",
		"*.workspaces-preprod.reliantapi.com": "smoke-probe.workspaces-preprod.reliantapi.com",
		"  *.example.com  ":                   "smoke-probe.example.com",
	}
	for in, want := range cases {
		if got := probeHostFor(in); got != want {
			t.Errorf("probeHostFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractSmokeTargets_WildcardProbeHost(t *testing.T) {
	bundle := `{"gateways":[{"name":"g"}],"http_routes":[
	  {"name":"ws","gateway":"g","service":"proxy","host":"*.ws.example.com","path":"/"}]}`
	e, _ := parseKCLEntities([]byte(bundle))
	targets := extractSmokeTargets(e)
	if len(targets) != 1 {
		t.Fatalf("want 1 target, got %d", len(targets))
	}
	if targets[0].Host != "*.ws.example.com" {
		t.Errorf("display host = %q, want literal wildcard", targets[0].Host)
	}
	if targets[0].ProbeHost != "smoke-probe.ws.example.com" {
		t.Errorf("probe host = %q, want concrete sample subdomain", targets[0].ProbeHost)
	}
}

func TestClassifyResponse(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		ct         string
		body       string
		wantStatus smokeStatus
		wantReason string
	}{
		{
			name:       "plain go-mux 404 is a misroute warn",
			status:     404,
			ct:         "text/plain; charset=utf-8",
			body:       "404 page not found\n",
			wantStatus: smokeStatusWarn,
			wantReason: smokeReasonMisroute,
		},
		{
			name:       "structured JSON 404 is a pass (backend answered)",
			status:     404,
			ct:         "application/json",
			body:       `{"code":"not_found"}`,
			wantStatus: smokeStatusPass,
			wantReason: smokeReasonReached,
		},
		{
			name:       "connect 415 to a bare probe is a pass",
			status:     415,
			ct:         "application/json",
			body:       `{"code":"unimplemented"}`,
			wantStatus: smokeStatusPass,
			wantReason: smokeReasonReached,
		},
		{
			name:       "401 unauthorized is a pass",
			status:     401,
			ct:         "application/json",
			body:       `{"code":"unauthenticated"}`,
			wantStatus: smokeStatusPass,
			wantReason: smokeReasonReached,
		},
		{
			name:       "200 is a pass",
			status:     200,
			ct:         "text/html",
			body:       "<html>",
			wantStatus: smokeStatusPass,
			wantReason: smokeReasonReached,
		},
		{
			name:       "404 text/plain but custom body is NOT the go-mux sentinel -> pass",
			status:     404,
			ct:         "text/plain",
			body:       "sorry, no such page",
			wantStatus: smokeStatusPass,
			wantReason: smokeReasonReached,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyResponse(tc.status, tc.ct, tc.body)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (detail: %s)", got.Status, tc.wantStatus, got.Detail)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %s, want %s", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestClassifyResponseForPath_RootVsSubpath(t *testing.T) {
	// A plain Go-mux 404 at the ROOT path is PASS (expected for a
	// Connect/RPC backend — root isn't a served path, the 404 still
	// proves the backend was reached).
	root := classifyResponseForPath(404, "text/plain; charset=utf-8", "404 page not found\n", "/")
	if root.Status != smokeStatusPass || root.Reason != smokeReasonReached {
		t.Errorf("root plain-404: got %s/%s, want PASS/reached-backend", root.Status, root.Reason)
	}

	// The same plain 404 at a DECLARED sub-path is the misroute WARN.
	sub := classifyResponseForPath(404, "text/plain; charset=utf-8", "404 page not found\n", "/reliant.v1.Thing/Method")
	if sub.Status != smokeStatusWarn || sub.Reason != smokeReasonMisroute {
		t.Errorf("subpath plain-404: got %s/%s, want WARN/likely-misroute", sub.Status, sub.Reason)
	}

	// An empty path is treated as root.
	empty := classifyResponseForPath(404, "text/plain", "404 page not found", "")
	if empty.Status != smokeStatusPass {
		t.Errorf("empty path plain-404: got %s, want PASS", empty.Status)
	}
}

func TestClassifyTransportError(t *testing.T) {
	res := classifyTransportError(errString("tls: handshake failure"))
	if res.Status != smokeStatusFail || res.Reason != smokeReasonTLS {
		t.Errorf("want FAIL/tls-transport, got %s/%s", res.Status, res.Reason)
	}
	if res.Detail != "TLS handshake failed" {
		t.Errorf("detail = %q", res.Detail)
	}
}

func TestApplyCORSVerdict(t *testing.T) {
	pass := classifyResponse(200, "application/json", "{}")

	// No origin -> CORS not asserted, result unchanged.
	if got := applyCORSVerdict(pass, "", http.Header{}); got.Status != smokeStatusPass {
		t.Errorf("no-origin: status = %s, want PASS", got.Status)
	}

	// Origin set, ACAO present -> stays PASS.
	withACAO := http.Header{"Access-Control-Allow-Origin": {"*"}}
	if got := applyCORSVerdict(pass, "https://app.web.app", withACAO); got.Status != smokeStatusPass {
		t.Errorf("acao-present: status = %s, want PASS", got.Status)
	}

	// Origin set, ACAO missing -> escalates to FAIL/cors-missing.
	got := applyCORSVerdict(pass, "https://app.web.app", http.Header{})
	if got.Status != smokeStatusFail || got.Reason != smokeReasonCORS {
		t.Errorf("acao-missing: got %s/%s, want FAIL/cors-missing", got.Status, got.Reason)
	}

	// CORS is only asserted on PASS — a WARN result is left alone even
	// with an origin and missing ACAO (the misroute is the real issue).
	warn := classifyResponse(404, "text/plain", "404 page not found")
	if got := applyCORSVerdict(warn, "https://app.web.app", http.Header{}); got.Status != smokeStatusWarn {
		t.Errorf("warn untouched by CORS: status = %s, want WARN", got.Status)
	}
}

func TestSummarizeSmoke(t *testing.T) {
	results := []smokeRouteResult{
		{Status: smokeStatusPass},
		{Status: smokeStatusPass},
		{Status: smokeStatusWarn},
		{Status: smokeStatusFail},
	}
	s := summarizeSmoke(results)
	if s.Pass != 2 || s.Warn != 1 || s.Fail != 1 || !s.AnyFail {
		t.Errorf("summary = %+v", s)
	}

	clean := summarizeSmoke([]smokeRouteResult{{Status: smokeStatusPass}})
	if clean.AnyFail {
		t.Errorf("clean run should not report AnyFail")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
