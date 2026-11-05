package cli

import (
	"strings"
	"testing"
)

// inTargetSet membership semantics (empty filter matches everything;
// non-empty is an exact-name allowlist) are covered by up_test.go's
// TestInTargetSet — the helper is shared with `forge env up`'s --target.

func operatorWL(name string) WorkloadEntity {
	w := clusterWL(name, "k3d-x", "ns")
	w.Kind = "operator"
	return w
}

// TestFilterEntitiesByTarget confirms the entity-layer filter narrows
// Workloads (of every kind), Infra and Frontends to the targeted names while
// carrying every other entity slice through unchanged.
func TestFilterEntitiesByTarget(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("admin-server"), hostWL("workspace-proxy"),
			operatorWL("workspace-controller"), operatorWL("billing-controller"),
		},
		Frontends: []FrontendEntity{{Name: "admin-ui"}, {Name: "marketing"}},
		Gateways:  []GatewayEntity{{Name: "gw"}},
	}
	got := filterEntitiesByTarget(e, []string{"admin-server", "admin-ui"})

	if names := got.WorkloadNames(""); len(names) != 1 || names[0] != "admin-server" {
		t.Errorf("workloads: got %v, want [admin-server]", names)
	}
	if len(got.Frontends) != 1 || got.Frontends[0].Name != "admin-ui" {
		t.Errorf("frontends: got %+v, want [admin-ui]", got.Frontends)
	}
	// Non-app-name entities are carried through untouched.
	if len(got.Gateways) != 1 {
		t.Errorf("gateways should be carried through unchanged, got %+v", got.Gateways)
	}
	// The original must not be mutated (shallow copy of the struct).
	if len(e.Workloads) != 4 {
		t.Errorf("input entities mutated: %+v", e.Workloads)
	}
}

// Naming an operator keeps just that operator: every workload kind is a
// first-class --target subject.
func TestFilterEntitiesByTarget_Operator(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{hostWL("admin-server"), operatorWL("workspace-controller"), operatorWL("billing-controller")},
		Frontends: []FrontendEntity{{Name: "admin-ui"}},
	}
	got := filterEntitiesByTarget(e, []string{"workspace-controller"})
	if names := got.WorkloadNames(""); len(names) != 1 || names[0] != "workspace-controller" {
		t.Errorf("workloads: got %v, want [workspace-controller]", names)
	}
	if len(got.Frontends) != 0 {
		t.Errorf("frontends should be empty when only an operator is targeted, got %+v", got.Frontends)
	}
}

// TestFilterEntitiesToFrontendsOnly confirms the --frontends-only narrowing
// drops EVERY non-frontend kind, which is what makes runDeploy's
// frontend-only guard engage (no cluster pipeline, only the frontend
// dispatch).
func TestFilterEntitiesToFrontendsOnly(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{clusterWL("admin-server", "k3d-x", "ns"), operatorWL("workspace-controller")},
		Infra:     []HostInfraEntity{{Name: "postgres"}},
		Frontends: []FrontendEntity{
			{Name: "reliant-web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
			{Name: "admin-web", Runtime: FrontendRuntime{Type: FrontendRuntimeBuildOnly}}, // bundled by reliant-web; must survive
		},
		Gateways:   []GatewayEntity{{Name: "gw"}},
		HelmCharts: []HelmChartEntity{{Name: "cert-manager"}},
	}

	got := filterEntitiesToFrontendsOnly(e)

	if len(got.Frontends) != 2 {
		t.Errorf("frontends should be carried through unchanged, got %+v", got.Frontends)
	}
	if len(got.Workloads) != 0 || len(got.Infra) != 0 || len(got.Gateways) != 0 || len(got.HelmCharts) != 0 {
		t.Errorf("non-frontend kinds should be stripped, got wl=%+v infra=%+v gw=%+v helm=%+v",
			got.Workloads, got.Infra, got.Gateways, got.HelmCharts)
	}
	// The exact predicate runDeploy uses to skip the cluster pipeline.
	if !(!kclEntitiesHaveK8sCluster(got) && hasShippableFrontend(got)) {
		t.Errorf("frontendOnly guard should engage after the filter; got false")
	}
	if len(e.Workloads) != 2 {
		t.Errorf("input entities mutated: %+v", e.Workloads)
	}
}

// TestValidateDeployTargets_Unknown confirms a typo'd target errors with
// the list of available app names (every workload kind + frontends), and
// that a fully-valid target set passes.
func TestValidateDeployTargets_Unknown(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{hostWL("admin-server"), operatorWL("workspace-controller")},
		Frontends: []FrontendEntity{{Name: "admin-ui"}},
	}
	if err := validateDeployTargets(e, []string{"admin-server", "admin-ui"}); err != nil {
		t.Fatalf("valid targets should pass, got: %v", err)
	}
	err := validateDeployTargets(e, []string{"nope"})
	if err == nil {
		t.Fatal("expected error for unknown target, got nil")
	}
	for _, want := range []string{"nope", "admin-server", "admin-ui", "workspace-controller"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// An operator (any workload kind) is a valid --target subject.
func TestValidateDeployTargets_Operator(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{hostWL("admin-server"), operatorWL("workspace-controller")}}
	if err := validateDeployTargets(e, []string{"workspace-controller"}); err != nil {
		t.Fatalf("operator target should be accepted, got: %v", err)
	}
}
