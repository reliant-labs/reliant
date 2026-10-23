package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// errKubectlExit1 is what execRunner returns for a failed kubectl: a
// plain exit-status error, with the reason only in the captured output.
// That split is the whole reason isKubectlNotFound reads the output
// rather than the error.
var errKubectlExit1 = errors.New("exit status 1")

// Owned-claim observation for a cluster workload with storage.
//
// A Cluster-bound workload with storageGiB is the one case where forge EMITS
// a PersistentVolumeClaim (pkg/deploy.RenderWorkloads, named by PVCName)
// rather than referencing one someone else provisioned. A claim that never
// binds shows up on the Deployment only as "0/1 ready" — true, and it names
// the symptom while the cause sits one object away. So the dispatch records
// the claim on the group (K8sClusterSpec.OwnedClaims) and the one k8s
// observer reads it back. The Deployment itself is observed like any other.
//
// The Service object is deliberately not observed. A Service is a stable
// name and a selector — it has no status worth reading, and reporting on it
// would add a row that is always green and never informative.

// storageWorkloadGroup is a cluster workload as the deploy dispatch builds
// it: a k8s-cluster group, one replica (storage requires it), plus the claim
// forge emits when storageGiB is declared.
func storageWorkloadGroup(storage bool) ServiceGroup {
	spec := &K8sClusterSpec{Replicas: 1, Ports: []int{8080}}
	if storage {
		spec.OwnedClaims = []string{"api-data"}
	}
	return ServiceGroup{
		Env:       "prod",
		Cluster:   "gke_example_prod",
		Namespace: "acme-prod",
		Services:  []ResolvedService{{Name: "api", K8sCluster: spec}},
	}
}

// pvcJSON is the kubectl `-o json` shape for a PersistentVolumeClaim.
func pvcJSON(t *testing.T, phase string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"status":     map[string]any{"phase": phase},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestOwnedClaimObserve_DeploymentIsAlreadyCovered is the EVIDENCE for
// "no new code was needed for the Deployment half".
//
// A workload with no storage is, to the observer, an ordinary
// cluster service — and it must observe exactly as one. If this ever
// needed provider changes to pass, the projection would have stopped
// being a true projection.
func TestOwnedClaimObserve_DeploymentIsAlreadyCovered(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl": deploymentJSON(t,
			"ghcr.io/acme/api@sha256:"+strings.Repeat("a", 64), 1, 1, 1, 1),
	}}
	obs, err := K8sClusterProvider{Runner: runner}.Observe(
		context.Background(), storageWorkloadGroup(false))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(obs.Items))
	}
	item := obs.Items[0]
	if item.Health != HealthHealthy {
		t.Errorf("health = %v (detail %q), want healthy", item.Health, item.Detail)
	}
	if item.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Errorf("digest = %q, want the pinned image's digest", item.Digest)
	}
	if item.Replicas == nil || item.Replicas.Desired != 1 || item.Replicas.Ready != 1 {
		t.Errorf("replicas = %+v, want desired=1 ready=1", item.Replicas)
	}

	// And it read the Deployment, by name, in the workload's own
	// namespace — not some other object or some other namespace.
	if len(runner.calls) != 1 {
		t.Fatalf("made %d kubectl calls, want exactly 1 (a storage-less workload owns no PVC): %v",
			len(runner.calls), runner.calls)
	}
	if !strings.Contains(runner.calls[0], "get deployment api -n acme-prod") {
		t.Errorf("kubectl call = %q, want a Deployment read for api in acme-prod", runner.calls[0])
	}
}

// TestOwnedClaimObserve_UnboundClaimIsDegradedNotHealthy is the GAP
// this work closed.
//
// The Deployment here is perfectly healthy on its own numbers. Without
// the claim read, this observation reports green for a workload whose
// storage never bound.
func TestOwnedClaimObserve_UnboundClaimIsDegradedNotHealthy(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl --context gke_example_prod get deployment": deploymentJSON(t, "img:v1", 1, 1, 1, 1),
		"kubectl --context gke_example_prod get pvc":        pvcJSON(t, "Pending"),
	}}
	obs, err := K8sClusterProvider{Runner: runner}.Observe(
		context.Background(), storageWorkloadGroup(true))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	item := obs.Items[0]
	if item.Health != HealthDegraded {
		t.Fatalf("health = %v, want degraded — the Deployment's own counts read 1/1 ready, so a "+
			"provider that did not read the PVC reports this workload as healthy", item.Health)
	}
	if !strings.Contains(item.Detail, "api-data") || !strings.Contains(item.Detail, "Pending") {
		t.Errorf("detail = %q, want it to name the claim and its phase", item.Detail)
	}
	// The measured Deployment facts survive the downgrade — a caller
	// needs both halves.
	if item.Replicas == nil || item.Replicas.Ready != 1 {
		t.Errorf("replicas = %+v, want the Deployment's measured counts to survive", item.Replicas)
	}
}

// TestOwnedClaimObserve_BoundClaimStaysHealthy is the control on the
// control: the claim check must not downgrade a workload that is fine, or
// it would report every storage workload as degraded forever and the test
// above would pass for the wrong reason.
func TestOwnedClaimObserve_BoundClaimStaysHealthy(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl --context gke_example_prod get deployment": deploymentJSON(t, "img:v1", 1, 1, 1, 1),
		"kubectl --context gke_example_prod get pvc":        pvcJSON(t, "Bound"),
	}}
	obs, _ := K8sClusterProvider{Runner: runner}.Observe(
		context.Background(), storageWorkloadGroup(true))
	item := obs.Items[0]
	if item.Health != HealthHealthy {
		t.Errorf("health = %v (detail %q), want healthy — a bound claim must not downgrade",
			item.Health, item.Detail)
	}
	if item.Detail != "" {
		t.Errorf("detail = %q, want empty; a bound claim is the expected state and saying so "+
			"every time buries the findings that matter", item.Detail)
	}
	// Two reads: the Deployment and the claim forge owns.
	if len(runner.calls) != 2 {
		t.Fatalf("made %d kubectl calls, want 2 (deployment + pvc): %v", len(runner.calls), runner.calls)
	}
	if !strings.Contains(runner.calls[1], "get pvc api-data -n acme-prod") {
		t.Errorf("second call = %q, want a PVC read for api-data in acme-prod", runner.calls[1])
	}
}

// TestOwnedClaimObserve_DeletedClaimIsReported covers the claim that
// is not there at all. forge emitted it, so its absence is a measurement
// — someone deleted it, or the apply never landed — and not a failure to
// look.
func TestOwnedClaimObserve_DeletedClaimIsReported(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			"kubectl --context gke_example_prod get deployment": deploymentJSON(t, "img:v1", 1, 1, 1, 1),
			"kubectl --context gke_example_prod get pvc": `Error from server (NotFound): ` +
				`persistentvolumeclaims "api-data" not found`,
		},
		runErrs: map[string]error{
			"kubectl --context gke_example_prod get pvc": errKubectlExit1,
		},
	}
	obs, _ := K8sClusterProvider{Runner: runner}.Observe(
		context.Background(), storageWorkloadGroup(true))
	item := obs.Items[0]
	if item.Health != HealthDegraded {
		t.Errorf("health = %v, want degraded", item.Health)
	}
	if !strings.Contains(item.Detail, "Absent") {
		t.Errorf("detail = %q, want it to report the claim as Absent", item.Detail)
	}
}

// TestOwnedClaimObserve_UnreadableClaimIsUnknownNotDegraded keeps the
// same distinction the Deployment read already makes: "I looked and the
// claim is wrong" and "I could not look" are different answers, and only
// the first is a measurement. Collapsing them would make an unreachable
// cluster indistinguishable from a broken PVC.
func TestOwnedClaimObserve_UnreadableClaimIsUnknownNotDegraded(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			"kubectl --context gke_example_prod get deployment": deploymentJSON(t, "img:v1", 1, 1, 1, 1),
			"kubectl --context gke_example_prod get pvc":        "Unable to connect to the server: dial tcp: i/o timeout",
		},
		runErrs: map[string]error{
			"kubectl --context gke_example_prod get pvc": errKubectlExit1,
		},
	}
	obs, _ := K8sClusterProvider{Runner: runner}.Observe(
		context.Background(), storageWorkloadGroup(true))
	item := obs.Items[0]
	if item.Health != HealthUnknown {
		t.Errorf("health = %v, want unknown — a claim forge emitted but could not read is not a "+
			"measurement, and must not be reported as one", item.Health)
	}
}

// TestOrdinaryClusterServiceReadsNoClaims pins the boundary from the
// other side: adding claim observation must not make forge start
// reporting on PVCs it merely REFERENCES. A forge.Volume of type "pvc"
// names a claim someone else provisioned, and forge has no standing to
// grade it — nor any way to tell an intentionally-absent one from a
// broken one.
func TestOrdinaryClusterServiceReadsNoClaims(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"kubectl": deploymentJSON(t, "img:v1", 2, 2, 2, 2),
	}}
	group := ServiceGroup{
		Env: "prod", Cluster: "c", Namespace: "ns",
		Services: []ResolvedService{{Name: "api", K8sCluster: &K8sClusterSpec{Replicas: 2}}},
	}
	if _, err := (K8sClusterProvider{Runner: runner}).Observe(context.Background(), group); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "get pvc") {
			t.Errorf("read a PVC for a service that owns none: %q", call)
		}
	}
}
