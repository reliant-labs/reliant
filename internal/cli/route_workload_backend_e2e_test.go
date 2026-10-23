//go:build e2e

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// ADR-0003 F2 end-to-end: a route that targets an OnHost workload really
// does carry traffic from the gateway listener into a HOST process, and a
// `traffic.health_check` expecting 401 really does keep a 401-answering
// backend in service.
//
// WHY THIS IS AN e2e AND NOT A RENDER ASSERTION. The render tests
// (kcl/tests/positive_route_workload_on_host.k) prove forge emits the
// manifests we intended. They cannot prove Envoy Gateway ACCEPTS them —
// and the first mechanism we tried, an ExternalName Service, rendered
// perfectly and was refused by the controller at apply time
// ("...is of type ExternalName, which is not supported as a backend").
// A render-only test would have called that green.
//
// TWO PREREQUISITES THIS TEST ALSO PINS, both of which bite silently:
//
//  1. The Backend API must be enabled on the controller
//     (config.envoyGateway.extensionApis.enableBackend=true). Without it
//     the route goes ResolvedRefs=False "Backend is disabled in Envoy
//     Gateway configuration" and the gateway returns 500.
//  2. Envoy Gateway's OWN CRDs must be applied. gateway-helm ships them in
//     a dependency subchart under a helm `crds/` directory, which
//     `--skip-crds` suppresses — so a cluster built the way forge builds
//     one has no gateway.envoyproxy.io CRDs at all, and both resources F2
//     renders (Backend, BackendTrafficPolicy) fail to apply.
//
// Everything is named adr3-f2-* and deleted by name. All state lives in
// t.TempDir(). Ports come from freePortE2E — the corpus runs in parallel.

const (
	e2eGatewayAPIVersion   = "v1.6.2"
	e2eEnvoyGatewayVersion = "v1.9.2"
)

// TestE2ERouteToOnHostWorkloadReachesHostProcess renders a real forge env
// whose HTTPRoute targets an OnHost workload, applies it to a throwaway
// k3d cluster running Envoy Gateway, and curls the gateway listener to
// reach a host http.Server.
func TestE2ERouteToOnHostWorkloadReachesHostProcess(t *testing.T) {
	t.Parallel() // own k3d cluster, own temp dir, kernel-allocated ports

	requireE2EBinaries(t, "k3d", "kubectl", "helm")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// The host process the route must reach. Started FIRST so the port is
	// genuinely bound before the cluster resolves it.
	const wantBody = "adr3-f2-host-process-reached"
	hostPort, stopHost := startHostProbeServer(t, wantBody)
	defer stopHost()

	const listenerPort = 80

	// ── Render the env through forge's own KCL module ──────────────────
	// Not a hand-written manifest: the point is that what FORGE renders
	// is what Envoy Gateway accepts.
	manifests := renderOnHostRouteEnv(t, onHostRouteEnvParams{
		ClusterContext: "k3d-" + e2eClusterName(t),
		Namespace:      "adr3-f2",
		ListenerPort:   listenerPort,
		HostListenPort: hostPort,
	})

	// The rendered output must contain the EG Backend pointing at the host
	// gateway on the allocated port — assert before paying for a cluster.
	assertRenderedHostBackend(t, manifests, hostPort)

	// ── Throwaway cluster, named and deleted by name ───────────────────
	cluster := e2eClusterName(t)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	gatewayHostPort := createK3dClusterF2(t, ctx, cluster, listenerPort, kubeconfig)

	installEnvoyGateway(t, ctx, kubeconfig)

	// ── Apply what forge rendered ──────────────────────────────────────
	kubectlApplyManifests(t, ctx, kubeconfig, manifests)

	// The route must RESOLVE. This is the assertion ExternalName failed,
	// and the one the Backend-API prerequisite fails.
	waitForRouteResolved(t, ctx, kubeconfig, "adr3-f2", "host-route")

	// ── The actual proof: traffic reaches the host process ─────────────
	url := fmt.Sprintf("http://127.0.0.1:%d/probe", gatewayHostPort)
	body := getWithRetryF2(t, ctx, url, 90*time.Second)
	if !strings.Contains(body, wantBody) {
		t.Fatalf("gateway did not reach the host process.\nGET %s\nbody: %q\nwant it to contain %q",
			url, body, wantBody)
	}
}

// TestE2ERouteTrafficHealthCheckExpects401 pins the trap from ADR-0003 S1
// §5: a registry with token auth answers /v2/ with 401 when HEALTHY, so a
// default (2xx) health check marks every replica down and the route
// serves nothing.
//
// The assertion reads ENVOY'S OWN health-check counters, not curl. That
// distinction is load-bearing and was measured during the F2 probe: with a
// single endpoint, Envoy's panic threshold routes to unhealthy hosts
// anyway, so a 200 through the gateway says nothing about health. The
// 404-answering control backend returned 200 through the gateway on every
// request while reporting health_check.failure on all 114 attempts.
func TestE2ERouteTrafficHealthCheckExpects401(t *testing.T) {
	t.Parallel()

	requireE2EBinaries(t, "k3d", "kubectl", "helm")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// Backend A answers 401 on /v2/ (a healthy token-authed registry).
	// Backend B answers 404 there — the control, which must go unhealthy
	// under the SAME check.
	healthyPort, stopHealthy := startRegistryStyleServer(t, 401)
	defer stopHealthy()

	unhealthyPort, stopUnhealthy := startRegistryStyleServer(t, 404)
	defer stopUnhealthy()

	const listenerPort = 80

	manifests := renderHealthCheckEnv(t, healthCheckEnvParams{
		Namespace:     "adr3-f2",
		ListenerPort:  listenerPort,
		HealthyPort:   healthyPort,
		UnhealthyPort: unhealthyPort,
	})

	cluster := e2eClusterName(t)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	createK3dClusterF2(t, ctx, cluster, listenerPort, kubeconfig)

	installEnvoyGateway(t, ctx, kubeconfig)
	kubectlApplyManifests(t, ctx, kubeconfig, manifests)

	waitForRouteResolved(t, ctx, kubeconfig, "adr3-f2", "healthy-route")
	waitForPolicyAccepted(t, ctx, kubeconfig, "adr3-f2", "healthy-route")

	// Envoy's counters are the authority. The 401 backend must accumulate
	// successes; the 404 backend must accumulate failures.
	stats := waitForHealthCheckStats(t, ctx, kubeconfig, 3*time.Minute)

	healthySuccess := stats.counter("healthy-route", "health_check.success")
	healthyFailure := stats.counter("healthy-route", "health_check.failure")
	if healthySuccess == 0 {
		t.Errorf("the 401-answering backend recorded NO health_check.success under expected_statuses=[401]"+
			" — a healthy token-authed registry was marked down.\nstats:\n%s", stats.raw)
	}
	if healthyFailure > 0 && healthySuccess == 0 {
		t.Errorf("the 401-answering backend recorded %d health_check.failure and no success", healthyFailure)
	}

	unhealthyFailure := stats.counter("unhealthy-route", "health_check.failure")
	unhealthySuccess := stats.counter("unhealthy-route", "health_check.success")
	if unhealthyFailure == 0 {
		t.Errorf("the 404-answering control backend recorded NO health_check.failure under"+
			" expected_statuses=[401] — the check is not discriminating, so the 401 result above"+
			" proves nothing.\nstats:\n%s", stats.raw)
	}
	if unhealthySuccess > 0 {
		t.Errorf("the 404-answering control backend recorded %d health_check.success under"+
			" expected_statuses=[401]; want 0", unhealthySuccess)
	}
}

// ---------------------------------------------------------------------------
// Rendering — through forge's own KCL module, not hand-written YAML.
// ---------------------------------------------------------------------------

type onHostRouteEnvParams struct {
	ClusterContext string
	Namespace      string
	ListenerPort   int
	HostListenPort int
}

func renderOnHostRouteEnv(t *testing.T, p onHostRouteEnvParams) []map[string]any {
	t.Helper()

	src := fmt.Sprintf(`import forge

_target = forge.ClusterTarget {
    cluster = %q
    namespace = %q
}

_gw = forge.Gateway {
    name = "adr3-f2-gw"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = %d
            protocol = "HTTP"
        }
    ]
}

_hostproc = forge.Workload {
    name = "hostproc"
    command = ["./bin/hostproc"]
    runtime = forge.OnHost {runner = "binary", listen_ports = [%d]}
}

_route = forge.HTTPRoute {
    name = "host-route"
    gateway = "adr3-f2-gw"
    listener = "http"
    workload = "hostproc"
}

_bundle = forge.Bundle {
    project = "adr3-f2"
    cluster_target = _target
    workloads = [_hostproc]
    gateways = [_gw]
    http_routes = [_route]
}

manifests = forge.render(_bundle).manifests
`, p.ClusterContext, p.Namespace, p.ListenerPort, p.HostListenPort)

	return renderKCLManifests(t, src)
}

type healthCheckEnvParams struct {
	Namespace     string
	ListenerPort  int
	HealthyPort   int
	UnhealthyPort int
}

func renderHealthCheckEnv(t *testing.T, p healthCheckEnvParams) []map[string]any {
	t.Helper()

	// Both routes carry the SAME traffic policy, expecting 401. The only
	// difference is which host port the backend is, so the comparison
	// isolates the backend's response from the policy.
	src := fmt.Sprintf(`import forge

_target = forge.ClusterTarget {
    cluster = "k3d-adr3-f2"
    namespace = %q
}

_gw = forge.Gateway {
    name = "adr3-f2-gw"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = %d
            protocol = "HTTP"
        }
    ]
}

_healthy = forge.Workload {
    name = "healthy"
    command = ["./bin/healthy"]
    runtime = forge.OnHost {runner = "binary", listen_ports = [%d]}
}

_unhealthy = forge.Workload {
    name = "unhealthy"
    command = ["./bin/unhealthy"]
    runtime = forge.OnHost {runner = "binary", listen_ports = [%d]}
}

_traffic = forge.RouteTraffic {
    retries = forge.RouteRetries {
        on = ["5xx", "reset", "connect-failure", "refused-stream"]
        attempts = 3
    }
    timeout = "1h"
    health_check = forge.RouteHealthCheck {
        path = "/v2/"
        expected_statuses = [401]
        interval = "1s"
        timeout = "1s"
    }
}

_healthy_route = forge.HTTPRoute {
    name = "healthy-route"
    gateway = "adr3-f2-gw"
    listener = "http"
    workload = "healthy"
    path = "/healthy"
    traffic = _traffic
}

_unhealthy_route = forge.HTTPRoute {
    name = "unhealthy-route"
    gateway = "adr3-f2-gw"
    listener = "http"
    workload = "unhealthy"
    path = "/unhealthy"
    traffic = _traffic
}

_bundle = forge.Bundle {
    project = "adr3-f2"
    cluster_target = _target
    workloads = [_healthy, _unhealthy]
    gateways = [_gw]
    http_routes = [_healthy_route, _unhealthy_route]
}

manifests = forge.render(_bundle).manifests
`, p.Namespace, p.ListenerPort, p.HealthyPort, p.UnhealthyPort)

	return renderKCLManifests(t, src)
}

func renderKCLManifests(t *testing.T, source string) []map[string]any {
	t.Helper()

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.k")
	if err := os.WriteFile(entry, []byte(source), 0o644); err != nil {
		t.Fatalf("write KCL entry: %v", err)
	}

	out, err := kclrender.Run(dir, entry, nil)
	if err != nil {
		t.Fatalf("render KCL: %v", err)
	}

	var doc struct {
		Manifests []map[string]any `json:"manifests"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode rendered document: %v\n%s", err, out)
	}
	if len(doc.Manifests) == 0 {
		t.Fatalf("render produced no manifests:\n%s", out)
	}
	return doc.Manifests
}

// assertRenderedHostBackend fails before any cluster is created if the
// render did not produce the EG Backend at the allocated host port.
func assertRenderedHostBackend(t *testing.T, manifests []map[string]any, wantPort int) {
	t.Helper()

	for _, m := range manifests {
		if m["kind"] != "Backend" {
			continue
		}
		if got := m["apiVersion"]; got != "gateway.envoyproxy.io/v1alpha1" {
			t.Fatalf("Backend apiVersion = %v, want gateway.envoyproxy.io/v1alpha1", got)
		}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal Backend: %v", err)
		}
		var b struct {
			Spec struct {
				Endpoints []struct {
					FQDN struct {
						Hostname string `json:"hostname"`
						Port     int    `json:"port"`
					} `json:"fqdn"`
				} `json:"endpoints"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("decode Backend: %v", err)
		}
		if len(b.Spec.Endpoints) != 1 {
			t.Fatalf("Backend has %d endpoints, want 1", len(b.Spec.Endpoints))
		}
		fqdn := b.Spec.Endpoints[0].FQDN
		if fqdn.Hostname != "host.k3d.internal" {
			t.Errorf("Backend fqdn hostname = %q, want host.k3d.internal", fqdn.Hostname)
		}
		if fqdn.Port != wantPort {
			t.Errorf("Backend fqdn port = %d, want the workload's allocated listen port %d",
				fqdn.Port, wantPort)
		}
		return
	}
	t.Fatalf("render produced no gateway.envoyproxy.io Backend for the OnHost target")
}

// ---------------------------------------------------------------------------
// Host probe servers.
// ---------------------------------------------------------------------------

func startHostProbeServer(t *testing.T, body string) (int, func()) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
	return startServer(t, mux)
}

// startRegistryStyleServer answers /v2/ with statusForV2 and everything
// else with 200 — the shape of a token-authed registry, whose healthy
// answer on /v2/ is 401.
func startRegistryStyleServer(t *testing.T, statusForV2 int) (int, func()) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusForV2)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	return startServer(t, mux)
}

// startServer serves h on a kernel-assigned port and returns it. The port is
// bound here, not probed with freePortE2E and bound later: in that gap a
// parallel test can take it ("bind: address already in use").
func startServer(t *testing.T, h http.Handler) (int, func()) {
	t.Helper()

	// Bind 0.0.0.0: the request arrives from the k3d node's view of the
	// host, not over loopback.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("bind host probe server: %v", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	return ln.Addr().(*net.TCPAddr).Port, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// ---------------------------------------------------------------------------
// Cluster lifecycle. Everything is named adr3-f2-* and removed by name;
// no pattern-kill, and no other cluster is touched.
// ---------------------------------------------------------------------------

func e2eClusterName(t *testing.T) string {
	t.Helper()
	// One cluster per test, distinct because the corpus runs in parallel.
	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-").Replace(name)
	if len(name) > 24 {
		name = name[len(name)-24:]
	}
	return "adr3-f2-" + strings.Trim(name, "-")
}

// createK3dClusterF2 creates the test's cluster and returns the host port its
// loadbalancer publishes listenerPort on.
//
// Docker picks that port, at the moment it binds it. The test used to pick it
// itself (freePortE2E: listen on :0, close, hand the number over), and in the
// seconds between that close and k3d starting the loadbalancer anything on the
// runner could take the port — a parallel test's k3d cluster draws its API
// port from the same range. CI hit exactly that: "Bind for 0.0.0.0:34433
// failed: port is already allocated" (E2E Suite run 39510866508). A port no
// one chose ahead of time cannot be taken first.
func createK3dClusterF2(t *testing.T, ctx context.Context, name string, listenerPort int, kubeconfig string) int {
	t.Helper()
	// Registered BEFORE the create, so a create that fails partway is still
	// deleted by name.
	t.Cleanup(func() { deleteK3dClusterF2(t, name) })

	// Through forge's own create (createK3dCluster), not a bare `k3d cluster
	// create`: it is what users get, and it rides out the one k3d failure
	// this test ever had — a k3s server that crashed two seconds into boot,
	// which k3d's readiness wait reports as "stopped returning log lines:
	// … status=restarting" instead of waiting for Docker's restart. forge
	// deletes that half-made cluster and creates it once more, printing the
	// server's exit and last log lines.
	if err := createK3dCluster(ctx, name, []string{"cluster", "create", name,
		"--servers", "1", "--agents", "0",
		"--port", fmt.Sprintf("%d@loadbalancer", listenerPort),
		// k3s bundles Traefik, whose svclb claims the loadbalancer's port 80
		// and answers its own "404 page not found" — Envoy never sees the
		// request. The test's gateway is Envoy, so Traefik must not exist.
		"--k3s-arg", "--disable=traefik@server:0",
		"--wait", "--timeout", "300s",
	}); err != nil {
		t.Fatalf("create k3d cluster %s: %v", name, err)
	}

	// A private kubeconfig for this test's own kubectl calls. createK3dCluster
	// merges the cluster's context into the default kubeconfig without
	// switching the current one (and the delete removes it); the test never
	// relies on that entry.
	//
	// `k3d kubeconfig write` rather than `get`: k3d writes its own
	// progress logging to the same stream as the document, ANSI colour
	// codes included, so capturing stdout yields a file kubectl rejects
	// with "yaml: control characters are not allowed". Letting k3d write
	// the file keeps the two apart.
	// --kubeconfig-switch-context=false is explicit even here: it defaults
	// to TRUE on this subcommand, and writing to a private --output does
	// not exempt it from touching the default kubeconfig's current-context.
	out := runE2E(t, ctx, "", nil, "k3d", "kubeconfig", "write", name,
		"--output", kubeconfig, "--overwrite", "--kubeconfig-switch-context=false")
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Fatalf("k3d did not write a kubeconfig to %s: %v\n%s", kubeconfig, err, out)
	}

	lb := "k3d-" + name + "-serverlb"
	published := runE2E(t, ctx, "", nil, "docker", "port", lb, fmt.Sprintf("%d/tcp", listenerPort))
	for _, line := range strings.Split(strings.TrimSpace(published), "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if port, err := strconv.Atoi(strings.TrimSpace(line[i+1:])); err == nil && port > 0 {
				return port
			}
		}
	}
	t.Fatalf("%s publishes no host port for %d/tcp: %q", lb, listenerPort, published)
	return 0
}

func deleteK3dClusterF2(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// By NAME. Best-effort: a failure here must not mask a test failure,
	// but it is reported so a leak is visible.
	// k3d removes the cluster's context from the DEFAULT kubeconfig on
	// delete, a whole-file rewrite: hold forge's kubeconfig lock so a
	// parallel test's forge cannot lose a context to it (kubeconfig_lock.go).
	_ = withKubeconfigLock(func() error {
		cmd := exec.CommandContext(ctx, "k3d", "cluster", "delete", name)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("cleanup: k3d cluster delete %s: %v\n%s", name, err, out)
		}
		return nil
	})
}

// installEnvoyGateway installs Gateway API + Envoy Gateway the way forge
// installs them, AND the two things F2 additionally requires: EG's own
// CRDs (which --skip-crds suppresses) and the Backend API enabled.
func installEnvoyGateway(t *testing.T, ctx context.Context, kubeconfig string) {
	t.Helper()

	env := []string{"KUBECONFIG=" + kubeconfig}

	// 1. Pinned standard-channel Gateway API CRDs (forge applies these
	//    explicitly so the CRD surface matches the cloud install).
	runE2E(t, ctx, "", env, "kubectl", "apply", "--server-side", "-f",
		"https://github.com/kubernetes-sigs/gateway-api/releases/download/"+
			e2eGatewayAPIVersion+"/standard-install.yaml")
	runE2E(t, ctx, "", env, "kubectl", "wait", "--for=condition=Established",
		"crd/gateways.gateway.networking.k8s.io",
		"crd/httproutes.gateway.networking.k8s.io", "--timeout=120s")

	// 2. The controller, with the Backend API ENABLED. Without the
	//    extensionApis value, a Backend backendRef is refused at apply
	//    time: "Backend is disabled in Envoy Gateway configuration."
	runE2E(t, ctx, "", env, "helm", "upgrade", "--install", "eg",
		"oci://docker.io/envoyproxy/gateway-helm", "--version", e2eEnvoyGatewayVersion,
		"-n", "envoy-gateway-system", "--create-namespace", "--skip-crds",
		"--set", "config.envoyGateway.extensionApis.enableBackend=true",
		"--wait", "--timeout", "8m")

	// 3. Envoy Gateway's OWN CRDs. gateway-helm ships them in a dependency
	//    subchart's `crds/` directory, which --skip-crds suppresses — so
	//    without this step the cluster has no gateway.envoyproxy.io CRDs
	//    and both resources F2 renders fail to apply. Pulled from the
	//    pinned chart rather than a URL so the version cannot drift.
	installEnvoyGatewayCRDs(t, ctx, env)

	runE2E(t, ctx, "", env, "kubectl", "wait", "--for=condition=Established",
		"crd/backends.gateway.envoyproxy.io",
		"crd/backendtrafficpolicies.gateway.envoyproxy.io", "--timeout=120s")

	// The controller reads its config at start.
	runE2E(t, ctx, "", env, "kubectl", "-n", "envoy-gateway-system",
		"rollout", "restart", "deploy/envoy-gateway")
	runE2E(t, ctx, "", env, "kubectl", "-n", "envoy-gateway-system",
		"rollout", "status", "deploy/envoy-gateway", "--timeout=300s")

	// The `eg` GatewayClass forge's Gateways name.
	applyYAML(t, ctx, env, `apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
`)
}

func installEnvoyGatewayCRDs(t *testing.T, ctx context.Context, env []string) {
	t.Helper()

	dir := t.TempDir()
	runE2E(t, ctx, "", env, "helm", "pull", "oci://docker.io/envoyproxy/gateway-helm",
		"--version", e2eEnvoyGatewayVersion, "-d", dir, "--untar")

	// Only EG's own generated CRDs. Deliberately NOT the subchart's
	// gatewayapi-crds.yaml: that is an experimental-channel copy of the
	// Gateway API CRDs, and the standard channel's safe-upgrades policy
	// (applied in step 1) denies it. That conflict is exactly why forge
	// passes --skip-crds in the first place.
	generated := filepath.Join(dir, "gateway-helm", "charts", "crds", "crds", "generated")
	entries, err := os.ReadDir(generated)
	if err != nil {
		t.Fatalf("read Envoy Gateway CRD dir %s: %v\n"+
			"The chart layout moved; F2's rendered Backend/BackendTrafficPolicy need these CRDs.", generated, err)
	}
	args := []string{"apply", "--server-side"}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			args = append(args, "-f", filepath.Join(generated, e.Name()))
		}
	}
	if len(args) == 2 {
		t.Fatalf("no CRD yaml found under %s", generated)
	}
	runE2E(t, ctx, "", env, "kubectl", args...)
}

func kubectlApplyManifests(t *testing.T, ctx context.Context, kubeconfig string, manifests []map[string]any) {
	t.Helper()

	env := []string{"KUBECONFIG=" + kubeconfig}
	applyYAML(t, ctx, env, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: adr3-f2\n")

	// Apply as a JSON List — the manifests are already decoded JSON, and
	// a List avoids re-serializing each object separately.
	list := map[string]any{
		"apiVersion": "v1",
		"kind":       "List",
		"items":      manifests,
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal manifest list: %v", err)
	}

	path := filepath.Join(t.TempDir(), "manifests.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write manifests: %v", err)
	}
	runE2E(t, ctx, "", env, "kubectl", "apply", "--server-side", "-f", path)
}

func applyYAML(t *testing.T, ctx context.Context, env []string, yaml string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "apply.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	runE2E(t, ctx, "", env, "kubectl", "apply", "--server-side", "-f", path)
}

// ---------------------------------------------------------------------------
// Status polling.
// ---------------------------------------------------------------------------

// waitForRouteResolved blocks until the HTTPRoute reports
// ResolvedRefs=True. This is the assertion an ExternalName Service backend
// FAILS, and the one a missing Backend API fails, so its message quotes
// the controller's own reason.
func waitForRouteResolved(t *testing.T, ctx context.Context, kubeconfig, ns, name string) {
	t.Helper()

	env := []string{"KUBECONFIG=" + kubeconfig}
	const jsonPath = `{range .status.parents[*].conditions[?(@.type=="ResolvedRefs")]}{.status}|{.reason}|{.message}{end}`

	deadline := time.Now().Add(4 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("context cancelled waiting for route %s/%s", ns, name)
		}
		out, err := runE2ESoft(ctx, "", env, "kubectl", "-n", ns, "get", "httproute", name,
			"-o", "jsonpath="+jsonPath)
		if err == nil {
			last = strings.TrimSpace(out)
			if strings.HasPrefix(last, "True|") {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("HTTPRoute %s/%s never reached ResolvedRefs=True.\nlast status: %s\n"+
		"A 'not supported as a backend' reason means the backend mechanism is wrong;\n"+
		"'Backend is disabled in Envoy Gateway configuration' means the controller needs\n"+
		"config.envoyGateway.extensionApis.enableBackend=true.", ns, name, last)
}

func waitForPolicyAccepted(t *testing.T, ctx context.Context, kubeconfig, ns, name string) {
	t.Helper()

	env := []string{"KUBECONFIG=" + kubeconfig}
	const jsonPath = `{range .status.ancestors[*].conditions[?(@.type=="Accepted")]}{.status}|{.message}{end}`

	deadline := time.Now().Add(3 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		out, err := runE2ESoft(ctx, "", env, "kubectl", "-n", ns, "get", "backendtrafficpolicy", name,
			"-o", "jsonpath="+jsonPath)
		if err == nil {
			last = strings.TrimSpace(out)
			if strings.HasPrefix(last, "True|") {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("BackendTrafficPolicy %s/%s never reported Accepted=True.\nlast status: %s", ns, name, last)
}

// ---------------------------------------------------------------------------
// Envoy health-check stats — the authoritative signal.
// ---------------------------------------------------------------------------

type envoyStats struct{ raw string }

// counter sums the value of every stat line whose name contains both the
// route name and the counter suffix. Envoy names them
// `cluster.httproute/<ns>/<route>/rule/<n>.health_check.<counter>`.
func (s envoyStats) counter(route, suffix string) int {
	total := 0
	for _, line := range strings.Split(s.raw, "\n") {
		name, valStr, ok := strings.Cut(line, ": ")
		if !ok || !strings.Contains(name, "/"+route+"/") || !strings.HasSuffix(name, suffix) {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(strings.TrimSpace(valStr), "%d", &v); err == nil {
			total += v
		}
	}
	return total
}

// waitForHealthCheckStats polls the Envoy proxy's admin stats until active
// health checking has actually run, then returns the whole filtered dump.
func waitForHealthCheckStats(t *testing.T, ctx context.Context, kubeconfig string, within time.Duration) envoyStats {
	t.Helper()

	env := []string{"KUBECONFIG=" + kubeconfig}
	pod := waitForEnvoyProxyPod(t, ctx, env)

	// The envoy container has no shell, so the admin endpoint is read
	// through a port-forward rather than an exec.
	localPort := freePortE2E(t)
	pfCtx, cancelPF := context.WithCancel(ctx)
	defer cancelPF()
	pf := exec.CommandContext(pfCtx, "kubectl", "-n", "envoy-gateway-system",
		"port-forward", "pod/"+pod, fmt.Sprintf("%d:19000", localPort))
	pf.Env = append(os.Environ(), env...)
	if err := pf.Start(); err != nil {
		t.Fatalf("start port-forward to envoy admin: %v", err)
	}
	defer func() { _ = pf.Process.Kill() }()

	url := fmt.Sprintf("http://127.0.0.1:%d/stats?filter=health_check", localPort)
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		body, err := httpGet(ctx, url)
		if err != nil {
			continue
		}
		last = body
		// Wait until BOTH routes have attempted a check, so a zero on
		// either side is a real result rather than a timing artifact.
		s := envoyStats{raw: body}
		if s.counter("healthy-route", "health_check.attempt") > 0 &&
			s.counter("unhealthy-route", "health_check.attempt") > 0 {
			return s
		}
	}
	t.Fatalf("Envoy never reported health_check attempts for both routes within %s.\nlast stats:\n%s", within, last)
	return envoyStats{}
}

func waitForEnvoyProxyPod(t *testing.T, ctx context.Context, env []string) string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		out, err := runE2ESoft(ctx, "", env, "kubectl", "-n", "envoy-gateway-system",
			"get", "pods", "-l", "gateway.envoyproxy.io/owning-gateway-name=adr3-f2-gw",
			"-o", "jsonpath={.items[?(@.status.phase==\"Running\")].metadata.name}")
		if err == nil {
			if name := strings.Fields(strings.TrimSpace(out)); len(name) > 0 {
				return name[0]
			}
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no Running Envoy proxy pod for gateway adr3-f2-gw")
	return ""
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

func requireE2EBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not on PATH; this e2e needs a local cluster toolchain", n)
		}
	}
}

func getWithRetryF2(t *testing.T, ctx context.Context, url string, within time.Duration) string {
	t.Helper()

	deadline := time.Now().Add(within)
	var lastErr error
	var lastBody string
	for time.Now().Before(deadline) {
		body, err := httpGet(ctx, url)
		if err == nil {
			return body
		}
		lastErr, lastBody = err, body
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("GET %s never succeeded within %s: %v (last body %q)", url, within, lastErr, lastBody)
	return ""
}

func httpGet(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return string(b), fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(b), nil
}

func runE2E(t *testing.T, ctx context.Context, dir string, env []string, name string, args ...string) string {
	t.Helper()

	out, err := runE2ESoft(ctx, dir, env, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func runE2ESoft(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
