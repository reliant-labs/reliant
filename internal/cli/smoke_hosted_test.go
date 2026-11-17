package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedStatusWith builds a status with one URL-bearing workload.
func hostedStatusWith(workloads ...deploytarget.HostedWorkloadStatus) deploytarget.HostedEnvStatus {
	return deploytarget.HostedEnvStatus{
		EnvironmentID: "env-prod-uuid", Verdict: "converged", Workloads: workloads,
	}
}

// recordingProbe answers every probe with one verdict and records what it was
// asked about, so the orchestration is tested without a live hosted env.
type recordingProbe struct {
	asked  []hostedSmokeTarget
	result smokeRouteResult
}

func (p *recordingProbe) probe(_ context.Context, t hostedSmokeTarget, _ time.Duration) smokeRouteResult {
	p.asked = append(p.asked, t)
	return p.result
}

func passing() smokeRouteResult {
	return smokeRouteResult{Status: smokeStatusPass, Reason: smokeReasonReached, StatusCode: 200}
}

// THE HEADLINE, HALF ONE: a hosted env with a platform URL gets PROBED. The
// old behaviour found no targets in the render and exited 0.
func TestHostedSmoke_ProbesThePlatformURL(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "api", Tier: "workload", URL: "https://api-acme.reliantapps.dev",
		ObservedState: "ready", Verdict: "converged",
	})
	probe := &recordingProbe{result: passing()}

	var out strings.Builder
	err := runHostedSmoke(context.Background(), "prod", smokeOptions{}, status, probe.probe, nil, &out)
	if err != nil {
		t.Fatalf("smoke: %v", err)
	}
	if len(probe.asked) != 1 {
		t.Fatalf("probed %d target(s), want 1: %+v", len(probe.asked), probe.asked)
	}
	if probe.asked[0].URL != "https://api-acme.reliantapps.dev" {
		t.Errorf("probed %q", probe.asked[0].URL)
	}
	if probe.asked[0].Kind != hostedTargetPlatform {
		t.Errorf("kind = %q, want %q", probe.asked[0].Kind, hostedTargetPlatform)
	}
	if !strings.Contains(out.String(), "api") || !strings.Contains(out.String(), "PASS") {
		t.Errorf("the report should name the workload and its verdict:\n%s", out.String())
	}
}

// THE HEADLINE, HALF TWO: an env with no URL-bearing workload reports
// SKIPPED, not passed. Exit stays 0 (nothing is broken), but the report says
// what was not checked and why.
func TestHostedSmoke_NoURLBearingWorkloadIsSkippedNotPassed(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(
		deploytarget.HostedWorkloadStatus{Name: "db", Tier: "database", ObservedState: "ready"},
		deploytarget.HostedWorkloadStatus{Name: "worker", Tier: "workload", ObservedState: "ready"},
	)
	probe := &recordingProbe{result: passing()}

	var out strings.Builder
	if err := runHostedSmoke(context.Background(), "prod", smokeOptions{}, status, probe.probe, nil, &out); err != nil {
		t.Fatalf("a cluster-internal env is a legitimate shape, so exit must stay 0: %v", err)
	}
	if len(probe.asked) != 0 {
		t.Fatalf("nothing should be probed: %+v", probe.asked)
	}
	report := out.String()
	if !strings.Contains(report, "SKIPPED") {
		t.Errorf("the report must say SKIPPED, not report a pass:\n%s", report)
	}
	if !strings.Contains(report, "2 workload(s) reported") {
		t.Errorf("the report should say what WAS seen, so the next step is obvious:\n%s", report)
	}
}

// And the JSON document of that same run maps to a SKIPPED gate. This is the
// end of the green-while-blind chain: zero probes → no pass in the evidence
// trail.
func TestHostedSmoke_SkippedRunRecordsAsASkippedGate(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{Name: "db", Tier: "database"})

	var out strings.Builder
	err := runHostedSmoke(context.Background(), "prod",
		smokeOptions{jsonOut: true}, status, (&recordingProbe{}).probe, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	gate, gerr := gateFromDocument([]byte(out.String()), "")
	if gerr != nil {
		t.Fatalf("the smoke document must be recordable: %v\n%s", gerr, out.String())
	}
	if gate.Status != release.GateStatusSkipped {
		t.Fatalf("gate status = %q, want skipped — a smoke that probed nothing must never record as a pass",
			gate.Status)
	}
}

// A successful hosted run records as a PASSING gate, through the same
// document. The two tests together pin both ends of the mapping.
func TestHostedSmoke_PassingRunRecordsAsAPassingGate(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "api", URL: "https://api-acme.reliantapps.dev",
	})
	var out strings.Builder
	err := runHostedSmoke(context.Background(), "prod",
		smokeOptions{jsonOut: true}, status, (&recordingProbe{result: passing()}).probe, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	gate, gerr := gateFromDocument([]byte(out.String()), "")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if gate.Status != release.GateStatusPassed || gate.Name != "smoke" {
		t.Fatalf("gate = %+v, want a passing smoke gate", gate)
	}
	if !strings.Contains(gate.Summary, "1 passed") {
		t.Errorf("summary = %q", gate.Summary)
	}
}

// ONLY LIVE CUSTOM DOMAINS ARE PROBED. A domain in pending_dns or issuing has
// been handed no traffic — the author still owes a DNS record, or the cert is
// still issuing — so probing it would FAIL on a state outside the release's
// control.
func TestHostedSmoke_ProbesLiveCustomDomainsOnly(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "web", URL: "https://web-acme.reliantapps.dev",
		Domains: []deploytarget.HostedCustomDomain{
			{Domain: "app.example.com", State: deploytarget.DomainStateLive},
			{Domain: "pending.example.com", State: deploytarget.DomainStatePendingDNS},
			{Domain: "issuing.example.com", State: deploytarget.DomainStateIssuing},
			{Domain: "broken.example.com", State: deploytarget.DomainStateFailed},
		},
	})
	probe := &recordingProbe{result: passing()}

	var out strings.Builder
	if err := runHostedSmoke(context.Background(), "prod", smokeOptions{}, status, probe.probe, nil, &out); err != nil {
		t.Fatal(err)
	}
	var probed []string
	for _, t := range probe.asked {
		probed = append(probed, t.URL)
	}
	want := []string{"https://web-acme.reliantapps.dev", "https://app.example.com"}
	if len(probed) != len(want) {
		t.Fatalf("probed %v, want exactly %v", probed, want)
	}
	for i, u := range want {
		if probed[i] != u {
			t.Errorf("probed[%d] = %q, want %q (platform URL first)", i, probed[i], u)
		}
	}
}

// A FAIL on any target fails the command, so smoke can gate a deploy.
func TestHostedSmoke_AFailingProbeFailsTheCommand(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "api", URL: "https://api-acme.reliantapps.dev",
	})
	probe := &recordingProbe{result: smokeRouteResult{
		Status: smokeStatusFail, Reason: smokeReasonTLS, Detail: "handshake failure",
	}}

	var out strings.Builder
	err := runHostedSmoke(context.Background(), "prod", smokeOptions{}, status, probe.probe, nil, &out)
	if err == nil {
		t.Fatal("a FAILing probe must fail the command")
	}
	if !strings.Contains(err.Error(), "FAILED") {
		t.Errorf("error = %q", err)
	}
	if !strings.Contains(out.String(), smokeReasonTLS) {
		t.Errorf("the report should carry the reason class:\n%s", out.String())
	}
}

// APP-FLOW CHECKS STILL COUNT on a hosted env: they assert an invariant no
// URL probe can see, so an env whose every URL answers while the app is
// broken must still go red.
func TestHostedSmoke_FlowChecksStillGate(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "api", URL: "https://api-acme.reliantapps.dev",
	})
	flow := []smokeRouteResult{{
		Status: smokeStatusFail, Reason: "flow-check",
		Target: smokeTarget{RouteName: "signup", Host: "api-acme.reliantapps.dev"},
		Detail: "signup returned 500",
	}}

	var out strings.Builder
	err := runHostedSmoke(context.Background(), "prod", smokeOptions{}, status,
		(&recordingProbe{result: passing()}).probe, flow, &out)
	if err == nil {
		t.Fatal("a failing flow check must fail the command even when every URL answers")
	}
}

// The JSON document is the SAME shape the cluster path emits. A second shape
// would make every consumer learn which topology produced it before it could
// read the verdict.
func TestHostedSmoke_JSONIsTheSharedShape(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(deploytarget.HostedWorkloadStatus{
		Name: "api", URL: "https://api-acme.reliantapps.dev/healthz",
	})
	var out strings.Builder
	if err := runHostedSmoke(context.Background(), "prod", smokeOptions{jsonOut: true, tag: "v1.4.0"},
		status, (&recordingProbe{result: passing()}).probe, nil, &out); err != nil {
		t.Fatal(err)
	}
	var doc smokeJSONReport
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("not the shared smoke document: %v\n%s", err, out.String())
	}
	if doc.Env != "prod" || doc.Tag != "v1.4.0" {
		t.Errorf("doc = %+v", doc)
	}
	if len(doc.Routes) != 1 || doc.Routes[0].Result != "PASS" {
		t.Fatalf("routes = %+v", doc.Routes)
	}
	// The workload and the probed URL must both be legible.
	if doc.Routes[0].RouteName != "api" || !strings.Contains(doc.Routes[0].Host, "api-acme") {
		t.Errorf("route = %+v", doc.Routes[0])
	}
	if doc.Routes[0].Path != "/healthz" {
		t.Errorf("path = %q, want the URL's own path", doc.Routes[0].Path)
	}
	if !doc.Summary.OK {
		t.Error("summary.ok must be true for a clean run")
	}
}

// hostedSmokeTargets is pure and its order is stable, so the report and the
// document are deterministic for a given status.
func TestHostedSmokeTargets_OrderIsStable(t *testing.T) {
	t.Parallel()
	status := hostedStatusWith(
		deploytarget.HostedWorkloadStatus{Name: "web", URL: "https://web.dev",
			Domains: []deploytarget.HostedCustomDomain{
				{Domain: "z.example.com", State: deploytarget.DomainStateLive},
				{Domain: "a.example.com", State: deploytarget.DomainStateLive},
			}},
		deploytarget.HostedWorkloadStatus{Name: "api", URL: "https://api.dev"},
	)
	var got []string
	for _, tgt := range hostedSmokeTargets(status) {
		got = append(got, tgt.URL)
	}
	want := []string{"https://api.dev", "https://web.dev", "https://a.example.com", "https://z.example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// An env the control plane reports as empty says so, because "no workloads"
// and "workloads without URLs" need different next steps.
func TestHostedSmoke_EmptyEnvAsksWhetherDeployHasRun(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := runHostedSmoke(context.Background(), "prod", smokeOptions{},
		hostedStatusWith(), (&recordingProbe{}).probe, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "forge env deploy prod") {
		t.Errorf("an empty env should point at deploy:\n%s", out.String())
	}
}
