package cluster

import (
	"context"
	"strings"
	"testing"
)

// Chart cluster targeting: an operator must be installed where its custom
// resources land.
//
// THE GAP. A forge.HelmChart always landed in the env's PRIMARY cluster.
// Measured on the live dev clusters before this change, with control-plane's
// dev declaring CNPG as a HelmChart:
//
//	k3d-control-plane: clusters.postgresql.cnpg.io   CRD present, operator Ready
//	k3d-cp-daemon:     NotFound
//
// But that project's managed-database tier is configured to place customer
// workloads on its SECOND cluster, so it renders every
// `postgresql.cnpg.io/v1 Cluster` object into cp-daemon — an apiserver with
// no such kind:
//
//	$ kubectl --context k3d-cp-daemon apply -f cnpg-cluster.yaml
//	error: resource mapping not found ... no matches for kind "Cluster" in
//	version "postgresql.cnpg.io/v1"; ensure CRDs are installed first
//
// WHAT THESE TESTS ASSERT, and why it is the argv. The failure mode this
// closes is "installed nowhere useful, reported success", and every way of
// getting it wrong is invisible from inside a helper: a context threaded into
// the apply but not the Established wait would report a CRD the target cluster
// does not have, and the deploy would pass. So these start from the declared
// HelmChartSpec — where the deploy starts — and assert `--context` on the REAL
// kubectl argv of every call the chart's sequence makes, correlated with the
// manifests that rode it. Severing the wiring in applyRenderedCharts
// (`chartContext(kctx, rc.spec)` → `kctx`) leaves chartContext perfectly
// correct in isolation and turns these red, which is the point.

// Context returns the value of this call's `--context` flag, or "" when the
// call passed none. Paired with kubectlCall.Namespace (helm_namespace_test.go),
// this is what lets a test say "the Deployment was applied to THIS cluster in
// THIS namespace" about a real invocation.
func (c kubectlCall) Context() string {
	fields := strings.Fields(c.Args)
	for i, f := range fields {
		if f == "--context" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// IsWaitFor reports whether this call is a `kubectl wait` for the named
// condition — `Established` (the CRD gate) or `Available` (the controller
// gate). The waits are the calls a partial fix silently leaves pointing at the
// wrong apiserver.
func (c kubectlCall) IsWaitFor(condition string) bool {
	fields := strings.Fields(c.Args)
	isWait := false
	for _, f := range fields {
		if f == "wait" {
			isWait = true
		}
	}
	return isWait && strings.Contains(c.Args, "condition="+condition)
}

// cnpgShapedRender is a CNPG-shaped chart render: a CRD (so the Established
// wait fires), the synthesized Namespace, a ConfigMap (config pass), and a
// controller Deployment (so the Available wait fires). It spans every kubectl
// call the chart sequence makes, which is what makes a partial re-target
// visible.
const cnpgShapedRender = `apiVersion: v1
kind: Namespace
metadata:
  name: cnpg-system
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: clusters.postgresql.cnpg.io
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cnpg-controller-manager-config
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cnpg-controller-manager
spec: {}`

// TestApplyRenderedCharts_ChartClusterRetargetsEveryKubectlCall is THE
// regression test. A chart declaring its own cluster must have its manifests,
// its CRD-Established wait, its controller-Available wait and its riding
// manifests ALL run against that cluster's context — and none of them against
// the env's primary.
//
// The Established wait is the assertion that matters most. `kubectl wait
// --for=condition=Established crd/clusters.postgresql.cnpg.io` against the
// PRIMARY apiserver succeeds, because the CRD really is Established there —
// it just is not on the cluster the operator was supposed to be installed
// into. A deploy that applied to cp-daemon and waited on control-plane would
// report success for a chart whose CRDs the target cluster does not serve.
func TestApplyRenderedCharts_ChartClusterRetargetsEveryKubectlCall(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	// Exactly what the CLI builds for a re-targeted CNPG: the env's primary
	// context is k3d-control-plane, the chart declares k3d-cp-daemon.
	charts := []renderedChart{{
		spec: HelmChartSpec{
			Name:      "cloudnative-pg",
			Namespace: "cnpg-system",
			Cluster:   "k3d-cp-daemon",
		},
		manifests: cnpgShapedRender,
		extra: `apiVersion: v1
kind: ConfigMap
metadata:
  name: riding-config`,
	}}

	if err := applyRenderedCharts(context.Background(), "k3d-control-plane", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	calls := readCalls()
	if len(calls) == 0 {
		t.Fatal("no kubectl invocations recorded")
	}

	// NOTHING in a re-targeted chart's sequence may touch the primary.
	for _, c := range calls {
		if got := c.Context(); got != "k3d-cp-daemon" {
			t.Errorf("a re-targeted chart's kubectl ran against context %q, want "+
				"\"k3d-cp-daemon\"; argv: %s", got, c.Args)
		}
	}

	// The apply that carried the operator Deployment — the object whose
	// absence from cp-daemon was the observed symptom.
	deploy := findApplyCarrying(t, calls, "kind: Deployment")
	if deploy.Context() != "k3d-cp-daemon" {
		t.Errorf("the operator Deployment must be applied to the chart's cluster; "+
			"got context %q, argv: %s", deploy.Context(), deploy.Args)
	}
	// The namespace fix (57096c5e) must keep working for a re-targeted chart:
	// the two flags are threaded through the same calls and a change to one
	// must not drop the other.
	if deploy.Namespace() != "cnpg-system" {
		t.Errorf("a re-targeted chart must still be applied into its declared "+
			"namespace; got %q, argv: %s", deploy.Namespace(), deploy.Args)
	}

	// The apply that carried the CRD, and then the wait that gates on it.
	crdApply := findApplyCarrying(t, calls, "kind: CustomResourceDefinition")
	if crdApply.Context() != "k3d-cp-daemon" {
		t.Errorf("the CRDs must be applied to the chart's cluster; got context %q, argv: %s",
			crdApply.Context(), crdApply.Args)
	}

	// THE ESTABLISHED WAIT. Waiting on the wrong apiserver reports Established
	// for a CRD the target does not have.
	established := findWait(t, calls, "Established")
	if established.Context() != "k3d-cp-daemon" {
		t.Errorf("the CRD-Established wait must run against the chart's cluster — waiting "+
			"on the primary apiserver reports Established for a CRD the target does not "+
			"have; got context %q, argv: %s", established.Context(), established.Args)
	}
	if !strings.Contains(established.Args, "crd/clusters.postgresql.cnpg.io") {
		t.Errorf("the Established wait must name the chart's CRDs; argv: %s", established.Args)
	}

	// The controller-Available wait gates the riding manifests.
	available := findWait(t, calls, "Available")
	if available.Context() != "k3d-cp-daemon" {
		t.Errorf("the controller-Available wait must run against the chart's cluster; "+
			"got context %q, argv: %s", available.Context(), available.Args)
	}

	// Riding manifests are applied from a DIFFERENT call site
	// (applyRidingManifestsWithRetry) and must follow the chart.
	riding := findApplyCarrying(t, calls, "riding-config")
	if riding.Context() != "k3d-cp-daemon" {
		t.Errorf("a chart's riding manifests must be applied to the chart's cluster; "+
			"got context %q, argv: %s", riding.Context(), riding.Args)
	}
}

// TestApplyRenderedCharts_NoChartClusterUsesTheEnvContext is the compatibility
// guarantee, asserted where it can actually be broken. Envoy Gateway and Flux
// declare no cluster, and their argv must be identical to before the field
// existed — every call against the env's primary context.
func TestApplyRenderedCharts_NoChartClusterUsesTheEnvContext(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	charts := []renderedChart{{
		spec:      HelmChartSpec{Name: "flux", Namespace: "flux-system"},
		manifests: fluxShapedRender,
	}}

	if err := applyRenderedCharts(context.Background(), "k3d-control-plane", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	calls := readCalls()
	if len(calls) == 0 {
		t.Fatal("no kubectl invocations recorded")
	}
	for _, c := range calls {
		if got := c.Context(); got != "k3d-control-plane" {
			t.Errorf("a chart declaring no cluster must run against the env's primary "+
				"context; got %q, argv: %s", got, c.Args)
		}
	}
}

// TestApplyRenderedCharts_PerChartContextsDoNotBleed pins the per-chart
// resolution. Two charts in ONE apply — one re-targeted, one not — must each
// reach their own cluster. A fix that resolved the context once for the whole
// loop (rather than per chart) would send both to the same place and still
// pass the two single-chart tests above.
func TestApplyRenderedCharts_PerChartContextsDoNotBleed(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	charts := []renderedChart{
		{
			spec:      HelmChartSpec{Name: "flux", Namespace: "flux-system"},
			manifests: fluxShapedRender,
		},
		{
			spec: HelmChartSpec{
				Name:      "cloudnative-pg",
				Namespace: "cnpg-system",
				Cluster:   "k3d-cp-daemon",
			},
			manifests: cnpgShapedRender,
		},
	}

	if err := applyRenderedCharts(context.Background(), "k3d-control-plane", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	calls := readCalls()

	// Flux's controller stays on the primary.
	fluxDeploy := findApplyCarrying(t, calls, "source-controller")
	if fluxDeploy.Context() != "k3d-control-plane" {
		t.Errorf("flux (no cluster) must stay on the primary; got context %q, argv: %s",
			fluxDeploy.Context(), fluxDeploy.Args)
	}
	// CNPG's goes to cp-daemon.
	cnpgDeploy := findApplyCarrying(t, calls, "cnpg-controller-manager")
	if cnpgDeploy.Context() != "k3d-cp-daemon" {
		t.Errorf("cloudnative-pg (re-targeted) must go to cp-daemon; got context %q, argv: %s",
			cnpgDeploy.Context(), cnpgDeploy.Args)
	}
	// And CNPG's Established wait must not have been aimed at the primary
	// just because the preceding chart in the loop lived there.
	cnpgWait := findWaitCarrying(t, calls, "crd/clusters.postgresql.cnpg.io")
	if cnpgWait.Context() != "k3d-cp-daemon" {
		t.Errorf("the re-targeted chart's Established wait must not inherit the previous "+
			"chart's context; got %q, argv: %s", cnpgWait.Context(), cnpgWait.Args)
	}
}

// TestChartContext pins the resolution rule directly, including the
// whitespace-only case (which must read as unset rather than as a context
// named " ", since kubectl would reject that with an opaque error).
func TestChartContext(t *testing.T) {
	if got := chartContext("k3d-primary", HelmChartSpec{Cluster: "k3d-cp-daemon"}); got != "k3d-cp-daemon" {
		t.Errorf("a declared chart cluster must win; got %q", got)
	}
	for _, unset := range []string{"", "  ", "\t"} {
		if got := chartContext("k3d-primary", HelmChartSpec{Cluster: unset}); got != "k3d-primary" {
			t.Errorf("chart cluster %q must resolve to the env context; got %q", unset, got)
		}
	}
	// An env with no context and a chart with none stays empty, so the
	// KubectlApply chokepoint refuses the write rather than falling back to
	// whatever context happens to be active.
	if got := chartContext("", HelmChartSpec{}); got != "" {
		t.Errorf("no env context and no chart cluster must stay empty; got %q", got)
	}
}

// findWait returns the `kubectl wait` call for the named condition.
func findWait(t *testing.T, calls []kubectlCall, condition string) kubectlCall {
	t.Helper()
	for _, c := range calls {
		if c.IsWaitFor(condition) {
			return c
		}
	}
	t.Fatalf("no kubectl wait for condition %q; calls:\n%s", condition, renderCalls(calls))
	return kubectlCall{}
}

// findWaitCarrying returns the `kubectl wait` call whose argv names marker —
// used to pick one chart's wait out of a multi-chart run.
func findWaitCarrying(t *testing.T, calls []kubectlCall, marker string) kubectlCall {
	t.Helper()
	for _, c := range calls {
		if c.IsWaitFor("Established") && strings.Contains(c.Args, marker) {
			return c
		}
	}
	t.Fatalf("no Established wait named %q; calls:\n%s", marker, renderCalls(calls))
	return kubectlCall{}
}
