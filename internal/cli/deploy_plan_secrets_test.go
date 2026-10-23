package cli

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeSecretKeys is a cluster as a presence check sees it: KEY NAMES per
// (context, namespace, name). It cannot hold a value — secretKeyGetter has no
// way to ask for one — which is the property under test.
type fakeSecretKeys struct {
	mu      sync.Mutex
	secrets map[secretLocation]map[string][]string // location -> name -> keys
	broken  map[string]bool                        // context -> every read fails
	reads   []string
}

func (f *fakeSecretKeys) GetSecretKeys(_ context.Context, kctx, namespace, name string) (map[string]struct{}, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, kctx+"/"+namespace+"/"+name)
	if f.broken[kctx] {
		return nil, false, errors.New("Unable to connect to the server")
	}
	keys, ok := f.secrets[secretLocation{Context: kctx, Namespace: namespace}][name]
	if !ok {
		return nil, false, nil
	}
	out := map[string]struct{}{}
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out, true, nil
}

func clusterWorkload(name, kctx, ns string, refs ...deployv1alpha1.SecretKeyRef) WorkloadEntity {
	w := WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: kctx, Namespace: ns}}}
	for i := range refs {
		w.Spec.Env = append(w.Spec.Env, deployv1alpha1.EnvVar{Name: refs[i].Key, SecretRef: &refs[i]})
	}
	return w
}

// presenceFixture is a two-cluster env: an api in prod reading db-credentials,
// a scheduler in the daemon cluster reading sched-token, and a declared
// prerequisite (cloudflare-api-token) that no workload reads.
func presenceFixture() (*KCLEntities, release.Shape) {
	e := &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Cluster: "prod", Namespace: "cp-prod"},
		Workloads: []WorkloadEntity{
			clusterWorkload("api", "", "", deployv1alpha1.SecretKeyRef{Name: "db-credentials", Key: "password"},
				deployv1alpha1.SecretKeyRef{Name: "db-credentials", Key: "replica", Optional: true}),
			clusterWorkload("scheduler", "daemon", "cnpg-system", deployv1alpha1.SecretKeyRef{Name: "sched-token", Key: "token"}),
		},
		RequiredSecrets: []ExternalSecretEntity{{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}}},
	}
	shape := release.Shape{Kind: release.EnvSelfManaged, Secrets: []release.ShapeSecret{
		{Name: "cloudflare-api-token", Provider: "external"},
		{Name: "db-credentials", Provider: "external", DeclaredBy: []string{"api"}},
		{Name: "sched-token", Provider: "external", DeclaredBy: []string{"scheduler"}},
	}}
	return e, shape
}

func TestClusterSecretPresence_ReadsKeysWhereEachSecretIsRead(t *testing.T) {
	e, shape := presenceFixture()
	cluster := &fakeSecretKeys{secrets: map[secretLocation]map[string][]string{
		{Context: "prod", Namespace: "cp-prod"}:          {"db-credentials": {"password"}},
		{Context: "daemon", Namespace: "cnpg-system"}:    {"sched-token": {"other"}}, // the key it reads is gone
		{Context: "daemon", Namespace: "cert-manager"}:   {"cloudflare-api-token": {"api-token"}},
		{Context: "prod", Namespace: "elsewhere"}:        {"sched-token": {"token"}}, // wrong place: does not count
		{Context: "prod", Namespace: "cert-manager-old"}: {"cloudflare-api-token": {"api-token"}},
	}}
	got := clusterSecretPresence(context.Background(), externalSecretChecks(e, shape), cluster)
	want := map[string]bool{
		"db-credentials": true, // an optional key's absence asserts nothing
		"sched-token":    false,
		// Unattributed prerequisite: in ANY of the env's clusters.
		"cloudflare-api-token": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("presence = %v, want %v (reads %v)", got, want, cluster.reads)
	}
	sort.Strings(cluster.reads)
	for i := 1; i < len(cluster.reads); i++ {
		if cluster.reads[i] == cluster.reads[i-1] {
			t.Errorf("read %s twice; each (context, namespace, name) is read once", cluster.reads[i])
		}
	}
}

// "Could not look" is never "missing": a cluster that does not answer makes
// its secrets UNVERIFIABLE (absent from the map), and BuildPlan keeps them
// silent. Only a definite absence becomes false.
func TestClusterSecretPresence_AnUnreadableClusterIsUnverifiable(t *testing.T) {
	e, shape := presenceFixture()
	cluster := &fakeSecretKeys{
		secrets: map[secretLocation]map[string][]string{{Context: "prod", Namespace: "cp-prod"}: {}},
		broken:  map[string]bool{"daemon": true},
	}
	got := clusterSecretPresence(context.Background(), externalSecretChecks(e, shape), cluster)
	if _, known := got["sched-token"]; known {
		t.Errorf("sched-token's cluster could not be read, so its presence must be unverifiable, got %v", got)
	}
	if set, known := got["db-credentials"]; !known || set {
		t.Errorf("db-credentials is definitely absent from prod: got known=%v set=%v", known, set)
	}
	// The prerequisite is absent from prod and its other candidate, the
	// daemon cluster, could not be read: not provably missing.
	if _, known := got["cloudflare-api-token"]; known {
		t.Errorf("cloudflare-api-token: one candidate absent, one unreadable — unverifiable, got %v", got)
	}
}

// Only `external` secrets are the cluster's to answer for: every other
// provider's Secret is written by the deploy itself, so its absence before
// the deploy is not a finding.
func TestExternalSecretChecks_OnlyExternalSecrets(t *testing.T) {
	e, shape := presenceFixture()
	for i := range shape.Secrets {
		shape.Secrets[i].Provider = "hosted"
	}
	if checks := externalSecretChecks(e, shape); len(checks) != 0 {
		t.Fatalf("checks = %+v, want none for secrets forge writes itself", checks)
	}
}
