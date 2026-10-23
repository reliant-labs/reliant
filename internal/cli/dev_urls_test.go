package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildIngressURLs_HTTPListener(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Listeners: []GatewayListenerEntity{
				{Name: "http", Port: 18080, Protocol: "HTTP"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name:     "workspace-proxy",
			Gateway:  "public",
			Listener: "http",
			Service:  "workspace-proxy",
			Port:     8080,
		}},
	}

	urls := buildIngressURLs(entities)
	if len(urls) != 1 {
		t.Fatalf("want 1 url, got %d", len(urls))
	}
	u := urls[0]
	if u.URL != "http://localhost:18080/" {
		t.Errorf("URL = %q, want http://localhost:18080/", u.URL)
	}
	if u.Kind != "HTTPRoute" {
		t.Errorf("Kind = %q, want HTTPRoute", u.Kind)
	}
	if u.Warning != "" {
		t.Errorf("unexpected warning: %q", u.Warning)
	}
}

func TestBuildIngressURLs_HTTPSListenerUsesHTTPSScheme(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Host: "api.example.com",
			Listeners: []GatewayListenerEntity{
				{Name: "https", Port: 443, Protocol: "HTTPS"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name:     "api",
			Gateway:  "public",
			Listener: "https",
			Service:  "api",
			Port:     8080,
		}},
	}
	urls := buildIngressURLs(entities)
	if urls[0].URL != "https://api.example.com:443/" {
		t.Errorf("URL = %q, want https://api.example.com:443/", urls[0].URL)
	}
}

func TestBuildIngressURLs_RouteHostOverridesGatewayHost(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Host: "api.example.com",
			Listeners: []GatewayListenerEntity{
				{Name: "http", Port: 80, Protocol: "HTTP"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name:     "admin",
			Gateway:  "public",
			Listener: "http",
			Service:  "admin",
			Port:     7000,
			Host:     "admin.example.com",
		}},
	}
	urls := buildIngressURLs(entities)
	if !strings.Contains(urls[0].URL, "admin.example.com") {
		t.Errorf("URL = %q, want admin.example.com host", urls[0].URL)
	}
}

func TestBuildIngressURLs_GRPCRouteForcesGRPCScheme(t *testing.T) {
	// listener.Protocol is H2C but the route is a GRPCRoute — the spec
	// says scheme must be grpc:// regardless of listener.Protocol.
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Listeners: []GatewayListenerEntity{
				{Name: "grpc", Port: 19190, Protocol: "H2C"},
			},
		}},
		GRPCRoutes: []GRPCRouteEntity{{
			Name:     "daemon-gateway",
			Gateway:  "public",
			Listener: "grpc",
			Service:  "daemon-gateway",
			Port:     19190,
		}},
	}
	urls := buildIngressURLs(entities)
	if len(urls) != 1 {
		t.Fatalf("want 1 url, got %d", len(urls))
	}
	if !strings.HasPrefix(urls[0].URL, "grpc://") {
		t.Errorf("URL = %q, want grpc:// prefix", urls[0].URL)
	}
	if urls[0].Kind != "GRPCRoute" {
		t.Errorf("Kind = %q, want GRPCRoute", urls[0].Kind)
	}

	// Same fixture but with a HTTPS listener — still grpc://, because
	// the kind is the discriminator.
	entities.Gateways[0].Listeners[0].Protocol = "HTTPS"
	urls = buildIngressURLs(entities)
	if !strings.HasPrefix(urls[0].URL, "grpc://") {
		t.Errorf("URL = %q, want grpc:// (even with HTTPS listener)", urls[0].URL)
	}
}

func TestBuildIngressURLs_PathPrefixConcat(t *testing.T) {
	cases := []struct {
		name       string
		pathPrefix string
		routePath  string
		want       string // path portion only
	}{
		{"both-empty", "", "", "/"},
		{"prefix-only", "/api", "", "/api"},
		{"route-only", "", "/v1/users", "/v1/users"},
		{"clean-join", "/api", "/v1/users", "/api/v1/users"},
		{"double-slash-collapse", "/api/", "/v1/users", "/api/v1/users"},
		{"trailing-slash-only", "/api/", "", "/api/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entities := &KCLEntities{
				Gateways: []GatewayEntity{{
					Name: "public",
					Listeners: []GatewayListenerEntity{
						{Name: "http", Port: 80, Protocol: "HTTP", PathPrefix: tc.pathPrefix},
					},
				}},
				HTTPRoutes: []HTTPRouteEntity{{
					Name: "r", Gateway: "public", Listener: "http",
					Service: "s", Port: 80, Path: tc.routePath,
				}},
			}
			urls := buildIngressURLs(entities)
			got := urls[0].URL
			wantSuffix := tc.want
			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("URL = %q, want path suffix %q", got, wantSuffix)
			}
			if strings.Contains(got[len("http://"):], "//") {
				t.Errorf("URL %q contains // — double-slash collapse failed", got)
			}
		})
	}
}

func TestBuildIngressURLs_EmptyEntities(t *testing.T) {
	if urls := buildIngressURLs(&KCLEntities{}); len(urls) != 0 {
		t.Errorf("want 0 urls, got %d", len(urls))
	}
	if urls := buildIngressURLs(nil); len(urls) != 0 {
		t.Errorf("want 0 urls for nil input, got %d", len(urls))
	}
}

func TestBuildIngressURLs_UnresolvedListenerWarns(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Listeners: []GatewayListenerEntity{
				{Name: "http", Port: 80, Protocol: "HTTP"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name: "ghost", Gateway: "public", Listener: "does-not-exist",
		}},
	}
	urls := buildIngressURLs(entities)
	if len(urls) != 1 {
		t.Fatalf("want 1 url, got %d", len(urls))
	}
	if urls[0].Warning == "" {
		t.Error("want warning for missing listener")
	}
	if urls[0].URL != "" {
		t.Errorf("URL = %q, want empty when listener missing", urls[0].URL)
	}
}

func TestBuildIngressURLs_UnresolvedGatewayWarns(t *testing.T) {
	entities := &KCLEntities{
		HTTPRoutes: []HTTPRouteEntity{{
			Name: "orphan", Gateway: "nope", Listener: "http",
		}},
	}
	urls := buildIngressURLs(entities)
	if len(urls) != 1 || urls[0].Warning == "" {
		t.Fatalf("want a warning row, got %+v", urls)
	}
}

func TestRenderIngressURLs_GroupsByGateway(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Listeners: []GatewayListenerEntity{
				{Name: "http", Port: 18080, Protocol: "HTTP"},
				{Name: "grpc", Port: 19190, Protocol: "H2C"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name: "workspace-proxy", Gateway: "public", Listener: "http",
			Service: "workspace-proxy", Port: 8080,
		}},
		GRPCRoutes: []GRPCRouteEntity{{
			Name: "daemon-gateway", Gateway: "public", Listener: "grpc",
			Service: "daemon-gateway", Port: 19190,
		}},
	}
	urls := buildIngressURLs(entities)
	var buf bytes.Buffer
	renderIngressURLs(&buf, entities, urls)
	out := buf.String()
	for _, want := range []string{
		"public",
		"http (port 18080, HTTP)",
		"grpc (port 19190, H2C)",
		"workspace-proxy",
		"http://localhost:18080/",
		"grpc://localhost:19190/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestBuildIngressURLs_ListenerHostnamePrecedence pins the host
// resolution order route.Host > listener.Hostname > gateway.Host >
// "localhost". The listener rung is what makes a multi-hostname Gateway
// (one load balancer serving N hosts) print correct URLs: without it a
// route on the "admin" listener would be reported on the gateway's
// default host, which is not the host the cluster serves it on.
//
// This mirrors _route_host in kcl/lib/gateway.k. If the two disagree,
// `forge cluster urls` prints a URL that does not resolve.
func TestBuildIngressURLs_ListenerHostnamePrecedence(t *testing.T) {
	cases := []struct {
		name        string
		gatewayHost string
		listenerHos string
		routeHost   string
		want        string
	}{
		{"nothing-set-falls-back-to-localhost", "", "", "", "localhost"},
		{"gateway-host-only", "gw.example.com", "", "", "gw.example.com"},
		{"listener-beats-gateway", "gw.example.com", "l.example.com", "", "l.example.com"},
		{"route-beats-listener", "gw.example.com", "l.example.com", "r.example.com", "r.example.com"},
		{"route-beats-gateway", "gw.example.com", "", "r.example.com", "r.example.com"},
		{"listener-only", "", "l.example.com", "", "l.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entities := &KCLEntities{
				Gateways: []GatewayEntity{{
					Name: "public",
					Host: tc.gatewayHost,
					Listeners: []GatewayListenerEntity{
						{Name: "http", Port: 80, Protocol: "HTTP", Hostname: tc.listenerHos},
					},
				}},
				HTTPRoutes: []HTTPRouteEntity{{
					Name: "r", Gateway: "public", Listener: "http",
					Service: "s", Port: 80, Host: tc.routeHost,
				}},
			}
			urls := buildIngressURLs(entities)
			if len(urls) != 1 {
				t.Fatalf("got %d urls, want 1", len(urls))
			}
			want := "http://" + tc.want + ":80/"
			if urls[0].URL != want {
				t.Errorf("URL = %q, want %q", urls[0].URL, want)
			}
		})
	}
}

// TestBuildIngressURLs_MultiHostGatewayPerListener is the CLI-side twin
// of kcl/tests/positive_gateway_multi_host.k: ONE Gateway serving
// several hostnames, each on its own listener. Each route must be
// reported on its own listener's host.
func TestBuildIngressURLs_MultiHostGatewayPerListener(t *testing.T) {
	entities := &KCLEntities{
		Gateways: []GatewayEntity{{
			Name: "public",
			Listeners: []GatewayListenerEntity{
				{Name: "api", Port: 443, Protocol: "HTTPS", Hostname: "api.example.com"},
				{Name: "admin", Port: 8443, Protocol: "HTTPS", Hostname: "admin.example.com"},
			},
		}},
		HTTPRoutes: []HTTPRouteEntity{
			{Name: "api", Gateway: "public", Listener: "api", Service: "api", Port: 8080},
			{Name: "admin", Gateway: "public", Listener: "admin", Service: "admin", Port: 8081},
		},
	}
	got := map[string]string{}
	for _, u := range buildIngressURLs(entities) {
		got[u.Route] = u.URL
	}
	want := map[string]string{
		"api":   "https://api.example.com:443/",
		"admin": "https://admin.example.com:8443/",
	}
	for name, wantURL := range want {
		if got[name] != wantURL {
			t.Errorf("route %q URL = %q, want %q", name, got[name], wantURL)
		}
	}
}
