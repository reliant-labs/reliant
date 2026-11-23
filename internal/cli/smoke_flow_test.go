package cli

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// flowEntities builds a rendered bundle the way the KCL render produces one:
// a gateway listener on `listenerPort`, and a daemon-gateway workload that
// declares a flow check. The check names a PATH only — the URL is resolved
// from where the env serves it.
func flowEntities(t *testing.T, listenerPort int, routePath string, runtime string) *KCLEntities {
	t.Helper()
	body := `{
  "output": {
    "gateways": [{"name": "public", "listeners": [{"name": "http", "port": ` + strconv.Itoa(listenerPort) + `, "protocol": "HTTP"}]}],
    "http_routes": [
      {"name": "catchall", "gateway": "public", "listener": "http", "service": "workspace-proxy", "port": 8080, "path": "/"},
      {"name": "gw-health", "gateway": "public", "listener": "http", "service": "daemon-gateway", "port": 8081, "path": "` + routePath + `"}
    ],
    "workloads": [
      {
        "name": "daemon-gateway", "kind": "service",
        "flow_checks": [{"path": "/flow-health", "description": "every Ready daemon is attached"}],
        "runtime": ` + runtime + `,
        "spec": {"kind": "service"}
      }
    ]
  }
}`
	e, err := parseKCLEntities([]byte(body))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	return e
}

const clusterRuntime = `{"type": "cluster", "cluster": "k3d", "namespace": "ns"}`

// TestResolveFlowChecks_FollowsTheEnvsListenerPort is the headline: the URL is
// never written down, so it cannot go stale. Re-point the env's listener and
// the resolved URL moves with it. forge.yaml used to carry
// `url: http://localhost:28080/flow-health`, a literal that silently pointed at
// nothing the moment the listener port changed.
func TestResolveFlowChecks_FollowsTheEnvsListenerPort(t *testing.T) {
	for _, port := range []int{28080, 38080} {
		got := resolveFlowChecks(flowEntities(t, port, "/flow-health", clusterRuntime), nil)
		if len(got) != 1 {
			t.Fatalf("want 1 resolved check, got %+v", got)
		}
		want := "http://localhost:" + strconv.Itoa(port) + "/flow-health"
		if got[0].URL != want {
			t.Errorf("listener :%d: URL = %q, want %q", port, got[0].URL, want)
		}
		if got[0].Misdeclared != "" {
			t.Errorf("a routed check must not be misdeclared: %q", got[0].Misdeclared)
		}
	}
}

// A route that does not cover the check's path does not forward it, so the check
// is not reachable through that route — and smoke says so instead of probing the
// catch-all route's backend (a different workload).
func TestResolveFlowChecks_RouteMustCoverThePath(t *testing.T) {
	got := resolveFlowChecks(flowEntities(t, 28080, "/elsewhere", clusterRuntime), nil)
	if len(got) != 1 {
		t.Fatalf("want 1 check, got %+v", got)
	}
	if got[0].URL != "" {
		t.Errorf("a route that does not cover /flow-health must not resolve it, got %q", got[0].URL)
	}
	if !strings.Contains(got[0].Misdeclared, "daemon-gateway") || !strings.Contains(got[0].Misdeclared, "/flow-health") {
		t.Errorf("the misdeclared reason must name the workload and path, got %q", got[0].Misdeclared)
	}
}

// A host workload is probed on its listen port, with no route involved.
func TestResolveFlowChecks_HostWorkloadUsesItsListenPort(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output": {"workloads": [{
      "name": "api", "kind": "service",
      "flow_checks": [{"path": "/healthz/flow", "name": "api-flow"}],
      "runtime": {"type": "host", "runner": "go-run", "listen_ports": [4321]},
      "spec": {"kind": "service"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := resolveFlowChecks(e, nil)
	if len(got) != 1 || got[0].URL != "http://localhost:4321/healthz/flow" || got[0].Name != "api-flow" {
		t.Fatalf("host workload check resolved to %+v", got)
	}
}

// A hosted workload's URL is allocated by the platform, so it comes from the
// control plane's status.
func TestResolveFlowChecks_HostedWorkloadUsesPlatformURL(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output": {"workloads": [{
      "name": "api", "kind": "service",
      "flow_checks": [{"path": "/flow-health"}],
      "runtime": {"type": "hosted", "platform": "amd64"},
      "spec": {"kind": "service"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := resolveFlowChecks(e, map[string]string{"api": "https://api-abc.example.dev/"})
	if len(got) != 1 || got[0].URL != "https://api-abc.example.dev/flow-health" {
		t.Fatalf("hosted check resolved to %+v", got)
	}
	if again := resolveFlowChecks(e, nil); again[0].Misdeclared == "" {
		t.Error("a hosted workload with no platform URL yet must be reported, not skipped")
	}
}

// The check exists where the workload does: an env that does not run the
// workload runs no check, with no env filter to keep in step.
func TestResolveFlowChecks_NoWorkloadNoCheck(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output": {"workloads": [{
      "name": "other", "kind": "service",
      "runtime": {"type": "cluster", "cluster": "c", "namespace": "n"}, "spec": {"kind": "service"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveFlowChecks(e, nil); len(got) != 0 {
		t.Fatalf("an env without the owning workload must resolve no checks, got %+v", got)
	}
	if got := resolveFlowChecks(nil, nil); got != nil {
		t.Fatalf("nil entities must resolve nothing, got %+v", got)
	}
}

func TestPathCovers(t *testing.T) {
	for _, tc := range []struct {
		prefix, path string
		want         bool
	}{
		{"/", "/flow-health", true},
		{"/flow-health", "/flow-health", true},
		{"/flow-health/", "/flow-health", true},
		{"/flow", "/flow-health", false}, // a segment prefix, not a string prefix
		{"/api", "/api/flow-health", true},
		{"/other", "/flow-health", false},
	} {
		if got := pathCovers(tc.prefix, tc.path); got != tc.want {
			t.Errorf("pathCovers(%q, %q) = %v, want %v", tc.prefix, tc.path, got, tc.want)
		}
	}
}

// TestRunSmokeFlowChecks_PassAndFail exercises the app-flow-check phase in
// isolation: an endpoint that returns 200 → PASS, one that returns 503 → FAIL.
// A 503 flow endpoint produces a FAIL row that turns the smoke run RED.
func TestRunSmokeFlowChecks_PassAndFail(t *testing.T) {
	checks := []flowCheck{
		{Name: "healthy-flow", URL: "http://svc/flow-health"},
		{Name: "broken-flow", URL: "http://other/flow-health"},
	}
	probe := func(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
		if strings.Contains(url, "other") {
			return 503, `{"status":"unhealthy","daemons":2,"unattached":1}`, nil
		}
		return 200, `{"status":"healthy","daemons":2,"unattached":0}`, nil
	}

	results := runSmokeFlowChecks(context.Background(), checks, probe, time.Second)
	if len(results) != 2 {
		t.Fatalf("expected 2 flow results, got %d", len(results))
	}
	// Sorted by name: broken-flow first.
	if results[0].Target.RouteName != "broken-flow" || results[0].Status != smokeStatusFail {
		t.Errorf("expected broken-flow FAIL, got %+v", results[0])
	}
	if results[0].Reason != smokeFlowReasonUnhealthy {
		t.Errorf("expected reason %q, got %q", smokeFlowReasonUnhealthy, results[0].Reason)
	}
	if !strings.Contains(results[0].Detail, "unattached") {
		t.Errorf("expected aggregate body quoted in detail, got %q", results[0].Detail)
	}
	if results[1].Target.RouteName != "healthy-flow" || results[1].Status != smokeStatusPass {
		t.Errorf("expected healthy-flow PASS, got %+v", results[1])
	}
	summary := summarizeSmoke(results)
	if !summary.AnyFail || summary.Fail != 1 || summary.Pass != 1 {
		t.Errorf("expected 1 PASS / 1 FAIL / AnyFail, got %+v", summary)
	}
}

func TestRunSmokeFlowChecks_Unreachable(t *testing.T) {
	probe := func(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
		return 0, "", context.DeadlineExceeded
	}
	results := runSmokeFlowChecks(context.Background(), []flowCheck{{Name: "down-flow", URL: "http://nope/flow-health"}}, probe, time.Second)
	if len(results) != 1 || results[0].Status != smokeStatusFail || results[0].Reason != smokeFlowReasonUnreach {
		t.Fatalf("expected 1 unreachable FAIL, got %+v", results)
	}
}

// A check forge could not resolve a URL for is a FAIL, never a silent skip: a
// skipped assertion is exactly the green-while-broken failure this phase closes.
func TestRunSmokeFlowChecks_MisdeclaredFails(t *testing.T) {
	probe := func(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
		t.Fatal("a check with no URL must not be probed")
		return 0, "", nil
	}
	results := runSmokeFlowChecks(context.Background(),
		[]flowCheck{{Name: "orphan", Misdeclared: "nothing in this env reaches it"}}, probe, time.Second)
	if len(results) != 1 || results[0].Status != smokeStatusFail || results[0].Reason != smokeFlowReasonErr {
		t.Fatalf("expected a misdeclared FAIL, got %+v", results)
	}
	if !strings.Contains(results[0].Detail, "nothing in this env reaches it") {
		t.Errorf("detail must carry the fix, got %q", results[0].Detail)
	}
}

// flowSmokeEnv renders flowSmokeBundle with the gateway listener on a port a
// real socket holds, so the dev port probe the smoke dispatcher runs reaches
// something: the ROUTE half of the verdict is then green for a real reason, and
// whatever the flow check says is the only thing that can move the exit code.
func flowSmokeEnv(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// READ THE REQUEST FIRST. Answering on accept raced the
			// client's write: when the response landed before the request
			// went out, net/http reported "Unsolicited response received on
			// idle HTTP channel" and failed the probe as port-unreachable —
			// a flake that only showed under load (seen in a full -race run
			// of the package on a busy machine).
			_, _ = http.ReadRequest(bufio.NewReader(c))
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	body := strings.ReplaceAll(flowSmokeBundle, "28080", strconv.Itoa(port))
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, body))
}

func noGateway(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
	return "", nil
}

func noRouteProbe(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
	return smokeRouteResult{}
}

// TestRunSmokeWith_FlowCheckFailsOverall is the integration guarantee: the check
// is declared on a workload in the RENDER, every ROUTE passes (a real listener
// answers), but the flow endpoint returns 503 — so the WHOLE smoke run exits
// non-zero. This is the green-while-broken regression the phase closes.
func TestRunSmokeWith_FlowCheckFailsOverall(t *testing.T) {
	flowSmokeEnv(t)

	var probedURL string
	flowProbe := func(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
		probedURL = url
		return 503, `{"status":"unhealthy"}`, nil
	}

	var buf bytes.Buffer
	err := runSmokeWith(context.Background(), "dev", smokeOptions{flowProbe: flowProbe}, noGateway, noRouteProbe, &buf)
	if err == nil {
		t.Fatalf("expected non-nil error: routes pass but flow check 503'd:\n%s", buf.String())
	}
	if !strings.HasPrefix(probedURL, "http://localhost:") || !strings.HasSuffix(probedURL, "/flow-health") {
		t.Errorf("flow check probed %q, want a URL resolved from the gateway listener", probedURL)
	}
	out := buf.String()
	if !strings.Contains(out, "daemon-gateway:/flow-health") || !strings.Contains(out, "UNHEALTHY") {
		t.Errorf("expected the flow FAIL row in output:\n%s", out)
	}
	if !strings.Contains(out, "PASS    gw-health") {
		t.Errorf("the route half must be green for the right reason, got:\n%s", out)
	}
	if !strings.Contains(out, "app-flow checks") {
		t.Errorf("expected the app-flow checks section in output:\n%s", out)
	}
}

// When routes and the flow check both pass, smoke stays GREEN and the verdict
// counts the flow check.
func TestRunSmokeWith_FlowCheckHealthyStaysGreen(t *testing.T) {
	flowSmokeEnv(t)

	flowProbe := func(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
		return 200, `{"status":"healthy","daemons":2,"unattached":0}`, nil
	}

	var buf bytes.Buffer
	err := runSmokeWith(context.Background(), "dev", smokeOptions{flowProbe: flowProbe}, noGateway, noRouteProbe, &buf)
	if err != nil {
		t.Fatalf("expected GREEN when routes + flow pass: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "every app-flow check is healthy") {
		t.Errorf("expected healthy combined verdict:\n%s", buf.String())
	}
}

// flowSmokeBundle is a dev-shaped render: one host-mapped listener (so the
// port-based smoke path runs) and a daemon-gateway workload declaring a flow
// check that a route on that listener forwards.
const flowSmokeBundle = `{
  "output": {
    "gateways": [{"name": "public", "listeners": [{"name": "http", "port": 28080, "protocol": "HTTP"}]}],
    "http_routes": [
      {"name": "gw-health", "gateway": "public", "listener": "http", "service": "daemon-gateway", "port": 8081, "path": "/flow-health"}
    ],
    "workloads": [
      {
        "name": "daemon-gateway", "kind": "service",
        "flow_checks": [{"path": "/flow-health", "description": "every Ready daemon is attached"}],
        "runtime": {"type": "cluster", "cluster": "k3d", "namespace": "ns"},
        "spec": {"kind": "service"}
      }
    ]
  }
}`
