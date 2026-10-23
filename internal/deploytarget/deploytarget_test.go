package deploytarget

import (
	"strings"
	"testing"
)

// TestGroupServices_K8sClusterShared confirms that services sharing the
// same (cluster, namespace, registry) tuple — the typical "single
// K8sCluster ref reused across many services" pattern — end up in one
// group.
func TestGroupServices_K8sClusterShared(t *testing.T) {
	prod := &RawK8sCluster{
		Cluster:   "prod",
		Namespace: "ns-prod",
		Spec:      &K8sClusterSpec{Replicas: 1},
	}
	groups, err := GroupServices("prod", []RawService{
		{Name: "a", K8sCluster: prod},
		{Name: "b", K8sCluster: prod},
		{Name: "c", K8sCluster: prod},
	})
	if err != nil {
		t.Fatalf("GroupServices: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group (services share cluster/ns/registry), got %d", len(groups))
	}
	if got := len(groups[0].Services); got != 3 {
		t.Errorf("group should contain 3 services, got %d", got)
	}
	if groups[0].ProviderID != "k8s-cluster" {
		t.Errorf("ProviderID: want k8s-cluster, got %q", groups[0].ProviderID)
	}
	if groups[0].Namespace != "ns-prod" {
		t.Errorf("Namespace: want ns-prod, got %q", groups[0].Namespace)
	}
}

// TestGroupServices_K8sClusterMixed confirms that services declaring
// different K8sCluster targets (different cluster or namespace) end up
// in DIFFERENT groups — each cluster-namespace tuple is one apply.
func TestGroupServices_K8sClusterMixed(t *testing.T) {
	groups, err := GroupServices("prod", []RawService{
		{Name: "a", K8sCluster: &RawK8sCluster{Cluster: "c1", Namespace: "n1", Spec: &K8sClusterSpec{}}},
		{Name: "b", K8sCluster: &RawK8sCluster{Cluster: "c1", Namespace: "n1", Spec: &K8sClusterSpec{}}},
		{Name: "c", K8sCluster: &RawK8sCluster{Cluster: "c2", Namespace: "n2", Spec: &K8sClusterSpec{}}},
	})
	if err != nil {
		t.Fatalf("GroupServices: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (mixed clusters), got %d", len(groups))
	}
}

// TestGroupServices_SkipsHostAndBuildOnly: services without a deploy
// target (all union variants nil) are skipped entirely — those are
// host / build-only / no-deploy, owned by forge run / forge build.
func TestGroupServices_SkipsHostAndBuildOnly(t *testing.T) {
	groups, err := GroupServices("dev", []RawService{
		{Name: "host-svc"},  // no K8sCluster/Compose/HostInfra → skipped
		{Name: "build-svc"}, // same
		{Name: "deployable", K8sCluster: &RawK8sCluster{Cluster: "c", Namespace: "n", Spec: &K8sClusterSpec{}}},
	})
	if err != nil {
		t.Fatalf("GroupServices: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group (only the K8sCluster service), got %d", len(groups))
	}
	if len(groups[0].Services) != 1 || groups[0].Services[0].Name != "deployable" {
		t.Errorf("group should contain only 'deployable', got %+v", groups[0].Services)
	}
}

// TestGroupServices_ComposeByFile: compose services grouped by compose file.
func TestGroupServices_ComposeByFile(t *testing.T) {
	groups, err := GroupServices("prod", []RawService{
		{Name: "a", Compose: &ComposeSpec{ComposeFile: "docker-compose.yml"}},
		{Name: "b", Compose: &ComposeSpec{ComposeFile: "docker-compose.yml"}},
		{Name: "c", Compose: &ComposeSpec{ComposeFile: "prod.yml"}},
	})
	if err != nil {
		t.Fatalf("GroupServices: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (split by compose_file), got %d", len(groups))
	}
}

// TestRegistry_DefaultProviders confirms the canonical Registry comes
// pre-populated with the providers forge ships in this release.
func TestRegistry_DefaultProviders(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"k8s-cluster", "compose", "host-infra", "hosted", "firebase", "static-site"} {
		if r.Lookup(id) == nil {
			t.Errorf("Registry missing provider %q", id)
		}
	}
}

// TestFormatGroupSummary confirms the human-readable summary lists the
// provider id, target, and per-service names — what the deploy loop
// prints between groups.
func TestFormatGroupSummary(t *testing.T) {
	g := ServiceGroup{
		ProviderID: "k8s-cluster",
		Cluster:    "prod",
		Namespace:  "ns-prod",
		Services: []ResolvedService{
			{Name: "a"}, {Name: "b"},
		},
	}
	s := FormatGroupSummary(g)
	for _, want := range []string{"k8s-cluster", "prod", "ns-prod", "a", "b"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary should contain %q, got %q", want, s)
		}
	}
}
