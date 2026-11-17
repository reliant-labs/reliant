package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- fakes ---------------------------------------------------------------

// fakeServedKinds reports a fixed served-kind set, or an error to model a
// discovery failure (kubectl unreachable / RBAC denial). The served set is
// keyed by servedKindKey(group, kind) so the fake mirrors the live checker's
// key shape exactly.
type fakeServedKinds struct {
	served map[string]struct{}
	err    error
}

func (f fakeServedKinds) ServedKinds(_ context.Context, _ string) (map[string]struct{}, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.served, nil
}

// servedSet builds a served-kind set from (group, kind) pairs passed as
// alternating args: servedSet("apps", "Deployment", "", "Service").
func servedSet(groupKind ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i+1 < len(groupKind); i += 2 {
		out[servedKindKey(groupKind[i], groupKind[i+1])] = struct{}{}
	}
	return out
}

// grpcRouteManifest renders a Gateway-API GRPCRoute alongside a core Service —
// the exact shape from the prod incident (daemon-gateway's route on a cluster
// missing the GRPCRoute CRD). The Service is a CORE kind that must never be
// gated; the GRPCRoute is the non-core kind whose CRD the cluster may lack.
const grpcRouteManifest = `
apiVersion: v1
kind: Service
metadata:
  name: daemon-gateway
  namespace: app-prod
spec:
  ports:
    - port: 29190
---
apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata:
  name: daemon-gateway-route
  namespace: app-prod
spec:
  parentRefs:
    - name: daemon-gateway
`

// --- CollectManifestGVKs -------------------------------------------------

func TestCollectManifestGVKs(t *testing.T) {
	gvks := CollectManifestGVKs(grpcRouteManifest)
	if len(gvks) != 2 {
		t.Fatalf("expected 2 top-level GVKs, got %d: %+v", len(gvks), gvks)
	}
	// Document order is preserved.
	if gvks[0].Kind != "Service" || gvks[0].APIVersion != "v1" || gvks[0].Name != "daemon-gateway" {
		t.Errorf("doc[0]: got %+v", gvks[0])
	}
	if gvks[1].Kind != "GRPCRoute" ||
		gvks[1].APIVersion != "gateway.networking.k8s.io/v1" ||
		gvks[1].Name != "daemon-gateway-route" {
		t.Errorf("doc[1]: got %+v", gvks[1])
	}
	// group() splits the API group off apiVersion; core kinds report "".
	if gvks[0].group() != "" {
		t.Errorf("Service is core-group; group() should be empty, got %q", gvks[0].group())
	}
	if gvks[1].group() != "gateway.networking.k8s.io" {
		t.Errorf("GRPCRoute group(): got %q", gvks[1].group())
	}
}

// A nested pod-template `kind` (not a top-level applied object) must NOT be
// collected — only the document's own apiVersion/kind is gated.
func TestCollectManifestGVKs_IgnoresNestedKinds(t *testing.T) {
	gvks := CollectManifestGVKs(deploymentManifest)
	for _, g := range gvks {
		if g.Kind != "Deployment" {
			t.Errorf("only the top-level Deployment kind should be collected; got %q", g.Kind)
		}
	}
	if len(gvks) != 1 {
		t.Fatalf("expected exactly 1 top-level GVK, got %d: %+v", len(gvks), gvks)
	}
}

// Documents missing apiVersion or kind (a comment-only chunk, a malformed doc)
// are skipped rather than collected as an empty GVK.
func TestCollectManifestGVKs_SkipsMalformed(t *testing.T) {
	const m = `
# just a comment
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ok
---
metadata:
  name: no-kind
`
	gvks := CollectManifestGVKs(m)
	if len(gvks) != 1 || gvks[0].Kind != "Deployment" {
		t.Fatalf("expected only the well-formed Deployment, got %+v", gvks)
	}
}

// --- Preflight CRD gate --------------------------------------------------

// The motivating footgun: a GRPCRoute whose CRD is absent from the cluster's
// served set must BLOCK the deploy (not warn, not pass).
func TestPreflight_MissingCRDBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: grpcRouteManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		// Cluster serves core Service + apps but NOT GRPCRoute.
		ServedKinds: fakeServedKinds{served: servedSet(
			"", "Service",
			"apps", "Deployment",
		)},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("a manifest whose CRD is not installed must BLOCK the deploy")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GRPCRoute") {
		t.Errorf("error should name the missing Kind; got: %s", msg)
	}
	if !strings.Contains(msg, "gateway.networking.k8s.io/v1") {
		t.Errorf("error should name the apiVersion; got: %s", msg)
	}
	if !strings.Contains(msg, "daemon-gateway-route") {
		t.Errorf("error should name the requiring manifest; got: %s", msg)
	}
	// Must NOT false-positive on the core Service kind.
	if strings.Contains(msg, "Service") {
		t.Errorf("core kind Service must not be reported as a missing CRD; got: %s", msg)
	}
}

// When the cluster DOES serve the non-core kind, the gate passes.
func TestPreflight_CRDPresentPasses(t *testing.T) {
	opts := PreflightOpts{
		Manifests: grpcRouteManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		ServedKinds: fakeServedKinds{served: servedSet(
			"", "Service",
			"gateway.networking.k8s.io", "GRPCRoute",
		)},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("served CRD must pass the gate; got: %v", err)
	}
}

// A bundle of ONLY core kinds is never gated — even with an empty served set,
// the CRD check must not false-positive (core kinds are served by every
// cluster; gating them would block on a discovery blind spot).
func TestPreflight_CoreKindsNeverGated(t *testing.T) {
	const coreOnly = `
apiVersion: v1
kind: Service
metadata:
  name: svc
  namespace: app-prod
spec:
  ports:
    - port: 80
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
  namespace: app-prod
`
	opts := PreflightOpts{
		Manifests:   coreOnly,
		Context:     "gke_prod",
		Namespace:   "app-prod",
		ServedKinds: fakeServedKinds{served: servedSet()}, // serves NOTHING
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("core-only bundle must pass even against an empty served set; got: %v", err)
	}
}

// A discovery FAILURE (kubectl unreachable / RBAC) aborts the preflight with
// the underlying error — the served set is unknown, so the gate must NOT
// silently pass (nor blanket-block every kind).
func TestPreflight_DiscoveryFailureAborts(t *testing.T) {
	sentinel := errors.New("the server doesn't have a resource type")
	opts := PreflightOpts{
		Manifests:   grpcRouteManifest,
		Context:     "gke_prod",
		Namespace:   "app-prod",
		ServedKinds: fakeServedKinds{err: sentinel},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("a discovery failure must abort the preflight, not pass")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("abort error should wrap the discovery failure; got: %v", err)
	}
}

// With no target Context, the CRD gate is skipped (host-only / local env with
// nothing to verify) — even with a checker configured.
func TestPreflight_NoContextSkipsCRDCheck(t *testing.T) {
	opts := PreflightOpts{
		Manifests:   grpcRouteManifest,
		Namespace:   "app-prod",
		ServedKinds: fakeServedKinds{served: servedSet()}, // would block if it ran
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("no context must skip the CRD check; got: %v", err)
	}
}

// The structured result records a MissingCRD distinctly, and OK() reports the
// run as failing.
func TestRunPreflightChecks_MissingCRDIsBlock(t *testing.T) {
	opts := PreflightOpts{
		Manifests:   grpcRouteManifest,
		Context:     "gke_prod",
		Namespace:   "app-prod",
		ServedKinds: fakeServedKinds{served: servedSet("", "Service")},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("a missing CRD is a structured block, not an abort: %v", err)
	}
	if len(res.MissingCRDs) != 1 {
		t.Fatalf("expected one missing CRD, got %v", res.MissingCRDs)
	}
	if res.OK() {
		t.Error("a run with a missing CRD must not be OK()")
	}
}

// --- parseServedKinds ----------------------------------------------------

// parseServedKinds handles the real `kubectl api-resources --no-headers -o
// wide` column layout for core, grouped, and shortname-bearing rows.
func TestParseServedKinds(t *testing.T) {
	// Columns: NAME [SHORTNAMES] APIVERSION NAMESPACED KIND VERBS [CATEGORIES]
	const out = `configmaps                    cm           v1                                  true    ConfigMap     create,delete,get,list
services                      svc          v1                                  true    Service       create,delete,get,list
deployments                   deploy       apps/v1                             true    Deployment    create,delete,get,list
grpcroutes                                 gateway.networking.k8s.io/v1        true    GRPCRoute     create,delete,get,list
namespaces                    ns           v1                                  false   Namespace     create,delete,get,list`

	served := parseServedKinds(out)

	cases := []struct {
		group, kind string
		want        bool
	}{
		{"", "ConfigMap", true},
		{"", "Service", true},
		{"apps", "Deployment", true},
		{"gateway.networking.k8s.io", "GRPCRoute", true},
		{"", "Namespace", true},
		{"gateway.networking.k8s.io", "HTTPRoute", false}, // not in output
	}
	for _, c := range cases {
		if got := ServesKind(served, c.group, c.kind); got != c.want {
			t.Errorf("ServesKind(%q,%q)=%v, want %v (served=%v)", c.group, c.kind, got, c.want, served)
		}
	}
}

// looksLikeBareVersion distinguishes a core-group APIVERSION token from a
// resource NAME so the parser can locate the APIVERSION column.
func TestLooksLikeBareVersion(t *testing.T) {
	cases := map[string]bool{
		"v1":          true,
		"v2":          true,
		"v1beta1":     true,
		"v2beta3":     true,
		"v1alpha1":    true,
		"apps":        false,
		"deployments": false,
		"v":           false,
		"version":     false,
		"":            false,
	}
	for in, want := range cases {
		if got := looksLikeBareVersion(in); got != want {
			t.Errorf("looksLikeBareVersion(%q)=%v, want %v", in, got, want)
		}
	}
}

// A kind whose CRD the same bundle renders is not missing: the apply sends the
// stream's CRDs in an early batch, before the objects that use them. This was
// control-plane's DaemonRollout: the release that introduced the CRD and its
// first object was blocked on "CRD not served" by itself (`--skip-preflight`).
func TestRunPreflightChecks_KindDefinedByABundleCRDIsNotMissing(t *testing.T) {
	const crdAndInstance = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: daemonrollouts.reliant.dev
spec:
  group: reliant.dev
  names:
    kind: DaemonRollout
    plural: daemonrollouts
  scope: Namespaced
---
apiVersion: reliant.dev/v1alpha1
kind: DaemonRollout
metadata:
  name: daemon-rollout
---
apiVersion: other.dev/v1
kind: Widget
metadata:
  name: still-missing
`
	opts := PreflightOpts{
		Manifests:   crdAndInstance,
		Context:     "gke_prod",
		Namespace:   "app-prod",
		ServedKinds: fakeServedKinds{served: servedSet("apiextensions.k8s.io", "CustomResourceDefinition")},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingCRDs) != 1 || !strings.Contains(res.MissingCRDs[0], "Widget") {
		t.Fatalf("MissingCRDs = %v, want only Widget (DaemonRollout's CRD ships in the bundle)", res.MissingCRDs)
	}
}
