package cli

// `forge env smoke <env>` on a HOSTED environment
// (control-plane docs/design/hosted-deploy-primitives.md §3.3).
//
// THE BUG THIS FIXES IS A GREEN CHECK THAT CHECKED NOTHING. smoke probes the
// ingress graph forge finds in the env's RENDER — Gateways, HTTPRoutes,
// Frontends. A hosted env has none of those: the platform allocates the URL,
// so nothing in the KCL names it. So smoke found no targets, printed "nothing
// to probe", and exited 0 — on the environment where a post-deploy probe
// matters most, and with a verdict indistinguishable from a clean pass.
//
// On a hosted env the ingress graph lives in the control plane's status
// instead: each workload's `observed.url` is its platform URL, and each
// custom domain it serves has a state. So smoke reads GetStatus and probes
// what it finds there, through the SAME route classifier the cluster path
// uses (PASS reached-backend / WARN likely-misroute / FAIL tls-transport), so
// one vocabulary covers both topologies and a CI consumer keying on `reason`
// does not have to learn a second.
//
// AN ENV WITH NO URL-BEARING WORKLOAD IS `skipped`, NEVER `passed`. That is
// the whole point and it is load-bearing in two places: the exit stays 0
// (nothing is broken — a cluster-internal env is a legitimate shape), but the
// report's summary carries zero probes, which gateFromDocument maps to
// GateStatusSkipped. So the evidence trail records "this checked nothing"
// rather than a pass, and the two can never again be confused.
//
// ONLY *LIVE* CUSTOM DOMAINS ARE PROBED. A domain in pending_dns or issuing
// has not been handed any traffic yet — the author still owes a DNS record,
// or the certificate is still being issued — so probing it would FAIL on a
// state that is both expected and outside the release's control. A domain
// that is live and does not serve is a real finding; one that is not live yet
// is a different command's business (`forge domain`).

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// hostedSmokeTarget is one URL the hosted path probes, and where it came
// from.
type hostedSmokeTarget struct {
	// Workload is the deployment that serves this URL.
	Workload string
	// URL is the absolute URL to probe.
	URL string
	// Kind is "platform" for the platform-allocated URL, or "domain" for
	// a live custom domain. Carried so the report can say which, and so a
	// reader can tell a platform-URL failure (the release) from a
	// custom-domain one (usually DNS or a certificate).
	Kind string
}

const (
	hostedTargetPlatform = "platform"
	hostedTargetDomain   = "domain"
)

// hostedRouteProbe issues one probe against an absolute URL. A seam, so the
// orchestration is unit-testable without a live hosted env.
type hostedRouteProbe func(ctx context.Context, target hostedSmokeTarget, timeout time.Duration) smokeRouteResult

// runHostedSmoke is the hosted half of `forge env smoke`.
//
// flowResults are the declared app-flow checks, already run by the caller and
// folded in here. They must still count on a hosted env for the same reason
// they count on a cluster one: a flow check asserts an end-to-end invariant
// no URL probe can see, and an env whose every URL answers while the app is
// broken is exactly the case they exist to catch.
func runHostedSmoke(
	ctx context.Context,
	env string,
	opts smokeOptions,
	status deploytarget.HostedEnvStatus,
	probe hostedRouteProbe,
	flowResults []smokeRouteResult,
	out io.Writer,
) error {
	targets := hostedSmokeTargets(status)

	results := make([]smokeRouteResult, 0, len(targets))
	for _, t := range targets {
		res := probe(ctx, t, opts.timeout)
		res.Target = hostedTargetAsSmokeTarget(t)
		results = append(results, res)
	}
	sortSmokeResults(results)

	combined := append(append([]smokeRouteResult{}, results...), flowResults...)
	summary := summarizeSmoke(combined)

	if opts.jsonOut {
		// The SAME document shape as the cluster path. Deliberately
		// not a hosted-specific one: `forge env smoke --json` is a
		// published contract, and a second shape would mean every
		// consumer — including gateFromDocument — needs to know which
		// topology produced it before it can read the verdict.
		if err := writeSmokeJSON(out, env, opts.tag, combined, summary); err != nil {
			return err
		}
	} else {
		writeHostedSmokeReport(out, env, status, results, flowResults, summary)
	}

	if summary.AnyFail {
		return fmt.Errorf("smoke %s: %d check(s) FAILED", env, summary.Fail)
	}
	return nil
}

// hostedSmokeTargets is every URL in a hosted env's status worth probing:
// each workload's platform URL, and each of its LIVE custom domains.
//
// Pure, and the ordering is stable, so the report and the JSON document are
// deterministic for a given status.
func hostedSmokeTargets(status deploytarget.HostedEnvStatus) []hostedSmokeTarget {
	var targets []hostedSmokeTarget
	for _, w := range status.Workloads {
		if u := strings.TrimSpace(w.URL); u != "" {
			targets = append(targets, hostedSmokeTarget{Workload: w.Name, URL: u, Kind: hostedTargetPlatform})
		}
		for _, d := range w.Domains {
			// See the file header: only a live domain has been
			// handed traffic, so only a live one can be judged.
			if d.State != deploytarget.DomainStateLive {
				continue
			}
			if host := strings.TrimSpace(d.Domain); host != "" {
				targets = append(targets, hostedSmokeTarget{
					Workload: w.Name, URL: "https://" + host, Kind: hostedTargetDomain,
				})
			}
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Workload != targets[j].Workload {
			return targets[i].Workload < targets[j].Workload
		}
		if targets[i].Kind != targets[j].Kind {
			// The platform URL first: it is the one that reflects
			// the release, and a custom domain's failure is usually
			// read against it.
			return targets[i].Kind == hostedTargetPlatform
		}
		return targets[i].URL < targets[j].URL
	})
	return targets
}

// hostedTargetAsSmokeTarget projects a hosted target onto the shared
// smokeTarget, so the existing report table, the JSON document and
// sortSmokeResults all work unchanged.
//
// Gateway carries the WORKLOAD name, because on a hosted env the workload is
// what the platform routes to — it occupies the same column in the table and
// answers the same question ("which thing serves this").
func hostedTargetAsSmokeTarget(t hostedSmokeTarget) smokeTarget {
	host, path := t.URL, "/"
	if u, err := url.Parse(t.URL); err == nil && u.Host != "" {
		host = u.Host
		if u.Path != "" {
			path = u.Path
		}
	}
	return smokeTarget{
		RouteKind: t.Kind,
		RouteName: t.Workload,
		Gateway:   t.Workload,
		Host:      host,
		ProbeHost: host,
		Path:      path,
	}
}

// probeHostedURL probes one hosted URL and classifies the response.
//
// DNS IS RESOLVED NORMALLY, unlike the cluster path's curl --resolve dial. A
// platform URL and a live custom domain are both public names the platform
// has already published, so forcing the connection somewhere else would test
// a path no user takes — and would miss precisely the DNS and certificate
// faults that make a hosted URL unreachable.
//
// Certificate validation stays ON, for the reason the cluster path documents:
// a stuck or wrong certificate must surface as a transport failure, because
// that IS the bug being hunted.
func probeHostedURL(ctx context.Context, target hostedSmokeTarget, timeout time.Duration) smokeRouteResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		},
		// Redirects are NOT followed: a 301/302 is a routing answer
		// about the URL that was asked about, and following it would
		// report the classification of a different URL. The classifier
		// treats a structured redirect as a backend that answered.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, http.NoBody)
	if err != nil {
		return classifyTransportError(err)
	}
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return classifyTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	path := "/"
	if u, perr := url.Parse(target.URL); perr == nil && u.Path != "" {
		path = u.Path
	}
	return classifyResponseForPath(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), path)
}

// writeHostedSmokeReport prints the human report.
func writeHostedSmokeReport(
	out io.Writer,
	env string,
	status deploytarget.HostedEnvStatus,
	results, flowResults []smokeRouteResult,
	summary smokeSummary,
) {
	fmt.Fprintf(out, "forge env smoke %s (hosted)\n", env)
	if status.Verdict != "" {
		fmt.Fprintf(out, "  control plane reports the environment %s\n", status.Verdict)
	}

	if len(results) == 0 {
		// SAY WHAT WAS AND WAS NOT CHECKED, and why. "nothing to
		// probe" on its own is what made the old behaviour read as a
		// pass; naming the cause makes the next step obvious.
		fmt.Fprintf(out, "  SKIPPED — no workload in this environment serves a URL, so there is no ingress to probe.\n")
		if len(status.Workloads) == 0 {
			fmt.Fprintf(out, "    the control plane reports no workloads at all: has `forge env deploy %s` run?\n", env)
		} else {
			fmt.Fprintf(out, "    %d workload(s) reported, none with a platform URL "+
				"(a cluster-internal service or a database has none).\n", len(status.Workloads))
		}
	} else {
		writeHostedSmokeTable(out, results)
	}
	if len(flowResults) > 0 {
		writeFlowCheckSection(out, flowResults)
	}
	writeSmokeOverallVerdict(out, summary, len(results)+len(flowResults))
}

// writeHostedSmokeTable renders the probe rows.
//
// A table of its own rather than the cluster path's writeSmokeTable, because
// that one's columns are cluster concepts — a gateway-IP inventory, a kubectl
// context — and it prints its own summary line. A hosted env has no gateway
// IPs, and the summary here must fold in the flow checks. The REASON column
// is shared (smokeReasonLine), which is the part a CI consumer reads.
func writeHostedSmokeTable(out io.Writer, results []smokeRouteResult) {
	const wResult = 6
	workloadW, kindW, hostW := len("WORKLOAD"), len("VIA"), len("URL")
	for _, r := range results {
		workloadW = maxInt(workloadW, len(r.Target.RouteName))
		kindW = maxInt(kindW, len(r.Target.RouteKind))
		hostW = maxInt(hostW, len(r.Target.Host))
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %-*s  %-*s  %-*s  %-*s  %s\n",
		wResult, "RESULT", workloadW, "WORKLOAD", kindW, "VIA", hostW, "URL", "REASON")
	for _, r := range results {
		fmt.Fprintf(out, "  %-*s  %-*s  %-*s  %-*s  %s\n",
			wResult, string(r.Status),
			workloadW, r.Target.RouteName,
			kindW, r.Target.RouteKind,
			hostW, r.Target.Host,
			smokeReasonLine(r))
	}
}

// readHostedSmokeStatus reads the env's hosted status for smoke. A package
// variable so a test states the control plane's answer without a live env;
// production resolves the real client from the env's own declaration, the
// same precedence every other hosted verb uses.
var readHostedSmokeStatus hostedStatusReader = readHostedStatusFromDeclaration
