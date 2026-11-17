package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// devBundle is the dev (port-based, host-less) topology: a single `public`
// gateway whose listeners are host-mapped to localhost ports (http :28080,
// controller :28090, grpc/H2C :29190), host-less routes attached to each,
// and host services whose env URLs name the localhost infra ports
// (Postgres :5434, NATS :4222). This is the shape deploy/kcl/dev renders.
const devBundle = `{
  "output": {
    "gateways": [
      {
        "name": "public",
        "listeners": [
          {
            "name": "http",
            "port": 28080,
            "protocol": "HTTP"
          },
          {
            "name": "controller",
            "port": 28090,
            "protocol": "HTTP"
          },
          {
            "name": "grpc",
            "port": 29190,
            "protocol": "H2C"
          }
        ]
      }
    ],
    "http_routes": [
      {
        "name": "workspace-proxy",
        "gateway": "public",
        "listener": "http",
        "service": "workspace-proxy",
        "port": 8080
      },
      {
        "name": "workspace-controller",
        "gateway": "public",
        "listener": "controller",
        "service": "workspace-controller",
        "port": 9191
      }
    ],
    "grpc_routes": [
      {
        "name": "daemon-gateway",
        "gateway": "public",
        "listener": "grpc",
        "service": "daemon-gateway",
        "port": 9190
      }
    ],
    "workloads": [
      {
        "name": "admin-server",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "value": "postgres://postgres:postgres@localhost:5434/cp?sslmode=disable"
            },
            {
              "name": "NATS_URL",
              "value": "nats://localhost:4222"
            }
          ]
        }
      },
      {
        "name": "reliant-api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "value": "postgres://postgres:postgres@localhost:5434/reliant?sslmode=disable"
            }
          ]
        }
      },
      {
        "name": "workspace-proxy",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-control-plane",
          "namespace": "ns"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`

func TestExtractDevSmokeTargets(t *testing.T) {
	e, err := parseKCLEntities([]byte(devBundle))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	targets := extractDevSmokeTargets(e)

	byName := map[string]devSmokeTarget{}
	for _, tg := range targets {
		byName[tg.Name] = tg
	}

	// 2 http routes + 1 grpc route + 2 infra deps (postgres, nats; the
	// shared postgres :5434 referenced by BOTH host services dedups to one).
	if len(targets) != 5 {
		t.Fatalf("want 5 targets, got %d: %+v", len(targets), targets)
	}

	// HTTP listener route -> http probe on the listener's host port.
	if got := byName["workspace-proxy"]; got.Port != 28080 || got.Probe != "http" {
		t.Errorf("workspace-proxy = port %d/%s, want 28080/http", got.Port, got.Probe)
	}
	// Controller route on its dedicated HTTP listener -> :28090.
	if got := byName["workspace-controller"]; got.Port != 28090 || got.Probe != "http" {
		t.Errorf("workspace-controller = port %d/%s, want 28090/http", got.Port, got.Probe)
	}
	// gRPC route on an H2C listener -> tcp probe on :29190.
	if got := byName["daemon-gateway"]; got.Port != 29190 || got.Probe != "tcp" {
		t.Errorf("daemon-gateway = port %d/%s, want 29190/tcp", got.Port, got.Probe)
	}
	// Infra deps parsed from host env URLs, deduped, tcp-probed.
	if got := byName["postgres"]; got.Kind != "infra" || got.Port != 5434 || got.Probe != "tcp" {
		t.Errorf("postgres infra = %+v, want infra/5434/tcp", got)
	}
	if got := byName["nats"]; got.Kind != "infra" || got.Port != 4222 || got.Probe != "tcp" {
		t.Errorf("nats infra = %+v, want infra/4222/tcp", got)
	}
}

func TestLocalhostPortFromURL(t *testing.T) {
	cases := map[string]int{
		"postgres://postgres:postgres@localhost:5434/cp?sslmode=disable": 5434,
		"nats://localhost:4222": 4222,
		"http://127.0.0.1:8090": 8090,
		// In-cluster DNS host -> not a localhost dial, skip.
		"nats://host.k3d.internal:4222":          0,
		"postgres://db.svc.cluster.local:5432/x": 0,
		// No explicit port -> nothing to probe.
		"http://localhost": 0,
		"":                 0,
	}
	for in, want := range cases {
		if got := localhostPortFromURL(in); got != want {
			t.Errorf("localhostPortFromURL(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestProbeDevHTTP_ReachesBackend points the HTTP probe at a live local
// listener and asserts a structured response classifies PASS.
func TestProbeDevHTTP_ReachesBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	res := probeDevPort(context.Background(), devSmokeTarget{
		Kind: "http", Name: "x", Port: port, Path: "/", Probe: "http",
	}, 5*time.Second)
	if res.Status != smokeStatusPass {
		t.Fatalf("want PASS, got %s (%s)", res.Status, res.Detail)
	}
	if res.StatusCode != 401 {
		t.Errorf("status code = %d, want 401", res.StatusCode)
	}
}

// TestProbeDevTCP_Connects asserts a TCP probe against a live listener PASSes.
func TestProbeDevTCP_Connects(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := portOf(t, "http://"+ln.Addr().String())

	res := probeDevPort(context.Background(), devSmokeTarget{
		Kind: "infra", Name: "nats", Port: port, Probe: "tcp",
	}, 5*time.Second)
	if res.Status != smokeStatusPass || res.Reason != smokeReasonReached {
		t.Fatalf("want PASS/reached-backend, got %s/%s", res.Status, res.Reason)
	}
}

// TestProbeDevPort_Unreachable_Fail is the core regression: a dead port
// (nothing listening) must FAIL with the dev port-unreachable reason — this
// is the recurring dead :28090 controller breakage.
func TestProbeDevPort_Unreachable_Fail(t *testing.T) {
	// Grab a port, then close the listener so the dial is refused.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := portOf(t, "http://"+ln.Addr().String())
	ln.Close() // now nothing is listening on `port`

	res := probeDevPort(context.Background(), devSmokeTarget{
		Kind: "http", Name: "workspace-controller", Port: port, Path: "/", Probe: "http",
	}, 2*time.Second)
	if res.Status != smokeStatusFail || res.Reason != smokeReasonUnreachable {
		t.Fatalf("want FAIL/port-unreachable, got %s/%s (%s)", res.Status, res.Reason, res.Detail)
	}
}

// TestRunDevSmokeWith exercises the orchestration end-to-end with a stub
// probe: one FAIL must drive a non-nil error (non-zero exit) and the table
// must render the localhost addresses.
func TestRunDevSmokeWith(t *testing.T) {
	e, _ := parseKCLEntities([]byte(devBundle))

	// Stub: FAIL the controller (the dead :28090), PASS everything else.
	probe := func(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
		if target.Name == "workspace-controller" {
			return smokeRouteResult{Status: smokeStatusFail, Reason: smokeReasonUnreachable, Detail: "connection refused"}
		}
		return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200, Detail: "ok"}
	}

	var buf bytes.Buffer
	err := runDevSmokeWith(context.Background(), "dev", smokeOptions{}, e, probe, nil, &buf)
	if err == nil {
		t.Fatalf("expected non-nil error when a target FAILs:\n%s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "localhost:28090") {
		t.Errorf("expected controller localhost:28090 row in table:\n%s", out)
	}
	if !strings.Contains(out, "1 FAIL") {
		t.Errorf("expected 1 FAIL in summary:\n%s", out)
	}
}

func TestRunDevSmokeWith_AllPass(t *testing.T) {
	e, _ := parseKCLEntities([]byte(devBundle))
	probe := func(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
		return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200}
	}
	var buf bytes.Buffer
	if err := runDevSmokeWith(context.Background(), "dev", smokeOptions{}, e, probe, nil, &buf); err != nil {
		t.Fatalf("all-pass dev smoke should exit 0: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "5 PASS") {
		t.Errorf("expected 5 PASS:\n%s", buf.String())
	}
}

// TestRunDevSmokeWith_JSON asserts --json works for dev and is parseable.
func TestRunDevSmokeWith_JSON(t *testing.T) {
	e, _ := parseKCLEntities([]byte(devBundle))
	probe := func(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
		if target.Name == "workspace-controller" {
			return smokeRouteResult{Status: smokeStatusFail, Reason: smokeReasonUnreachable}
		}
		return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200}
	}
	var buf bytes.Buffer
	err := runDevSmokeWith(context.Background(), "dev", smokeOptions{jsonOut: true}, e, probe, nil, &buf)
	if err == nil {
		t.Fatalf("expected error (a FAIL) but json run returned nil:\n%s", buf.String())
	}
	var rep smokeJSONReport
	if jerr := json.Unmarshal(buf.Bytes(), &rep); jerr != nil {
		t.Fatalf("dev --json not parseable: %v\n%s", jerr, buf.String())
	}
	if rep.Summary.OK {
		t.Errorf("summary.ok should be false with a FAIL: %+v", rep.Summary)
	}
	if len(rep.Routes) != 5 || rep.Summary.Fail != 1 || rep.Summary.Pass != 4 {
		t.Errorf("unexpected json summary: %+v (routes=%d)", rep.Summary, len(rep.Routes))
	}
	// Host is projected as the concrete localhost:<port> address.
	var sawController bool
	for _, r := range rep.Routes {
		if r.RouteName == "workspace-controller" {
			sawController = true
			if r.Host != "localhost:28090" {
				t.Errorf("controller host = %q, want localhost:28090", r.Host)
			}
			if r.Result != "FAIL" {
				t.Errorf("controller result = %q, want FAIL", r.Result)
			}
		}
	}
	if !sawController {
		t.Error("controller route missing from json output")
	}
}

// TestHasDevPortTargets distinguishes the dev (port-based) topology from a
// cluster-internal env with no host-mapped ports.
func TestHasDevPortTargets(t *testing.T) {
	dev, _ := parseKCLEntities([]byte(devBundle))
	if !hasDevPortTargets(dev) {
		t.Error("dev bundle should expose port targets")
	}
	// Host-bearing cloud bundle: its routes carry hosts, no host-mapped
	// listener ports -> no dev port targets (the gateways have no listeners).
	cloud, _ := parseKCLEntities([]byte(sampleSmokeBundle))
	if hasDevPortTargets(cloud) {
		t.Error("host-bearing cloud bundle should NOT expose dev port targets")
	}
}

// portOf extracts the integer port from a URL like http://127.0.0.1:53999.
func portOf(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %q: %v", raw, err)
	}
	return p
}
