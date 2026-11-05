package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newSmokeCmd builds `forge env smoke <env>` — a post-deploy ingress
// verification probe. forge holds the whole ingress graph in its model
// (Gateways / HTTPRoutes / GRPCRoutes / Frontends) but deploy never
// VERIFIES it; this command renders the env's Bundle, resolves each
// Gateway's live external IP, and probes every declared route through
// that IP (curl --resolve style, so it works pre-DNS-cutover). It
// classifies each route as PASS (backend answered) / WARN (likely
// misroute) / FAIL (TLS-transport or missing-CORS) and exits non-zero on
// any FAIL so it can gate a deploy or CI.
//
// Deploy-gate hook point (not wired here, by design): runDeploy in
// deploy.go could call runSmoke(ctx, env, smokeOptions{}) as a final
// post-rollout step behind a `forge env deploy --smoke` flag — the rollout
// already waits for Ready, so the gateway addresses are populated by the
// time smoke runs. Keeping it a standalone command first lets the
// classification mature against real envs before it blocks deploys.
func newSmokeCmd() *cobra.Command {
	var (
		tag               string
		jsonOut           bool
		timeoutSec        int
		contextOverride   string
		namespaceOverride string
	)
	cmd := &cobra.Command{
		Use:   "smoke <environment>",
		Short: "Probe every declared ingress route after deploy (TLS + routing + CORS)",
		Long: `Verify the ingress graph forge deployed actually serves traffic.

forge models the whole ingress graph (Gateways, HTTPRoutes, GRPCRoutes,
Frontends) but deploy never checks it. Real bugs shipped silently and
were only caught in a browser: a gateway with a stuck cert (TLS handshake
dropped -> ERR_CONNECTION_CLOSED), a route pointing at a backend that
404s the path, and an API route missing CORS for the frontend origin.

smoke renders the env's KCL (same path forge env deploy uses), resolves each
Gateway's live external IP from its status, and probes every route
through that IP via a curl --resolve-style dial (host:443 -> gatewayIP),
setting the TLS ServerName + Host header to the route host so it works
before DNS cutover. Each route is classified:

  PASS  backend answered      any structured HTTP response (200/401/403/
                              415, a Connect error envelope, a non-default
                              404) — TLS + routing reached a backend.
  WARN  likely misroute       a plain text/plain "404 page not found" (the
                              Go default mux) — the host reached a backend
                              that doesn't serve that path.
  FAIL  tls-transport         TLS handshake error / reset / no response —
                              cert stuck or gateway not programmed.
  FAIL  cors-missing          an API route the frontend calls answered but
                              carried no Access-Control-Allow-Origin.

Exits non-zero if any route FAILs, so it can gate a deploy or CI run.

Examples:
  forge env smoke preprod                 # probe every preprod route
  forge env smoke prod --json             # machine-readable output for CI
  forge env smoke staging --tag v1.2.3    # (tag reserved; render is tag-agnostic)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSmoke(cmd.Context(), args[0], smokeOptions{
				tag:               tag,
				jsonOut:           jsonOut,
				timeout:           time.Duration(timeoutSec) * time.Second,
				contextOverride:   contextOverride,
				namespaceOverride: namespaceOverride,
			})
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "Image tag to associate with this smoke run (informational; render is tag-agnostic)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON instead of the table")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 10, "Per-probe timeout in seconds")
	cmd.Flags().StringVar(&contextOverride, "context", "", "kubectl context to read Gateway status from (overrides the env's declared K8sCluster.cluster). Read-only override: smoke only queries status, so it never risks a wrong-cluster write the way deploy would.")
	cmd.Flags().StringVar(&namespaceOverride, "namespace", "", "namespace the Gateways live in (overrides the env's declared K8sCluster.namespace)")
	return cmd
}

type smokeOptions struct {
	tag               string
	jsonOut           bool
	timeout           time.Duration
	contextOverride   string
	namespaceOverride string

	// flowProbe injects the app-flow check probe. Production leaves it nil and
	// runSmoke uses the real one (probeFlowHealth); tests set it to drive the
	// phase deterministically without a live HTTP server. The checks themselves
	// come from the rendered workloads (resolveFlowChecks). See smoke_flow.go.
	flowProbe flowProbe
}

// gatewayIPResolver resolves a gateway's live external IP. Swapped out in
// tests; the real one shells kubectl against the env's declared context.
type gatewayIPResolver func(ctx context.Context, kubeContext, namespace, gateway string) (string, error)

// routeProbe issues one probe and returns the classified result (before
// the gateway/target metadata is attached). Swapped out in tests.
type routeProbe func(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult

// runSmoke is the orchestration entrypoint. Real cluster reads + probes
// are injected via runSmokeWith so the orchestration is unit-testable.
func runSmoke(ctx context.Context, env string, opts smokeOptions) error {
	if opts.timeout <= 0 {
		opts.timeout = 10 * time.Second
	}
	// Default the real probe for the production path. Tests inject their own
	// (see smokeOptions).
	if opts.flowProbe == nil {
		opts.flowProbe = probeFlowHealth
	}
	return runSmokeWith(ctx, env, opts, resolveGatewayIP, probeRoute, os.Stdout)
}

// runSmokeWith is the injectable core. It renders the Bundle, derives the
// per-env kubectl context + namespace (reusing the deploy code's
// declared-context resolution), resolves each gateway's IP, probes every
// route, prints the report, and returns a non-nil error when any route
// FAILed (so the process exits non-zero).
func runSmokeWith(ctx context.Context, env string, opts smokeOptions, resolve gatewayIPResolver, probe routeProbe, out io.Writer) error {
	projectDir := projectDirForKCL()
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return fmt.Errorf("smoke %s: render KCL: %w\n  fix: confirm deploy/kcl/%s/ exists and renders (try `forge env deploy %s --dry-run`)", env, err, env, env)
	}

	// App-flow checks run regardless of the route topology: they assert an
	// end-to-end invariant the route/port probes can't see, so they must
	// still run (and can still RED the smoke) even when the env has no
	// host-bearing routes. They are declared on the workloads in the KCL just
	// rendered; resolve and run them once and fold them into every path below.
	runFlow := func(hostedURL map[string]string) []smokeRouteResult {
		return runSmokeFlowChecks(ctx, resolveFlowChecks(entities, hostedURL), opts.flowProbe, flowCheckTimeout(opts.timeout))
	}

	// A HOSTED env's ingress is not in its render — the platform allocates
	// the URL, so nothing in the KCL names it. Its targets come from the
	// control plane's status instead (smoke_hosted.go, §3.3). Dispatched
	// before extractSmokeTargets because that finds nothing here, which is
	// exactly how a hosted smoke used to exit 0 having probed nothing.
	if entities.HasHosted() {
		status, err := readHostedSmokeStatus(ctx, env, entities)
		if err != nil {
			return fmt.Errorf("smoke %s: read the hosted environment's status: %w", env, err)
		}
		hostedURL := make(map[string]string, len(status.Workloads))
		for _, w := range status.Workloads {
			hostedURL[w.Name] = strings.TrimSpace(w.URL)
		}
		flowResults := runFlow(hostedURL)
		return runHostedSmoke(ctx, env, opts, status, probeHostedURL, flowResults, out)
	}

	flowResults := runFlow(nil)

	targets := extractSmokeTargets(entities)
	if len(targets) == 0 {
		// No HOST-bearing routes. Before giving up, try the PORT-based
		// (dev) topology: dev reaches every service at localhost:<port>
		// via a host-mapped Gateway listener per service (host-less
		// routes), so the host-bearing path finds nothing. If the bundle
		// exposes host-mapped listener ports (or host->infra deps), probe
		// THOSE instead. See smoke_dev.go.
		if hasDevPortTargets(entities) {
			return runDevSmokeWith(ctx, env, opts, entities, probeDevPort, flowResults, out)
		}
		// No routes/ports — but a declared app-flow check may still have an
		// opinion (it's the whole point: a green-while-broken env with no
		// probeable ingress). If any flow check ran, report on THAT.
		if len(flowResults) > 0 {
			return reportFlowOnlySmoke(out, env, opts, flowResults)
		}
		// Genuinely nothing to probe — no routes, no ports, no flow checks.
		// The env may be cluster-internal only.
		_, _ = fmt.Fprintf(out, "smoke %s: no host-bearing ingress routes declared — nothing to probe.\n", env)
		return nil
	}

	// Declared-context resolution, same source of truth as deploy: the
	// env's K8sCluster.cluster IS the kubectl context, and
	// K8sCluster.namespace is where the Gateways live. smoke's --context
	// override wins — and unlike deploy (which dropped its --context for a
	// declarative-only write path), smoke keeps the override because it
	// only READS Gateway status, so a wrong-cluster query is harmless. It
	// also covers envs where the render doesn't surface a cluster-typed
	// service's K8sCluster.cluster to firstK8sClusterField (the same blind
	// spot `forge env deploy <env> --explain` reports as "not declared").
	kubeContext := opts.contextOverride
	if kubeContext == "" {
		kubeContext = firstK8sClusterField(ctx, env, "cluster")
	}
	namespace := opts.namespaceOverride
	if namespace == "" {
		namespace = k8sClusterNamespaceForEnv(ctx, env)
	}

	// Resolve each gateway's external IP once (a route reuses its
	// gateway's IP). A gateway with no address is a FAIL — it isn't
	// programmed — and every route on it inherits that FAIL.
	gatewayIPs, gatewayErrs := resolveGatewayIPs(ctx, entities, kubeContext, namespace, resolve)

	results := make([]smokeRouteResult, 0, len(targets))
	for _, t := range targets {
		ip := gatewayIPs[t.Gateway]
		if ip == "" {
			res := smokeRouteResult{
				Target: t,
				Status: smokeStatusFail,
				Reason: smokeReasonNoAddr,
				Detail: gatewayNoAddrDetail(t.Gateway, gatewayErrs[t.Gateway]),
			}
			results = append(results, res)
			continue
		}
		res := probe(ctx, t, ip, opts.timeout)
		res.Target = t
		results = append(results, res)
	}

	sortSmokeResults(results)

	// The overall verdict folds BOTH the route probes and the app-flow checks:
	// a healthy ingress with a broken app-flow must still exit non-zero (the
	// green-while-broken bug this phase closes). The route table and the
	// flow-check section are printed separately for readability, but the
	// summary/exit counts the union.
	combined := append(append([]smokeRouteResult{}, results...), flowResults...)
	summary := summarizeSmoke(combined)

	if opts.jsonOut {
		if err := writeSmokeJSON(out, env, opts.tag, combined, summary); err != nil {
			return err
		}
	} else {
		writeSmokeTable(out, env, kubeContext, namespace, gatewayIPs, results, summarizeSmoke(results))
		// Only when app-flow checks ran do we print the flow section + a
		// combined verdict; without them the route table's own summary is the
		// verdict (unchanged behaviour for projects that declare no checks).
		if len(flowResults) > 0 {
			writeFlowCheckSection(out, flowResults)
			writeSmokeOverallVerdict(out, summary, len(combined))
		}
	}

	if summary.AnyFail {
		return fmt.Errorf("smoke %s: %d check(s) FAILED — ingress/app-flow is not serving correctly", env, summary.Fail)
	}
	return nil
}

// reportFlowOnlySmoke handles the case where the env exposes no probeable
// ingress routes / dev ports but DID declare app-flow checks — the purest
// green-while-broken scenario (nothing to TCP-probe, but the app flow may be
// broken). It prints just the flow-check section + verdict and exits non-zero
// on any flow FAIL.
func reportFlowOnlySmoke(out io.Writer, env string, opts smokeOptions, flowResults []smokeRouteResult) error {
	summary := summarizeSmoke(flowResults)
	if opts.jsonOut {
		if err := writeSmokeJSON(out, env, opts.tag, flowResults, summary); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(out, "forge env smoke %s\n", env)
		_, _ = fmt.Fprintln(out, "  no host-bearing routes / dev ports to probe — checking declared app-flow endpoints only.")
		writeFlowCheckSection(out, flowResults)
		writeSmokeOverallVerdict(out, summary, len(flowResults))
	}
	if summary.AnyFail {
		return fmt.Errorf("smoke %s: %d app-flow check(s) FAILED", env, summary.Fail)
	}
	return nil
}

// writeSmokeOverallVerdict prints the combined summary line + verdict once the
// route table and flow-check section have been printed. It tallies the union
// of route + flow results so a flow break is reflected in the bottom line.
func writeSmokeOverallVerdict(out io.Writer, summary smokeSummary, total int) {
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  summary: %d PASS, %d WARN, %d FAIL  (%d check(s))\n",
		summary.Pass, summary.Warn, summary.Fail, total)
	switch {
	case summary.AnyFail:
		_, _ = fmt.Fprintf(out, "  FAILED — ingress and/or app-flow is not healthy. See reasons above.\n")
	case summary.Warn > 0:
		_, _ = fmt.Fprintf(out, "  PASS with warnings — review WARN rows.\n")
	default:
		_, _ = fmt.Fprintf(out, "  PASS — every route reached a backend and every app-flow check is healthy.\n")
	}
}

func gatewayNoAddrDetail(gateway string, err error) string {
	if err != nil {
		return fmt.Sprintf("gateway %q: %v", gateway, err)
	}
	return fmt.Sprintf("gateway %q has no external address (not programmed / LB pending)", gateway)
}

// resolveGatewayIPs resolves every distinct gateway referenced by the
// bundle's routes. Errors are captured per-gateway (not fatal) so a
// single un-programmed gateway FAILs its own routes without aborting the
// whole smoke run.
func resolveGatewayIPs(ctx context.Context, e *KCLEntities, kubeContext, namespace string, resolve gatewayIPResolver) (map[string]string, map[string]error) {
	ips := map[string]string{}
	errs := map[string]error{}
	seen := map[string]struct{}{}
	collect := func(gateway string) {
		if gateway == "" {
			return
		}
		if _, ok := seen[gateway]; ok {
			return
		}
		seen[gateway] = struct{}{}
		ip, err := resolve(ctx, kubeContext, namespace, gateway)
		if err != nil {
			errs[gateway] = err
			return
		}
		ips[gateway] = ip
	}
	for _, r := range e.HTTPRoutes {
		collect(r.Gateway)
	}
	for _, r := range e.GRPCRoutes {
		collect(r.Gateway)
	}
	return ips, errs
}

// resolveGatewayIP reads the live Gateway's status.addresses[0].value via
// kubectl against the env's declared context. An empty result (gateway
// exists but has no address yet) is returned as ("", nil) so the caller
// classifies it as the gateway-no-address FAIL rather than a hard error;
// a kubectl failure (missing gateway, bad context, expired creds) is
// returned as an error so the runbook surfaces it.
func resolveGatewayIP(ctx context.Context, kubeContext, namespace, gateway string) (string, error) {
	args := []string{}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "get", "gateway", gateway,
		"-o", "jsonpath={.status.addresses[0].value}")
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("kubectl get gateway %s: %w (is the context %q reachable / creds valid?)", gateway, err, kubeContext)
	}
	return strings.TrimSpace(string(out)), nil
}

// probeRoute issues the real HTTP probe for one target through the
// gateway IP. It dials the gateway IP directly (curl --resolve style) but
// sets the TLS ServerName + Host header to the route host, so the gateway
// routes by host and presents the right cert — and it works before the
// route's DNS A record points at the gateway.
//
// We use net/http + crypto/tls directly (not shelling curl) so transport
// errors are classifiable: a TLS handshake failure / reset / EOF surfaces
// as a Go error we map to the tls-transport FAIL, distinct from a backend
// that answered with a structured status. A GET (with the CORS Origin
// header when the target carries one) is enough — the key signal is the
// transport, and Connect endpoints legitimately answer 401/415/404 to a
// bare GET, which still proves TLS + routing.
func probeRoute(ctx context.Context, target smokeTarget, gatewayIP string, timeout time.Duration) smokeRouteResult {
	// ProbeHost is the concrete host to dial / SNI / Host-header (equals
	// the declared Host except for a wildcard route). Fall back to Host
	// for targets built without it set (defensive — extractSmokeTargets
	// always populates it).
	probeHost := target.ProbeHost
	if probeHost == "" {
		probeHost = target.Host
	}
	client := smokeHTTPClient(probeHost, gatewayIP, timeout)

	url := "https://" + probeHost + target.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return classifyTransportError(err)
	}
	// Host header is set from the URL host already; set it explicitly to
	// be unambiguous when the URL host and dialed IP differ.
	req.Host = probeHost
	if target.Origin != "" {
		req.Header.Set("Origin", target.Origin)
	}
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return classifyTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read a bounded prefix of the body — enough to detect the
	// "404 page not found" sentinel without slurping a large payload.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	res := classifyResponseForPath(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), target.Path)
	res = applyCORSVerdict(res, target.Origin, resp.Header)
	return res
}

// smokeHTTPClient builds an http.Client whose transport dials the gateway
// IP for the route host (the --resolve behavior) while presenting the
// route host as the TLS ServerName. InsecureSkipVerify is deliberately
// FALSE: a stuck / wrong cert must surface as a TLS handshake error — that
// IS the ERR_CONNECTION_CLOSED bug we're hunting. We only override WHERE
// we connect, never WHETHER the cert is valid for the host.
func smokeHTTPClient(host, gatewayIP string, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		// Pin every dial to the gateway IP:443 regardless of the host in
		// the URL — this is curl --resolve host:443:<gatewayIP>.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(gatewayIP, "443"))
		},
		TLSClientConfig: &tls.Config{
			// Present the route host so the gateway selects the right
			// cert + SNI-based route. Validation stays on.
			ServerName: host,
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Don't follow redirects — a 301/302 is itself a structured
		// answer from a backend (PASS); following it would probe a
		// different host than the route we're verifying.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// --- output -------------------------------------------------------------

func writeSmokeTable(out io.Writer, env, kubeContext, namespace string, gatewayIPs map[string]string, results []smokeRouteResult, summary smokeSummary) {
	_, _ = fmt.Fprintf(out, "forge env smoke %s\n", env)
	if kubeContext != "" {
		_, _ = fmt.Fprintf(out, "  context: %s   namespace: %s\n", kubeContext, emptyAs(namespace, "(default)"))
	}
	// Gateway IP inventory (sorted) so a stuck gateway is visible above
	// the route table.
	if len(gatewayIPs) > 0 {
		gws := make([]string, 0, len(gatewayIPs))
		for g := range gatewayIPs {
			gws = append(gws, g)
		}
		sort.Strings(gws)
		_, _ = fmt.Fprintln(out, "  gateways:")
		for _, g := range gws {
			_, _ = fmt.Fprintf(out, "    %s -> %s\n", g, gatewayIPs[g])
		}
	}
	_, _ = fmt.Fprintln(out)

	// Column widths.
	const (
		wResult = 6
	)
	hostW, pathW, routeW := len("HOST"), len("PATH"), len("ROUTE")
	for _, r := range results {
		hostW = maxInt(hostW, len(r.Target.Host))
		pathW = maxInt(pathW, len(r.Target.Path))
		routeW = maxInt(routeW, len(r.Target.RouteName))
	}
	_, _ = fmt.Fprintf(out, "  %-*s  %-*s  %-*s  %-*s  %s\n", wResult, "RESULT", routeW, "ROUTE", hostW, "HOST", pathW, "PATH", "REASON")
	for _, r := range results {
		_, _ = fmt.Fprintf(out, "  %-*s  %-*s  %-*s  %-*s  %s\n",
			wResult, string(r.Status),
			routeW, r.Target.RouteName,
			hostW, r.Target.Host,
			pathW, r.Target.Path,
			smokeReasonLine(r))
	}

	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  summary: %d PASS, %d WARN, %d FAIL  (%d route(s))\n",
		summary.Pass, summary.Warn, summary.Fail, len(results))
	if summary.AnyFail {
		_, _ = fmt.Fprintf(out, "  FAILED — ingress is not serving every declared route. See reasons above.\n")
	} else if summary.Warn > 0 {
		_, _ = fmt.Fprintf(out, "  PASS with warnings — review WARN routes (likely misroutes).\n")
	} else {
		_, _ = fmt.Fprintf(out, "  PASS — every declared route reached a backend.\n")
	}
}

// smokeReasonLine renders the reason column: the reason class plus a
// short detail, including the HTTP status when one was received.
func smokeReasonLine(r smokeRouteResult) string {
	if r.StatusCode > 0 {
		return fmt.Sprintf("%s [HTTP %d] %s", r.Reason, r.StatusCode, r.Detail)
	}
	return fmt.Sprintf("%s %s", r.Reason, r.Detail)
}

// smokeJSONRoute is the stable --json per-route shape. Additive: new
// fields may appear; existing keys keep their meaning (audit-json
// contract style).
type smokeJSONRoute struct {
	Result     string `json:"result"` // PASS | WARN | FAIL
	Reason     string `json:"reason"`
	RouteKind  string `json:"route_kind"`
	RouteName  string `json:"route_name"`
	Gateway    string `json:"gateway"`
	Host       string `json:"host"`
	Path       string `json:"path"`
	StatusCode int    `json:"status_code,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type smokeJSONReport struct {
	Env     string           `json:"env"`
	Tag     string           `json:"tag,omitempty"`
	Routes  []smokeJSONRoute `json:"routes"`
	Summary struct {
		Pass int  `json:"pass"`
		Warn int  `json:"warn"`
		Fail int  `json:"fail"`
		OK   bool `json:"ok"` // false when any route FAILed
	} `json:"summary"`
}

func writeSmokeJSON(out io.Writer, env, tag string, results []smokeRouteResult, summary smokeSummary) error {
	rep := smokeJSONReport{Env: env, Tag: tag}
	for _, r := range results {
		rep.Routes = append(rep.Routes, smokeJSONRoute{
			Result:     string(r.Status),
			Reason:     r.Reason,
			RouteKind:  r.Target.RouteKind,
			RouteName:  r.Target.RouteName,
			Gateway:    r.Target.Gateway,
			Host:       r.Target.Host,
			Path:       r.Target.Path,
			StatusCode: r.StatusCode,
			Origin:     r.Target.Origin,
			Detail:     r.Detail,
		})
	}
	rep.Summary.Pass = summary.Pass
	rep.Summary.Warn = summary.Warn
	rep.Summary.Fail = summary.Fail
	rep.Summary.OK = !summary.AnyFail
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
