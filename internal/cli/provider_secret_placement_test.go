package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// A FileSecrets provider projects every Secret a CLUSTER workload declares by
// secret_ref. It must land in EVERY cluster whose workloads declare it, not
// only the env's primary one.
//
// Incident: control-plane's dev env runs workspace-controller in one k3d
// cluster and workspace-proxy in a second, and both read
// control-plane-secrets. forge applied the projected Secret into the primary
// cluster only, so the proxy's pod had nothing to start against. The project
// worked around it by declaring the Secret a forge.ExternalSecret (which then
// blocked every fresh worktree's deploy preflight) and creating + mirroring it
// with a shell script — whose random HMACs then disagreed with the values
// forge projected into the first cluster, so session cookies minted by one
// side never validated on the other.

// twoClusterFileSecretsEntities is that shape: one FileSecrets store, one
// Secret, two consuming workloads on two clusters (in the same namespace).
func twoClusterFileSecretsEntities() (*KCLEntities, []deploytarget.ServiceGroup) {
	ref := func(env, key string) deployv1alpha1.EnvVar {
		return deployv1alpha1.EnvVar{Name: env, SecretRef: &deployv1alpha1.SecretKeyRef{Name: "app-secrets", Key: key}}
	}
	entities := &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "file", Path: "secrets/dev.yaml"},
		Workloads: []WorkloadEntity{
			{Name: "controller", Runtime: RuntimeEntity{Type: RuntimeCluster}, Spec: deployv1alpha1.WorkloadSpec{
				Env: []deployv1alpha1.EnvVar{ref("INTERNAL_SERVICE_SECRET", "internal_service_secret")}}},
			{Name: "proxy", Runtime: RuntimeEntity{Type: RuntimeCluster}, Spec: deployv1alpha1.WorkloadSpec{
				Env: []deployv1alpha1.EnvVar{
					ref("INTERNAL_SERVICE_SECRET", "internal_service_secret"),
					ref("PROXY_SESSION_SECRET", "proxy_session_secret"),
				}}},
		},
	}
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: "k3d-hub", Namespace: "app-dev-wt", Services: []deploytarget.ResolvedService{{Name: "controller"}}},
		{ProviderID: "k8s-cluster", Cluster: "k3d-edge", Namespace: "app-dev-wt", Services: []deploytarget.ResolvedService{{Name: "proxy"}}},
	}
	return entities, groups
}

// writeDevSecretStore makes dir a forge project whose secrets/dev.yaml holds
// the two values the fixture declares, and chdirs into it (the provider
// resolves the store against the project root).
func writeDevSecretStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "secrets", "dev.yaml")
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("INTERNAL_SERVICE_SECRET: shared-hmac-0123\nPROXY_SESSION_SECRET: session-hmac-4567\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestApplyK8sSecretsFromProvider_ProjectsIntoEveryConsumingCluster is the
// regression test. The dry-run prints exactly what a real run applies, per
// destination.
//
// Mutation that fails it: apply the projection once, to the env-wide
// kubeContext, as forge did before (the edge cluster then receives nothing).
func TestApplyK8sSecretsFromProvider_ProjectsIntoEveryConsumingCluster(t *testing.T) {
	writeDevSecretStore(t)
	entities, groups := twoClusterFileSecretsEntities()

	// A collecting sink: the dry-run no longer prints Secret values, so what
	// each destination would receive is read where it is handed off.
	var placed []secretPlacement
	collect := secretTarget{sink: func(_ context.Context, p secretPlacement) error {
		placed = append(placed, p)
		return nil
	}}
	if err := applyK8sSecretsFromProviderTo(context.Background(), entities, groups, "app-dev-wt", "k3d-hub", "dev", false, collect); err != nil {
		t.Fatalf("projection: %v", err)
	}

	clusters := map[string]bool{}
	var secrets, copies int
	for _, p := range placed {
		clusters[p.cluster] = true
		secrets += strings.Count(p.stream, "kind: Secret")
		copies += strings.Count(p.stream, "shared-hmac-0123")
		if p.namespace != "app-dev-wt" {
			t.Errorf("placed into namespace %q, want app-dev-wt", p.namespace)
		}
	}
	for _, cluster := range []string{"k3d-hub", "k3d-edge"} {
		if !clusters[cluster] {
			t.Errorf("the projected Secret must be applied into %s (a workload there declares it); placed: %+v", cluster, placed)
		}
	}
	if secrets != 2 {
		t.Errorf("want one app-secrets manifest per consuming cluster (2), got %d", secrets)
	}
	// Both copies carry the SAME store values — the property the shell-script
	// mirror broke.
	if copies != 2 {
		t.Errorf("internal_service_secret must be identical in both clusters (want 2 copies), got %d", copies)
	}
}

// A dry run shows a Secret's shape and never its content.
func TestApplyK8sSecretsFromProvider_DryRunPrintsNoValue(t *testing.T) {
	writeDevSecretStore(t)
	entities, groups := twoClusterFileSecretsEntities()
	out := captureStdout(t, func() {
		if err := applyK8sSecretsFromProvider(context.Background(), entities, groups, "app-dev-wt", "k3d-hub", "dev", true); err != nil {
			t.Fatalf("dry-run projection: %v", err)
		}
	})
	if strings.Contains(out, "shared-hmac-0123") || strings.Contains(out, "session-hmac-4567") {
		t.Errorf("a dry run printed a Secret value:\n%s", out)
	}
	if !strings.Contains(out, "kind: Secret") {
		t.Errorf("a dry run should still show the Secret's shape:\n%s", out)
	}
}

// TestPlaceProviderSecretRefs_ScopesKeysToTheirConsumers: a cluster receives
// the keys ITS workloads declare and no others — the projection follows the
// trust boundary the RenderedSecrets provider already keeps (a value never
// lands in a cluster none of its consumers run in).
func TestPlaceProviderSecretRefs_ScopesKeysToTheirConsumers(t *testing.T) {
	entities, groups := twoClusterFileSecretsEntities()
	got := map[string][]string{}
	for _, p := range placeProviderSecretRefs(entities, groups, "fallback-ns", "fallback-ctx") {
		var keys []string
		for _, r := range p.refs {
			keys = append(keys, r.SecretKey)
		}
		got[p.cluster+"/"+p.namespace] = keys
	}
	want := map[string]string{
		"k3d-hub/app-dev-wt":  "internal_service_secret",
		"k3d-edge/app-dev-wt": "internal_service_secret,proxy_session_secret",
	}
	if len(got) != len(want) {
		t.Fatalf("placements = %v, want %v", got, want)
	}
	for place, keys := range want {
		if strings.Join(got[place], ",") != keys {
			t.Errorf("%s: keys %v, want %s", place, got[place], keys)
		}
	}
}

// TestPlaceProviderSecretRefs_UngroupedWorkloadUsesTheEnvTarget: a cluster
// workload no group claims (nothing routes it elsewhere) keeps the historical
// destination — the env's resolved context and namespace — so a single-cluster
// env is unchanged.
func TestPlaceProviderSecretRefs_UngroupedWorkloadUsesTheEnvTarget(t *testing.T) {
	entities, _ := twoClusterFileSecretsEntities()
	placed := placeProviderSecretRefs(entities, nil, "env-ns", "k3d-primary")
	if len(placed) != 1 || placed[0].cluster != "k3d-primary" || placed[0].namespace != "env-ns" {
		t.Fatalf("want every ref at k3d-primary/env-ns, got %+v", placed)
	}
}

// TestApplyK8sSecretsFromProvider_RefusesAPlacementWithNoCluster: a projection
// with no cluster to land in is an error, never an apply to whatever kubectl
// context happens to be current.
func TestApplyK8sSecretsFromProvider_RefusesAPlacementWithNoCluster(t *testing.T) {
	writeDevSecretStore(t)
	entities, _ := twoClusterFileSecretsEntities()
	err := applyK8sSecretsFromProvider(context.Background(), entities, nil, "env-ns", "", "dev", false)
	if err == nil || !strings.Contains(err.Error(), "no kubectl context") {
		t.Fatalf("want a no-context refusal, got %v", err)
	}
}
