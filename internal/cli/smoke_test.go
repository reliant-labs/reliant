package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// probeAgainstServer points probeRoute at an httptest TLS server: it
// dials the server's real IP:port (via the gateway-IP --resolve seam) but
// presents the route host as SNI. The server's self-signed cert is added
// to a private root pool so a genuine TLS handshake succeeds — this
// exercises the real dialer + crypto/tls + classifier path end-to-end.
func probeAgainstServer(t *testing.T, srv *httptest.Server, host, path, origin string) smokeRouteResult {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	gwIP := u.Hostname()
	gwPort := u.Port()

	// Build a client like smokeHTTPClient but (a) trusting the test
	// server's cert and (b) dialing the server's actual port (httptest
	// picks a random port, not 443).
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(gwIP, gwPort))
			},
			TLSClientConfig: &tls.Config{ServerName: host, RootCAs: pool},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+host+path, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		return classifyTransportError(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	res := classifyResponse(resp.StatusCode, resp.Header.Get("Content-Type"), string(body[:n]))
	return applyCORSVerdict(res, origin, resp.Header)
}

func TestProbe_ReachesBackend_Pass(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401) // Connect endpoint to a bare probe
		_, _ = w.Write([]byte(`{"code":"unauthenticated"}`))
	}))
	defer srv.Close()

	res := probeAgainstServer(t, srv, "api.example.com", "/", "")
	if res.Status != smokeStatusPass {
		t.Fatalf("want PASS, got %s (%s)", res.Status, res.Detail)
	}
	if res.StatusCode != 401 {
		t.Errorf("status code = %d", res.StatusCode)
	}
}

func TestProbe_PlainGoMux404_Warn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // text/plain "404 page not found"
	}))
	defer srv.Close()

	res := probeAgainstServer(t, srv, "api.example.com", "/reliant.v1.Thing/Method", "")
	if res.Status != smokeStatusWarn || res.Reason != smokeReasonMisroute {
		t.Fatalf("want WARN/misroute, got %s/%s", res.Status, res.Reason)
	}
}

func TestProbe_TLSHandshakeFailure_Fail(t *testing.T) {
	// A TLS server whose cert is for a DIFFERENT name than the SNI/host
	// the probe presents, and we do NOT trust it -> the real
	// smokeHTTPClient (validation on) must surface a TLS error. We use the
	// production client here (not the trusting test client) to prove the
	// stuck-cert -> tls-transport FAIL path.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	client := smokeHTTPClient("preprod.reliantapi.com", u.Hostname(), 5*time.Second)
	// smokeHTTPClient dials gatewayIP:443; the test server isn't on 443,
	// so dial would fail for the wrong reason. Re-wrap the transport to
	// dial the server's real port while keeping validation ON.
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, u.Host)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://preprod.reliantapi.com/", nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected TLS handshake error against untrusted cert, got none")
	}
	res := classifyTransportError(err)
	if res.Status != smokeStatusFail || res.Reason != smokeReasonTLS {
		t.Fatalf("want FAIL/tls-transport, got %s/%s (%v)", res.Status, res.Reason, err)
	}
}

func TestProbe_MissingCORS_Fail(t *testing.T) {
	// Backend answers structurally (PASS on transport) but sets no
	// Access-Control-Allow-Origin -> CORS FAIL when an origin is probed.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	res := probeAgainstServer(t, srv, "api.example.com", "/api", "https://admin-preprod.web.app")
	if res.Status != smokeStatusFail || res.Reason != smokeReasonCORS {
		t.Fatalf("want FAIL/cors-missing, got %s/%s (%s)", res.Status, res.Reason, res.Detail)
	}
}

func TestProbe_PresentCORS_Pass(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	res := probeAgainstServer(t, srv, "api.example.com", "/api", "https://admin-preprod.web.app")
	if res.Status != smokeStatusPass {
		t.Fatalf("want PASS with CORS ok, got %s (%s)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "CORS ok") {
		t.Errorf("detail should note CORS ok: %q", res.Detail)
	}
}

// TestRunSmokeWith exercises the orchestration with injected resolver +
// probe: render via fixture, resolve gateway IPs, classify, and verify
// the exit verdict + table output.
func TestRunSmokeWith(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, sampleSmokeBundle))

	resolve := func(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
		return "203.0.113.10", nil
	}
	// Probe stub: PASS everything.
	probe := func(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
		return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200, Detail: "ok"}
	}

	var buf bytes.Buffer
	err := runSmokeWith(context.Background(), "preprod", smokeOptions{}, resolve, probe, &buf)
	if err != nil {
		t.Fatalf("runSmokeWith (all-pass): %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "4 PASS") {
		t.Errorf("expected 4 PASS in summary:\n%s", out)
	}
}

func TestRunSmokeWith_GatewayNoAddress_Fail(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, sampleSmokeBundle))

	// Resolver returns empty IP (gateway not programmed).
	resolve := func(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
		return "", nil
	}
	probe := func(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
		t.Fatalf("probe should not run when gateway has no address")
		return smokeRouteResult{}
	}

	var buf bytes.Buffer
	err := runSmokeWith(context.Background(), "preprod", smokeOptions{}, resolve, probe, &buf)
	if err == nil {
		t.Fatalf("expected non-nil error (FAIL) when gateway has no address:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), smokeReasonNoAddr) {
		t.Errorf("expected gateway-no-address reason in output:\n%s", buf.String())
	}
}

func TestRunSmokeWith_JSON(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, sampleSmokeBundle))
	resolve := func(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
		return "203.0.113.10", nil
	}
	probe := func(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
		return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200}
	}
	var buf bytes.Buffer
	if err := runSmokeWith(context.Background(), "preprod", smokeOptions{jsonOut: true}, resolve, probe, &buf); err != nil {
		t.Fatalf("runSmokeWith json: %v", err)
	}
	var rep smokeJSONReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("json output not parseable: %v\n%s", err, buf.String())
	}
	if !rep.Summary.OK || rep.Summary.Pass != 4 || len(rep.Routes) != 4 {
		t.Errorf("json report unexpected: %+v", rep.Summary)
	}
}

func TestRunSmokeWith_NoRoutes(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{
  "output": {
    "workloads": [
      {
        "name": "s",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`))
	resolve := func(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
		t.Fatalf("resolve should not run with no routes")
		return "", nil
	}
	probe := func(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
		return smokeRouteResult{}
	}
	var buf bytes.Buffer
	if err := runSmokeWith(context.Background(), "dev", smokeOptions{}, resolve, probe, &buf); err != nil {
		t.Fatalf("no-routes env should exit 0, got: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to probe") {
		t.Errorf("expected nothing-to-probe message:\n%s", buf.String())
	}
}
