package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// smoke_flow.go — the APP-FLOW CHECK phase of `forge env smoke`.
//
// WHY THIS EXISTS. The built-in smoke probes (smoke.go cloud routes,
// smoke_dev.go dev ports) verify TRANSPORT only: a listener answered, a port
// is bound. That is necessary but NOT sufficient — smoke can be GREEN while
// the app is functionally broken, because no built-in probe can express an
// app-specific end-to-end invariant. The motivating case: `forge env smoke dev`
// was GREEN while the managed-daemon flow was broken, because forge had no way
// to assert "every Ready daemon is attached to the gateway".
//
// THE DESIGN. The OWNING SERVICE (the one holding the state internally) asserts
// the invariant and exposes it as an HTTP flow-health endpoint returning 200
// (healthy) / 503 (unhealthy) — status-only in public so it leaks nothing
// sensitive. The workload DECLARES that endpoint in its KCL
// (`flow_checks = [forge.FlowCheck {path = "/flow-health"}]`), and `forge env
// smoke <env>` curls it, folding the 200/503 into the SAME summary + table +
// --json + exit logic the route probes use. A 503 (or unreachable) flow
// endpoint turns smoke RED (exit 1), so a green smoke means the app actually
// works, not just that its ports are open.
//
// The URL is never written down. It is resolved from the env being smoked, off
// the workload that owns the endpoint (resolveFlowChecks), so there is no port
// to go stale and no `envs:` filter: the check exists where the workload does.

// smokeFlowReason* are the reason classes for the flow-check phase. Stable
// strings (the --json `reason` field keys off them), distinct from the
// route-probe reasons so a CI consumer can tell an app-flow failure apart from
// an ingress failure.
const (
	smokeFlowReasonHealthy   = "flow-healthy"     // PASS: endpoint returned 2xx
	smokeFlowReasonUnhealthy = "flow-unhealthy"   // FAIL: endpoint returned non-2xx (e.g. 503)
	smokeFlowReasonUnreach   = "flow-unreachable" // FAIL: endpoint couldn't be reached
	smokeFlowReasonErr       = "flow-misdeclared" // FAIL: the check declaration is invalid
)

// flowProbe issues one flow-health probe and returns (statusCode, body, err).
// Swapped out in tests so the phase orchestration is unit-testable without a
// live HTTP server. A non-nil err means the endpoint couldn't be reached
// (transport failure) — distinct from a reachable endpoint that answered 503.
type flowProbe func(ctx context.Context, url string, timeout time.Duration) (int, string, error)

// flowCheck is one declared flow check resolved to the thing smoke can probe.
// URL is empty — with Misdeclared set — when forge could not work out where the
// endpoint is served in this env.
type flowCheck struct {
	Name        string
	Workload    string
	URL         string
	Description string
	// Misdeclared explains why URL is empty, in terms of the fix.
	Misdeclared string
}

// resolveFlowChecks turns every workload's declared flow checks into probe
// targets for the env that was rendered. Order of resolution, first match wins:
//
//  1. an HTTPRoute/GRPCRoute to the workload whose path prefix covers the
//     check's path — the route forwards the request, so the listener's origin
//     plus the check's path reaches it (and a route with a narrower path, or
//     none covering it, does not);
//  2. a host workload's first listen port: http://localhost:<port><path>;
//  3. a hosted workload's platform URL, from the control plane's status.
//
// A check none of these reaches is returned Misdeclared rather than dropped:
// a silently skipped assertion is exactly the green-while-broken failure this
// phase exists to close.
func resolveFlowChecks(e *KCLEntities, hostedURL map[string]string) []flowCheck {
	if e == nil {
		return nil
	}
	var out []flowCheck
	for _, w := range e.Workloads {
		for _, fc := range w.FlowChecks {
			name := fc.Name
			if name == "" {
				name = w.Name + ":" + fc.Path
			}
			c := flowCheck{Name: name, Workload: w.Name, Description: fc.Description}
			switch {
			case routeFlowURL(e, w.Name, fc.Path) != "":
				c.URL = routeFlowURL(e, w.Name, fc.Path)
			case w.Runtime.Type == RuntimeHost && w.HostPort() > 0:
				c.URL = fmt.Sprintf("http://localhost:%d%s", w.HostPort(), fc.Path)
			case w.Runtime.Type == RuntimeHosted && hostedURL[w.Name] != "":
				c.URL = strings.TrimRight(hostedURL[w.Name], "/") + fc.Path
			default:
				c.Misdeclared = fmt.Sprintf("workload %q declares flow check %q but nothing in this env reaches it: add an HTTPRoute to %q whose path covers %q (or serve it on a host listen port)",
					w.Name, fc.Path, w.Name, fc.Path)
			}
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// routeFlowURL finds a route to workload whose path covers path, and returns
// the URL it is served at, or "". Both route kinds can carry the check (a
// flow-health endpoint is plain HTTP, so only an HTTPRoute can actually
// forward a GET — a GRPCRoute is skipped).
func routeFlowURL(e *KCLEntities, workload, path string) string {
	best, bestLen := "", -1
	for _, r := range e.HTTPRoutes {
		if r.Service != workload && r.Workload != workload {
			continue
		}
		gw := findGateway(e, r.Gateway)
		if gw == nil {
			continue
		}
		l := findListener(gw, r.Listener)
		if l == nil || l.Port == 0 {
			continue
		}
		prefix := normalizeSmokePath(r.Path)
		if !pathCovers(prefix, path) {
			continue
		}
		scheme := "http"
		if strings.EqualFold(l.Protocol, "HTTPS") {
			scheme = "https"
		}
		host := gw.EffectiveHost(l, r.Host)
		if host == "" || strings.HasPrefix(host, "*.") {
			host = "localhost"
		}
		origin := fmt.Sprintf("%s://%s:%d", scheme, host, l.Port)
		if (scheme == "http" && l.Port == 80) || (scheme == "https" && l.Port == 443) {
			origin = fmt.Sprintf("%s://%s", scheme, host)
		}
		if len(prefix) > bestLen {
			best, bestLen = origin+path, len(prefix)
		}
	}
	return best
}

// pathCovers reports whether a route's path prefix forwards path: "/" covers
// everything, otherwise prefix must equal path or be a whole-segment prefix of it.
func pathCovers(prefix, path string) bool {
	prefix = strings.TrimRight(prefix, "/")
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// runSmokeFlowChecks probes every resolved flow-health endpoint and returns the
// projected result rows, in name order so the table is deterministic. No
// declared checks yields no rows — the route probes alone decide the verdict,
// so existing projects are unaffected.
//
// The probe is injected so the orchestration (which checks run, how 200/503
// map to PASS/FAIL) is testable without a real HTTP server.
func runSmokeFlowChecks(ctx context.Context, checks []flowCheck, probe flowProbe, timeout time.Duration) []smokeRouteResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ordered := append([]flowCheck(nil), checks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	results := make([]smokeRouteResult, 0, len(ordered))
	for _, c := range ordered {
		results = append(results, probeOneFlowCheck(ctx, c, probe, timeout))
	}
	return results
}

// probeOneFlowCheck curls a single flow-health endpoint and projects its
// outcome into a smokeRouteResult so it folds into the shared
// summary/table/json. The check's name + URL are surfaced as the route
// metadata (RouteKind "flow") so the existing output helpers render it without
// special-casing.
func probeOneFlowCheck(ctx context.Context, c flowCheck, probe flowProbe, timeout time.Duration) smokeRouteResult {
	target := smokeTarget{
		RouteKind: "flow",
		RouteName: c.Name,
		Host:      c.URL,
		ProbeHost: c.URL,
		Path:      "(flow-health)",
	}
	if strings.TrimSpace(c.URL) == "" {
		return smokeRouteResult{
			Target: target,
			Status: smokeStatusFail,
			Reason: smokeFlowReasonErr,
			Detail: c.Misdeclared,
		}
	}

	statusCode, body, err := probe(ctx, c.URL, timeout)
	summary := flowBodySummary(body)

	switch {
	case err != nil:
		return smokeRouteResult{
			Target: target,
			Status: smokeStatusFail,
			Reason: smokeFlowReasonUnreach,
			Detail: flowDetail("UNREACHABLE", transportErrorDetail(err), c.Description),
		}
	case statusCode >= 200 && statusCode < 300:
		return smokeRouteResult{
			Target:     target,
			Status:     smokeStatusPass,
			Reason:     smokeFlowReasonHealthy,
			StatusCode: statusCode,
			Detail:     flowDetail("HEALTHY", summary, c.Description),
		}
	default:
		return smokeRouteResult{
			Target:     target,
			Status:     smokeStatusFail,
			Reason:     smokeFlowReasonUnhealthy,
			StatusCode: statusCode,
			Detail:     flowDetail("UNHEALTHY", summary, c.Description),
		}
	}
}

// probeFlowHealth is the real flow-health probe: a plain HTTP GET that returns
// the status code + a bounded body prefix. No redirect following (a redirect
// is not a health verdict), and it never errors on a non-2xx — only a
// transport failure (dial/TLS/read) is returned as err, so 503 surfaces as a
// reachable-but-unhealthy result the caller classifies as FAIL.
func probeFlowHealth(ctx context.Context, url string, timeout time.Duration) (int, string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n]), nil
}

// flowBodySummary trims a flow-health response body to a short single-line
// note so the smoke detail column quotes the aggregate ("2 daemons, 0
// unattached") without dumping the whole JSON. Returns "" when empty.
func flowBodySummary(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	// One line is enough — flow-health bodies are terse JSON / a status line.
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = strings.TrimSpace(body[:i])
	}
	const maxLen = 160
	if len(body) > maxLen {
		body = body[:maxLen] + "…"
	}
	return body
}

// flowDetail composes the smoke detail column as
//
//	<VERDICT> — <live body aggregate>  [<static description>]
//
// The VERDICT (HEALTHY / UNHEALTHY / UNREACHABLE) leads and is the LIVE state.
// The body aggregate is the endpoint's own terse summary ("2 attachments, 0
// stale") — the live evidence. The declared description is a STATIC label of
// what the check asserts; it's bracketed and trails so it can never be misread
// as the live verdict (the earlier "flow UNHEALTHY <description that reads
// healthy>" garble). Any empty part is omitted.
func flowDetail(verdict, bodySummary, description string) string {
	out := verdict
	if bodySummary != "" {
		out += " — " + bodySummary
	}
	if description != "" {
		out += "  [" + description + "]"
	}
	return out
}

// flowCheckTimeout derives the flow-health probe timeout from the smoke
// per-probe timeout. A flow-health endpoint runs an internal assertion (a DB
// read, a connection-table scan), so give it a little more headroom than a
// bare route probe, but never less than the configured timeout.
func flowCheckTimeout(probeTimeout time.Duration) time.Duration {
	if probeTimeout <= 0 {
		return 10 * time.Second
	}
	if probeTimeout < 10*time.Second {
		return 10 * time.Second
	}
	return probeTimeout
}

// writeFlowCheckSection prints the app-flow check rows as their own block
// under the route table, so a flow break is visible even when every route
// passed. Called by both smoke paths after the route table. No-op when there
// are no flow rows.
func writeFlowCheckSection(out io.Writer, flowResults []smokeRouteResult) {
	if len(flowResults) == 0 {
		return
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  app-flow checks (curl the owning service's flow-health endpoint):")
	nameW := len("CHECK")
	for _, r := range flowResults {
		nameW = maxInt(nameW, len(r.Target.RouteName))
	}
	_, _ = fmt.Fprintf(out, "    %-6s  %-*s  %s\n", "RESULT", nameW, "CHECK", "DETAIL")
	for _, r := range flowResults {
		_, _ = fmt.Fprintf(out, "    %-6s  %-*s  %s\n", string(r.Status), nameW, r.Target.RouteName, smokeReasonLine(r))
	}
}
