package deploytarget

import "testing"

// TestDeclaredContext_IsTheOnlySource confirms the K8sCluster provider
// derives its kubectl context SOLELY from the group's declared cluster (KCL
// forge.K8sCluster.cluster), with no CLI override and no fall-back to the
// active context.
func TestDeclaredContext_IsTheOnlySource(t *testing.T) {
	p := K8sClusterProvider{}
	g := ServiceGroup{
		ProviderID: "k8s-cluster",
		Cluster:    "gke_reliant-labs-475814_us-central1_prod",
		Namespace:  "cp-forge-prod",
	}
	if got := p.declaredContext(g); got != g.Cluster {
		t.Errorf("context should be the declared cluster: want %q, got %q", g.Cluster, got)
	}
}

// TestDeclaredContext_NoClusterEmpty confirms a group with no declared
// cluster yields an empty context — never a silent fall-back to the active
// one. Observe reports unknown on it (TestK8sObserve_*).
func TestDeclaredContext_NoClusterEmpty(t *testing.T) {
	p := K8sClusterProvider{}
	g := ServiceGroup{ProviderID: "k8s-cluster", Namespace: "ns"}
	if got := p.declaredContext(g); got != "" {
		t.Errorf("no declared cluster should yield empty context, got %q", got)
	}
}
