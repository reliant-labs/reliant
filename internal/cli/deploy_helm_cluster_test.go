package cli

import (
	"context"
	"strings"
	"testing"
)

// A chart naming a cluster the env does not declare is an ERROR, not a
// fallback to the primary.
//
// WHY THE DIRECTION MATTERS. Falling back would produce a cluster that looks
// entirely healthy — the operator 1/1 Ready, its CRDs Established, the deploy
// green — while the custom resources it exists to reconcile go to a different
// apiserver with no such kind. The failure then surfaces much later and
// somewhere else, as a `no matches for kind` on whichever tier renders the CRs,
// which reads as a bug in that tier. Refusing at deploy time names the actual
// mistake (an undeclared or mistyped cluster) while it is still cheap to fix.
//
// These tests drive helmChartSpecsFromEntities, the seam where the KCL entity
// becomes a cluster.HelmChartSpec, because that is where the env's declared
// clusters and the chart's requested one are both in scope.

// TestHelmChartSpecs_UndeclaredClusterIsRefused is the load-bearing case: a
// chart asking for a cluster this env does not declare must fail, and the
// message must name both what was asked for and what is available — a bare
// "unknown cluster" leaves the user guessing whether the name or the context
// form is wrong.
func TestHelmChartSpecs_UndeclaredClusterIsRefused(t *testing.T) {
	charts := []HelmChartEntity{{
		Name:      "cloudnative-pg",
		OCI:       "oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg",
		Version:   "0.29.0",
		Namespace: "cnpg-system",
		Cluster:   "k3d-nonexistent",
	}}
	declared := []ClusterEntity{
		{Name: "control-plane", Context: "k3d-control-plane"},
		{Name: "cp-daemon", Context: "k3d-cp-daemon"},
	}

	_, err := helmChartSpecsFromEntities(context.Background(), charts, declared, "")
	if err == nil {
		t.Fatal("a chart targeting an undeclared cluster must be refused, not applied " +
			"to the primary — silently installing an operator on the wrong cluster " +
			"reports success for a chart whose CRs land elsewhere")
	}
	for _, want := range []string{"cloudnative-pg", "k3d-nonexistent", "k3d-cp-daemon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the user can see what was asked for and what "+
				"is available; got: %v", want, err)
		}
	}
}

// TestHelmChartSpecs_BareClusterNameIsRefused pins the anti-drift half. The
// entity carries the DERIVED kubectl context, so a value that is the cluster's
// bare `name` cannot have come from a forge.Cluster reference — and it is not a
// context kubectl would accept. Matching it leniently would re-admit exactly
// the name-vs-context confusion the reference-typed schema field exists to
// prevent.
func TestHelmChartSpecs_BareClusterNameIsRefused(t *testing.T) {
	charts := []HelmChartEntity{{
		Name:      "cloudnative-pg",
		Version:   "0.29.0",
		Namespace: "cnpg-system",
		// The NAME, not the context. `kubectl --context cp-daemon` does not
		// resolve; the context is `k3d-cp-daemon`.
		Cluster: "cp-daemon",
	}}
	declared := []ClusterEntity{{Name: "cp-daemon", Context: "k3d-cp-daemon"}}

	if _, err := helmChartSpecsFromEntities(context.Background(), charts, declared, ""); err == nil {
		t.Fatal("a chart cluster given as the bare cluster NAME must be refused: forge " +
			"applies with `kubectl --context`, and the context is k3d-<name>")
	}
}

// TestHelmChartSpecs_DeclaredClusterIsAccepted is the positive path: the
// context reaches the spec so cluster.applyRenderedCharts can route the chart.
func TestHelmChartSpecs_DeclaredClusterIsAccepted(t *testing.T) {
	charts := []HelmChartEntity{{
		Name:      "cloudnative-pg",
		Version:   "0.29.0",
		Namespace: "cnpg-system",
		Cluster:   "k3d-cp-daemon",
	}}
	declared := []ClusterEntity{
		{Name: "control-plane", Context: "k3d-control-plane"},
		{Name: "cp-daemon", Context: "k3d-cp-daemon"},
	}

	specs, err := helmChartSpecsFromEntities(context.Background(), charts, declared, "")
	if err != nil {
		t.Fatalf("a chart targeting a declared cluster must be accepted: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(specs))
	}
	if specs[0].Cluster != "k3d-cp-daemon" {
		t.Errorf("the declared cluster must reach the spec so the apply can route the "+
			"chart; got %q", specs[0].Cluster)
	}
}

// TestHelmChartSpecs_NoClusterNeedsNoDeclaredClusters is the compatibility
// guarantee. Every chart declaration that predates this field carries no
// cluster, and such a chart must resolve with no validation at all — including
// in an env that declares no `clusters` list (every cloud env: staging, prod,
// which deploy to a pre-existing cluster forge does not create).
func TestHelmChartSpecs_NoClusterNeedsNoDeclaredClusters(t *testing.T) {
	charts := []HelmChartEntity{
		{Name: "envoy-gateway", Version: "v1.7.2", Namespace: "envoy-gateway-system"},
		{Name: "flux", Version: "2.19.1", Namespace: "flux-system"},
	}

	specs, err := helmChartSpecsFromEntities(context.Background(), charts, nil, "")
	if err != nil {
		t.Fatalf("charts declaring no cluster must resolve with no declared clusters "+
			"(staging/prod declare none): %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2", len(specs))
	}
	for _, s := range specs {
		if s.Cluster != "" {
			t.Errorf("chart %q must carry no cluster so the apply uses the env's primary "+
				"context; got %q", s.Name, s.Cluster)
		}
	}
}

// The declared namespace metadata, the prune setting and the owner reach the
// spec. Prune is a pointer on the entity so a contract that omits it reads as
// the schema default (on); a spec built by hand defaults to OFF, so it can
// never delete anything it did not mean to.
func TestHelmChartSpecs_NamespaceMetadataPruneAndOwnerReachTheSpec(t *testing.T) {
	off := false
	charts := []HelmChartEntity{
		{
			Name: "flux", Version: "2.19.1", Namespace: "flux-system",
			NamespaceLabels:      map[string]string{"pod-security.kubernetes.io/enforce": "restricted"},
			NamespaceAnnotations: map[string]string{"owner": "sre"},
		},
		{Name: "legacy", Version: "1", Namespace: "legacy", Prune: &off},
	}
	specs, err := helmChartSpecsFromEntities(context.Background(), charts, nil, "acme.prod")
	if err != nil {
		t.Fatalf("helmChartSpecsFromEntities: %v", err)
	}
	if got := specs[0].NamespaceLabels["pod-security.kubernetes.io/enforce"]; got != "restricted" {
		t.Errorf("namespace labels did not reach the spec: %v", specs[0].NamespaceLabels)
	}
	if specs[0].NamespaceAnnotations["owner"] != "sre" {
		t.Errorf("namespace annotations did not reach the spec: %v", specs[0].NamespaceAnnotations)
	}
	for _, s := range specs {
		if s.Owner != "acme.prod" {
			t.Errorf("%s: owner = %q, want acme.prod", s.Name, s.Owner)
		}
	}
	if !specs[0].Prune {
		t.Error("an entity that does not mention prune must default to pruning")
	}
	if specs[1].Prune {
		t.Error("prune = false must reach the spec")
	}
}
