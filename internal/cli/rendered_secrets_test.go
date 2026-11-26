package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// TestDeclaredSecret maps the entity shape to the secrets-package
// DeclaredSecret, preserving per-key sources.
func TestDeclaredSecret(t *testing.T) {
	got := declaredSecret(RenderedSecretEntity{
		Name: "db-credentials",
		Keys: map[string]RenderedSecretKeyEntity{
			"password": {From: "file", Key: "DB_PASSWORD"},
			"issuer":   {From: "literal", Value: "https://test.local/"},
		},
	})
	if got.Name != "db-credentials" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Keys["password"].Key != "DB_PASSWORD" || got.Keys["password"].From != "file" {
		t.Errorf("password key wrong: %+v", got.Keys["password"])
	}
	if got.Keys["issuer"].Value != "https://test.local/" || got.Keys["issuer"].From != "literal" {
		t.Errorf("issuer key wrong: %+v", got.Keys["issuer"])
	}

	// A non-rendered provider declares nothing of its own.
	if got := declaredSecretEntities(&KCLEntities{SecretProvider: &SecretProviderEntity{Type: "file"}}); len(got) != 0 {
		t.Errorf("a file provider should declare no rendered Secrets, got %+v", got)
	}
}

// TestReferencedSecretNamesForGroup is the trust-boundary scoping: a
// declared Secret lands in a cluster ONLY when one of that cluster's
// services references it. A Secret referenced only by a service in
// ANOTHER group must NOT appear.
func TestReferencedSecretNamesForGroup(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			clusterWL("cp-api", "k3d-cp", "cp", withSecretRef("DB_PASSWORD", "cp-db", "password")),
			clusterWL("workload-api", "k3d-workload", "wl", withSecretRef("TOKEN", "workload-token", "token")),
		},
	}

	cpGroup := deploytarget.ServiceGroup{
		ProviderID: "k8s-cluster",
		Cluster:    "k3d-cp",
		Services:   []deploytarget.ResolvedService{{Name: "cp-api"}},
	}
	workloadGroup := deploytarget.ServiceGroup{
		ProviderID: "k8s-cluster",
		Cluster:    "k3d-workload",
		Services:   []deploytarget.ResolvedService{{Name: "workload-api"}},
	}

	cpRefs := referencedSecretNamesForGroup(entities, cpGroup)
	if _, ok := cpRefs["cp-db"]; !ok {
		t.Error("cp group must reference cp-db")
	}
	if _, ok := cpRefs["workload-token"]; ok {
		t.Error("cp group must NOT reference workload-token (cross-boundary leak)")
	}

	wRefs := referencedSecretNamesForGroup(entities, workloadGroup)
	if _, ok := wRefs["workload-token"]; !ok {
		t.Error("workload group must reference workload-token")
	}
	if _, ok := wRefs["cp-db"]; ok {
		t.Error("workload group must NOT reference cp-db (cross-boundary leak)")
	}
}
