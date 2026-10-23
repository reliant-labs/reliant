package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// smoke_dev.go — the PORT-BASED (host-less) smoke probe path, for the DEV
// loop.
//
// The cloud smoke path (smoke.go) probes HOST-bearing routes: it resolves
// a gateway's external LB IP and dials it with the route host as SNI +
// Host header. DEV dropped that model — the dev loop reaches every service
// at http://localhost:<port> via a dedicated, host-mapped Gateway listener
// per service (controller -> :28090, http -> :28080, grpc -> :29190),
// NOT via a `controller.localhost` host header. So the dev routes carry no
// host, and the cloud path finds nothing to probe.
//
// This path instead probes each rendered route through its listener's
// HOST-MAPPED PORT on localhost: for each HTTPRoute/GRPCRoute it resolves
// the attached listener's `port` (which, in dev, IS the host port k3d
// pre-maps), and probes localhost:<port>. An HTTP listener gets a real
// HTTP GET (reusing the cloud classifier — a 200/401/404-at-root is a
// reached backend); an H2C/gRPC listener gets a TCP connect (a bare
// HTTP/1.1 GET against H2C is meaningless, but a successful dial proves the
// listener is bound + routed). Connection refused -> FAIL, so a dead
// :28090 (the workspace-controller breakage that keeps recurring) RED's and
// a mapped one GREEN's.
//
// STRETCH — the host->infra hop: the dev host processes reach Postgres /
// NATS / etc. at localhost:<port> coordinates baked into their KCL env
// (DATABASE_URL / NATS_URL). We extract those localhost ports and TCP-probe
// them too, so `forge env smoke dev` covers host->infra, not just gateway
// routes.

// devSmokeTarget is one port-based probe: a localhost:<port> endpoint
// derived from a rendered route+listener (Kind http/grpc) or an infra
// dependency (Kind infra) parsed from a host service's env URL.
type devSmokeTarget struct {
	Kind     string // "http" | "grpc" | "infra"
	Name     string // route name, or "<service>:<infra>" for infra deps
	Gateway  string // gateway name for a route; "" for infra
	Listener string // listener name for a route; "" for infra
	Port     int    // the localhost host port to dial
	Path     string // request path (http routes only)
	// Probe selects how the target is verified: "http" issues a real HTTP
	// GET and classifies the response; "tcp" only asserts the dial connects
	// (H2C/gRPC listeners and infra deps).
	Probe string
}

// devPortProbe issues one port-based probe and returns the classified
// result. Swapped out in tests.
type devPortProbe func(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult

// hasDevPortTargets reports whether the rendered bundle exposes any
// host-mapped listener ports we can port-probe — the signal that this env
// is the port-based (dev) topology rather than the host-bearing (cloud)
// one. Used to choose the dev path only when there's something to probe.
func hasDevPortTargets(e *KCLEntities) bool {
	return len(extractDevSmokeTargets(e)) > 0
}

// extractDevSmokeTargets is the pure projection from a rendered bundle to
// the ordered list of port-based probes. One per HTTPRoute/GRPCRoute whose
// attached listener resolves to a port, plus the infra TCP deps parsed from
// host-service env URLs. Pure: no I/O.
func extractDevSmokeTargets(e *KCLEntities) []devSmokeTarget {
	if e == nil {
		return nil
	}
	var out []devSmokeTarget

	addRoute := func(kind, name, gateway, listenerName, path string) {
		gw := findGateway(e, gateway)
		if gw == nil {
			return
		}
		l := findListener(gw, listenerName)
		if l == nil || l.Port == 0 {
			return
		}
		probe := "http"
		if kind == "grpc" || strings.EqualFold(l.Protocol, "H2C") {
			// H2C / native-gRPC listeners can't be probed with a bare
			// HTTP/1.1 GET — a TCP connect is the meaningful signal.
			probe = "tcp"
		}
		out = append(out, devSmokeTarget{
			Kind:     kind,
			Name:     name,
			Gateway:  gateway,
			Listener: listenerName,
			Port:     l.Port,
			Path:     normalizeSmokePath(path),
			Probe:    probe,
		})
	}
	for _, r := range e.HTTPRoutes {
		addRoute("http", r.Name, r.Gateway, r.Listener, r.Path)
	}
	for _, r := range e.GRPCRoutes {
		addRoute("grpc", r.Name, r.Gateway, r.Listener, r.Path)
	}

	out = append(out, extractDevInfraTargets(e)...)
	return out
}

// extractDevInfraTargets parses the host->infra TCP probes from the host
// services' env URLs. The dev host processes reach Postgres / NATS at
// localhost:<port> coordinates baked into DATABASE_URL / NATS_URL; we
// dedup by (kind, port) so the same shared infra port (e.g. Postgres :5434
// referenced by both control-plane and reliant) is probed once.
func extractDevInfraTargets(e *KCLEntities) []devSmokeTarget {
	type infraVar struct {
		env  string // env var name to read
		kind string // short label for the probe name
	}
	probes := []infraVar{
		{env: "DATABASE_URL", kind: "postgres"},
		{env: "NATS_URL", kind: "nats"},
	}
	seen := map[string]struct{}{} // "kind:port"
	var out []devSmokeTarget
	for _, w := range e.WorkloadsOn(RuntimeHost) { // only host processes dial localhost infra
		env := w.HostEnv()
		for _, p := range probes {
			val := env[p.env]
			port := localhostPortFromURL(val)
			if port == 0 {
				continue
			}
			key := fmt.Sprintf("%s:%d", p.kind, port)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, devSmokeTarget{
				Kind:  "infra",
				Name:  p.kind,
				Port:  port,
				Probe: "tcp",
			})
		}
	}
	// Stable order: by kind then port, so output is deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// envVarValue returns the inline Value of the named env var, or "" when
// absent / not an inline value (a secretKeyRef carries no Value).
func envVarValue(vars []KCLEnvVar, name string) string {
	for _, v := range vars {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

// localhostPortFromURL extracts the port from a URL whose host is a
// loopback name (localhost / 127.0.0.1). Returns 0 when the URL has no
// loopback host or no explicit port — we only probe what the host process
// actually dials on this machine, never an in-cluster DNS name or a port-
// less URL.
func localhostPortFromURL(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return 0
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" {
		return 0
	}
	portStr := u.Port()
	if portStr == "" {
		return 0
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return 0
	}
	return port
}

// runDevSmokeWith is the injectable core for the port-based path: derive
// the localhost port targets from the already-rendered bundle, probe each,
// print the report, and return a non-nil error on any FAIL (non-zero exit).
// The caller (runSmokeWith) renders the bundle once and dispatches here when
// the env has no host-bearing routes but does expose host-mapped ports.
func runDevSmokeWith(ctx context.Context, env string, opts smokeOptions, entities *KCLEntities, probe devPortProbe, flowResults []smokeRouteResult, out io.Writer) error {
	targets := extractDevSmokeTargets(entities)
	if len(targets) == 0 {
		// No dev ports — but a declared app-flow check may still have an
		// opinion. If any ran, report on THAT.
		if len(flowResults) > 0 {
			return reportFlowOnlySmoke(out, env, opts, flowResults)
		}
		_, _ = fmt.Fprintf(out, "smoke %s: no host-mapped listener ports or host-infra deps declared — nothing to probe.\n", env)
		return nil
	}

	results := make([]smokeRouteResult, 0, len(targets))
	for _, t := range targets {
		res := probe(ctx, t, opts.timeout)
		res.Target = devTargetAsSmokeTarget(t)
		results = append(results, res)
	}

	// Fold the app-flow checks into the overall verdict (a healthy dev port
	// graph with a broken app-flow must still exit non-zero — the
	// green-while-broken bug this phase closes).
	combined := append(append([]smokeRouteResult{}, results...), flowResults...)
	summary := summarizeSmoke(combined)

	if opts.jsonOut {
		// Reuse the cloud --json shape so consumers are uniform: each
		// result already carries its projected smokeTarget (host =
		// localhost:<port>, route_kind ∈ http|grpc|infra|flow).
		if err := writeSmokeJSON(out, env, opts.tag, combined, summary); err != nil {
			return err
		}
	} else {
		writeDevSmokeTable(out, env, targets, results, summarizeSmoke(results))
		if len(flowResults) > 0 {
			writeFlowCheckSection(out, flowResults)
			writeSmokeOverallVerdict(out, summary, len(combined))
		}
	}

	if summary.AnyFail {
		return fmt.Errorf("smoke %s: %d check(s) FAILED — dev ingress/infra/app-flow is not serving correctly", env, summary.Fail)
	}
	return nil
}

// devTargetAsSmokeTarget projects a devSmokeTarget into the shared
// smokeTarget so the existing JSON route shape + table helpers can carry
// it. Host is rendered as the concrete localhost:<port> address so the
// table/json reads naturally.
func devTargetAsSmokeTarget(t devSmokeTarget) smokeTarget {
	return smokeTarget{
		RouteKind: t.Kind,
		RouteName: t.Name,
		Gateway:   t.Gateway,
		Host:      fmt.Sprintf("localhost:%d", t.Port),
		ProbeHost: fmt.Sprintf("localhost:%d", t.Port),
		Path:      t.Path,
	}
}

// probeDevPort is the real port-based probe. For an HTTP target it issues a
// plaintext HTTP GET to localhost:<port><path> and classifies the response
// with the same classifier the cloud path uses. For a TCP target (H2C/gRPC
// listener or infra dep) it asserts the dial connects — a successful
// connect proves the listener is bound + routed; a refused/timeout dial is
// the FAIL we hunt (a dead :28090 -> connection refused).
func probeDevPort(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if target.Probe == "tcp" {
		return probeDevTCP(ctx, target, timeout)
	}
	return probeDevHTTP(ctx, target, timeout)
}

func probeDevHTTP(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
	addr := fmt.Sprintf("localhost:%d", target.Port)
	reqURL := "http://" + addr + target.Path
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return classifyDevTransportError(err)
	}
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return classifyDevTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	return classifyResponseForPath(resp.StatusCode, resp.Header.Get("Content-Type"), string(body[:n]), target.Path)
}

func probeDevTCP(ctx context.Context, target devSmokeTarget, timeout time.Duration) smokeRouteResult {
	addr := fmt.Sprintf("localhost:%d", target.Port)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return classifyDevTransportError(err)
	}
	_ = conn.Close()
	label := "listener"
	if target.Kind == "infra" {
		label = target.Name
	}
	return smokeRouteResult{
		Status: smokeStatusPass,
		Reason: smokeReasonReached,
		Detail: fmt.Sprintf("TCP connect ok (%s on :%d)", label, target.Port),
	}
}

// classifyDevTransportError maps a port-based dial/HTTP failure to the dev
// unreachable FAIL. The dev path is PLAINTEXT, so a failure is a dead
// port (connection refused), a hung backend (timeout), or a reset — never a
// TLS handshake. transportErrorDetail still gives the short human note
// ("connection refused" / "timeout"), but the reason class is
// port-unreachable, not tls-transport.
func classifyDevTransportError(err error) smokeRouteResult {
	return smokeRouteResult{
		Status: smokeStatusFail,
		Reason: smokeReasonUnreachable,
		Detail: transportErrorDetail(err),
	}
}

// --- output -------------------------------------------------------------

func writeDevSmokeTable(out io.Writer, env string, targets []devSmokeTarget, results []smokeRouteResult, summary smokeSummary) {
	_, _ = fmt.Fprintf(out, "forge env smoke %s (dev / port-based)\n", env)
	_, _ = fmt.Fprintln(out, "  probing host-mapped listener ports + host->infra deps on localhost")
	_, _ = fmt.Fprintln(out)

	nameW, addrW, probeW := len("TARGET"), len("ADDRESS"), len("PROBE")
	for i, r := range results {
		nameW = maxInt(nameW, len(r.Target.RouteName))
		addrW = maxInt(addrW, len(r.Target.Host))
		probeW = maxInt(probeW, len(targets[i].Probe))
	}
	_, _ = fmt.Fprintf(out, "  %-6s  %-*s  %-*s  %-*s  %s\n", "RESULT", nameW, "TARGET", addrW, "ADDRESS", probeW, "PROBE", "REASON")
	for i, r := range results {
		_, _ = fmt.Fprintf(out, "  %-6s  %-*s  %-*s  %-*s  %s\n",
			string(r.Status),
			nameW, r.Target.RouteName,
			addrW, r.Target.Host,
			probeW, targets[i].Probe,
			smokeReasonLine(r))
	}

	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  summary: %d PASS, %d WARN, %d FAIL  (%d target(s))\n",
		summary.Pass, summary.Warn, summary.Fail, len(results))
	if summary.AnyFail {
		_, _ = fmt.Fprintf(out, "  FAILED — a dev route/infra port is not serving. See reasons above.\n")
	} else if summary.Warn > 0 {
		_, _ = fmt.Fprintf(out, "  PASS with warnings — review WARN targets.\n")
	} else {
		_, _ = fmt.Fprintf(out, "  PASS — every dev port reached a backend.\n")
	}
}
