package cluster

import (
	"context"
	"strings"
	"testing"
)

// gatewayStream is an env render whose only CRD-dependent object is a Gateway
// — the shape control-plane's dev-k8s/e2e bundles carry. The Gateway is a
// pre-rollout SUPPORT object (not a workload), so it is applied by the
// pre-rollout gate, which is the pass that failed in CI.
const gatewayStream = `apiVersion: v1
kind: Namespace
metadata:
  name: control-plane-dev-k8s
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public
  namespace: control-plane-dev-k8s
spec:
  gatewayClassName: eg
  allowedListeners:
    namespaces:
      from: Same
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: control-plane-dev-k8s
spec: {}`

// gatewayAPICRDBundle stands in for the forge-supplied `gateway-api` bundle a
// chart carries: the Gateway CRD whose schema must be registered before the
// Gateway above can be server-side applied.
const gatewayAPICRDBundle = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gateways.gateway.networking.k8s.io
spec:
  group: gateway.networking.k8s.io`

// envoyGatewayChart is the rendered (--skip-crds) controller half of the
// envoy-gateway platform dependency, carrying its forge-supplied CRDs.
const envoyGatewayChart = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-gateway
  namespace: envoy-gateway-system
spec: {}`

// TestApplyRendered_ChartCRDsEstablishBeforeTheGatewayThatUsesThem is the
// ordering guarantee a project relies on when it deletes a hand-rolled
// `kubectl apply -f .../standard-install.yaml` CI step and lets
// `forge env deploy` install the Gateway API CRDs declaratively instead.
//
// control-plane's k3d smoke test installed those CRDs itself, from a
// hardcoded v1.2.1 that predates Gateway.spec.allowedListeners, and the
// deploy failed with
//
//	failed to create typed patch object (.../Gateway):
//	.spec.allowedListeners: field not declared in schema
//
// The fix is to delete the step — forge already pins the bundle (v1.6.2,
// standard channel, matching prod) and applies it via `crds = "gateway-api"`.
// That fix is only correct if the chart's CRDs are applied AND Established
// before the env stream's Gateway is sent. If they were not, deleting the CI
// step would trade a stale-schema failure for a race, so this test pins the
// ordering rather than leaving it to be rediscovered in CI.
func TestApplyRendered_ChartCRDsEstablishBeforeTheGatewayThatUsesThem(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)

	// applyRendered applies the platform charts FIRST and then the env
	// stream, in one sequence against one recorded kubectl. The chart is
	// pre-rendered here (the renderedChart the CLI's renderSelectedCharts
	// produces) so the test does not shell out to a real `helm template`;
	// everything after that point — CRD-first, Established wait, then the
	// env's own passes — is the real production code path.
	opts := ApplyOpts{
		Namespace: "control-plane-dev-k8s",
		Context:   "k3d-test",
		Rollout:   RolloutPolicy{Mode: RolloutSkip},
	}
	charts := []renderedChart{{
		spec: HelmChartSpec{
			Name:      "envoy-gateway",
			Namespace: "envoy-gateway-system",
			CRDBundle: "gateway-api",
		},
		crds:      gatewayAPICRDBundle,
		manifests: envoyGatewayChart,
	}}
	if err := applyRenderedCharts(context.Background(), opts.Context, charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	if err := applyRendered(context.Background(), opts, gatewayStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	calls := readCalls()

	crdApply := firstIndex(calls, appliesKind("CustomResourceDefinition"))
	wait := firstIndex(calls, func(c kubectlCall) bool {
		return strings.Contains(c.Args, "--for=condition=Established") &&
			strings.Contains(c.Args, "crd/gateways.gateway.networking.k8s.io")
	})
	gateway := firstIndex(calls, appliesKind("Gateway"))

	if crdApply < 0 || wait < 0 || gateway < 0 {
		t.Fatalf("missing a step (crd apply=%d, established wait=%d, gateway apply=%d):\n%v",
			crdApply, wait, gateway, calls)
	}
	if !(crdApply < wait && wait < gateway) {
		t.Errorf("order: chart crd apply=%d, established wait=%d, gateway apply=%d — "+
			"the Gateway must be applied only after its CRD is Established", crdApply, wait, gateway)
	}
	// The Gateway must not ride the CRD's own apply: that is a single
	// server-side apply racing a schema registration.
	if strings.Contains(calls[crdApply].Stdin, "\nkind: Gateway\n") {
		t.Errorf("the Gateway rode the CRD apply:\n%s", calls[crdApply].Stdin)
	}
}
