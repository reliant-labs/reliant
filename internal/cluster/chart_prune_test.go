package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The chart prune, proven against a stateful fake cluster (fake_cluster_test.go).
//
// Each test applies a chart as release N, then as release N+1 with the render
// changed, and reads what is LIVE afterwards. That is the claim the prune
// makes, and an argv recorder cannot check it.

const chartOwnerFixture = "acme.prod"

// fakeHelmReleases installs a `helm` that prints whatever stream was last passed
// to the returned setter, so one test can render release N and then release N+1
// through the real RenderHelmChart.
func fakeHelmReleases(t *testing.T) (setRelease func(stream string)) {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	body := filepath.Join(dir, "chart.yaml")
	script := "#!/bin/sh\ncat " + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(stream string) {
		t.Helper()
		if err := os.WriteFile(body, []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fluxLikeSpec() HelmChartSpec {
	return HelmChartSpec{
		Name: "flux", OCI: "oci://example/flux", Version: "1", Namespace: "flux-system",
		Owner: chartOwnerFixture, Prune: true,
	}
}

// renderRelease renders spec through the real RenderHelmChart and returns it as
// the renderedChart the apply loop takes.
func renderRelease(t *testing.T, spec HelmChartSpec) renderedChart {
	t.Helper()
	out, err := RenderHelmChart(context.Background(), spec)
	if err != nil {
		t.Fatalf("RenderHelmChart: %v", err)
	}
	return renderedChart{spec: spec, manifests: out}
}

func applyRelease(t *testing.T, spec HelmChartSpec) {
	t.Helper()
	rc := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
}

const (
	docDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
spec: {}`
	docWebhookPolicy = `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-webhooks
spec:
  podSelector: {}`
	docImpersonator = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-reconciler-impersonator
rules: []`
	docImpersonatorBinding = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-reconciler-impersonator
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-reconciler-impersonator}`
)

func stream(docs ...string) string { return strings.Join(docs, "\n---\n") }

// An object present in release N and absent from N+1 is gone afterwards; what
// release N+1 still renders is untouched. The three removed objects are the
// ones the security cleanup had to delete by hand: a NetworkPolicy, a
// ClusterRole and its ClusterRoleBinding. The Deployment names no namespace,
// as flux2's 40 objects do, so it also proves a namespace-less object is read
// as living in the chart's namespace and is not mistaken for a departed one.
func TestChartPrune_DeletesObjectsThatLeftTheRender(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	setRelease(stream(docDeployment, docWebhookPolicy, docImpersonator, docImpersonatorBinding))
	applyRelease(t, spec)
	for _, o := range [][3]string{
		{"NetworkPolicy", "flux-system", "allow-webhooks"},
		{"ClusterRole", "", "cluster-reconciler-impersonator"},
		{"ClusterRoleBinding", "", "cluster-reconciler-impersonator"},
		{"Deployment", "flux-system", "source-controller"},
	} {
		if !cl.has(o[0], o[1], o[2]) {
			t.Fatalf("release N should have applied %v", o)
		}
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Fatalf("release N deleted %v; the first apply has nothing to prune", got)
	}

	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	for _, o := range [][3]string{
		{"NetworkPolicy", "flux-system", "allow-webhooks"},
		{"ClusterRole", "", "cluster-reconciler-impersonator"},
		{"ClusterRoleBinding", "", "cluster-reconciler-impersonator"},
	} {
		if cl.has(o[0], o[1], o[2]) {
			t.Errorf("%s %s/%s left the render and must be pruned, but is still live", o[0], o[1], o[2])
		}
	}
	if !cl.has("Deployment", "flux-system", "source-controller") {
		t.Error("the Deployment is still rendered and must survive the prune")
	}
	if !cl.has("Namespace", "", "flux-system") {
		t.Error("the chart's Namespace is still rendered and must survive the prune")
	}
}

// A second apply of the SAME render prunes nothing: the prune is a diff, not a
// sweep.
func TestChartPrune_IdempotentOnAnUnchangedRender(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	setRelease(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)
	applyRelease(t, spec)
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("re-applying an unchanged render deleted %v", got)
	}
}

// `kustomize.toolkit.fluxcd.io/prune: disabled` is the escape hatch, and it is
// Flux's own, so one annotation protects an object on both delivery paths. It
// is set on the live object, which is how an operator marks one that must
// outlive the chart. The render does not need to carry it.
func TestChartPrune_PruneDisabledObjectSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	setRelease(stream(docDeployment, docWebhookPolicy, docImpersonator))
	applyRelease(t, spec)
	// An operator pins the NetworkPolicy on the live object.
	state := cl.state()
	for _, o := range state.Objects {
		if objKey(o) == fakeKey("NetworkPolicy", "flux-system", "allow-webhooks") {
			md := strMap(o["metadata"])
			md["annotations"] = map[string]any{PruneAnnotation: PruneDisabled}
		}
	}
	saveFakeState(cl.path, state)

	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Error("a prune: disabled object was deleted")
	}
	if cl.has("ClusterRole", "", "cluster-reconciler-impersonator") {
		t.Error("the unannotated ClusterRole should still have been pruned")
	}
}

// An object a controller owns is the controller's to garbage-collect.
func TestChartPrune_OwnedObjectIsLeftToItsOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	setRelease(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)
	state := cl.state()
	for _, o := range state.Objects {
		if objKey(o) == fakeKey("NetworkPolicy", "flux-system", "allow-webhooks") {
			strMap(o["metadata"])["ownerReferences"] = []any{map[string]any{"kind": "Deployment", "name": "x"}}
		}
	}
	saveFakeState(cl.path, state)

	setRelease(stream(docDeployment))
	applyRelease(t, spec)
	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Error("an object with ownerReferences was deleted by the prune")
	}
}

// Kinds that hold data, or own everything under them, are reported and kept:
// PVC, StatefulSet, Secret, and a CRD (which is never deleted implicitly).
// The Namespace is covered separately, because forge itself renders it.
func TestChartPrune_StatefulKindsAreKept(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	docs := map[string]string{
		"PersistentVolumeClaim": "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data\nspec: {}",
		"StatefulSet":           "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: db\nspec: {}",
		"Secret":                "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\nstringData: {a: b}",
		"CustomResourceDefinition": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n" +
			"metadata:\n  name: widgets.acme.dev\nspec: {group: acme.dev}",
	}
	setRelease(stream(docDeployment, docs["PersistentVolumeClaim"], docs["StatefulSet"], docs["Secret"], docs["CustomResourceDefinition"]))
	applyRelease(t, spec)

	// Release N+1 drops all four, and a plain object beside them.
	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	for _, o := range [][3]string{
		{"PersistentVolumeClaim", "flux-system", "data"},
		{"StatefulSet", "flux-system", "db"},
		{"Secret", "flux-system", "creds"},
		{"CustomResourceDefinition", "", "widgets.acme.dev"},
	} {
		if !cl.has(o[0], o[1], o[2]) {
			t.Errorf("%s %s was deleted by the chart prune; stateful kinds are reported and kept", o[0], o[2])
		}
	}
	for _, d := range cl.deletes() {
		for _, kind := range []string{"persistentvolumeclaim", "statefulset", "secret", "customresourcedefinition", "namespace"} {
			if strings.HasPrefix(d, kind) {
				t.Errorf("the prune issued %q for a stateful kind", d)
			}
		}
	}
}

// The Namespace is never deleted. When a chart moves to another namespace the
// old Namespace is departed, and it owns everything inside it.
func TestChartPrune_NamespaceIsNeverDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	moved := spec
	moved.Namespace = "flux-system-2"
	applyRelease(t, moved)

	if !cl.has("Namespace", "", "flux-system") {
		t.Error("the old Namespace was deleted when the chart moved; a Namespace owns everything in it")
	}
	if cl.has("Deployment", "flux-system", "source-controller") {
		t.Error("the Deployment left in the old namespace is departed and should have been pruned individually")
	}
	if !cl.has("Deployment", "flux-system-2", "source-controller") {
		t.Error("the Deployment in the new namespace must exist")
	}
	for _, d := range cl.deletes() {
		if strings.HasPrefix(d, "namespace") {
			t.Errorf("the prune issued %q", d)
		}
	}
	// The inventory moved with the chart: the old one was found in the old
	// namespace (that is how the old Deployment was a candidate at all), folded
	// into the new one and deleted. The kept Namespace stays tracked.
	invs := cl.inventories()
	if len(invs) != 1 {
		t.Fatalf("want exactly one inventory after the move, got %v", invs)
	}
	want := []string{
		"Deployment/flux-system-2/source-controller",
		"Namespace/flux-system",
		"Namespace/flux-system-2",
	}
	if got := cl.inventory("flux-system-2"); !reflect.DeepEqual(got, want) {
		t.Errorf("inventory after the move = %v, want %v", got, want)
	}
}

// `prune = false` turns the step off for a chart whose live objects are
// hand-managed; an unset owner turns it off for a spec forge cannot attribute.
func TestChartPrune_DisabledAndOwnerlessChartsDeleteNothing(t *testing.T) {
	for name, mutate := range map[string]func(*HelmChartSpec){
		"prune = false": func(s *HelmChartSpec) { s.Prune = false },
		"no owner":      func(s *HelmChartSpec) { s.Owner = "" },
	} {
		t.Run(name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
			}
			cl := newFakeCluster(t)
			setRelease := fakeHelmReleases(t)
			spec := fluxLikeSpec()
			mutate(&spec)

			setRelease(stream(docDeployment, docWebhookPolicy))
			applyRelease(t, spec)
			setRelease(stream(docDeployment))
			applyRelease(t, spec)

			if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
				t.Errorf("%s: the departed object was deleted", name)
			}
			if got := cl.deletes(); len(got) != 0 {
				t.Errorf("%s: issued deletes %v", name, got)
			}
		})
	}
}

// Only an object an earlier apply of THIS chart recorded is ever a candidate.
// Another env's, another chart's, a hand-made object, one dropped before forge
// stamped an owner, and a CLONE carrying exactly this chart's stamp (a manifest
// copied with `kubectl get -o yaml`) all survive: none was in the inventory.
func TestChartPrune_OnlyTouchesWhatTheInventoryRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	mk := func(name string, labels map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": name, "namespace": "flux-system", "labels": labels},
		}
	}
	cl.seed(mk("other-env", map[string]any{ChartOwnerLabel: "acme.staging", WorkloadLabel: "flux"}))
	cl.seed(mk("other-chart", map[string]any{ChartOwnerLabel: chartOwnerFixture, WorkloadLabel: "cert-manager"}))
	cl.seed(mk("hand-made", map[string]any{"app": "x"}))
	cl.seed(mk("pre-stamp", map[string]any{WorkloadLabel: "flux"})) // dropped before forge stamped an owner
	cl.seed(mk("clone", map[string]any{ChartOwnerLabel: chartOwnerFixture, WorkloadLabel: "flux"}))

	applyRelease(t, spec)

	for _, name := range []string{"other-env", "other-chart", "hand-made", "pre-stamp", "clone"} {
		if !cl.has("NetworkPolicy", "flux-system", name) {
			t.Errorf("NetworkPolicy %s was never recorded in this chart's inventory and must be left alone", name)
		}
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
}

// The FIRST apply that finds no inventory (the first deploy under this version)
// deletes nothing. It does not even delete objects that carry this chart's
// stamp but that the render no longer has, which is exactly what an env stamped
// by an earlier forge holds. It records an inventory, and pruning starts from
// the next apply.
func TestChartPrune_FirstApplyWithoutInventoryDeletesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	// What an earlier forge left live: every object stamped, no inventory.
	setRelease(stream(docDeployment, docWebhookPolicy, docImpersonator))
	for _, doc := range splitDocs(renderRelease(t, spec).manifests) {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatal(err)
		}
		if md := strMap(o["metadata"]); md["namespace"] == nil && o["kind"] != "Namespace" && o["kind"] != "ClusterRole" {
			md["namespace"] = "flux-system"
		}
		cl.seed(o)
	}
	if len(cl.inventories()) != 0 {
		t.Fatal("fixture: the cluster must start with no inventory")
	}

	setRelease(stream(docDeployment))
	applyRelease(t, spec)

	if got := cl.deletes(); len(got) != 0 {
		t.Fatalf("the first apply without an inventory deleted %v", got)
	}
	for _, o := range [][3]string{{"NetworkPolicy", "flux-system", "allow-webhooks"}, {"ClusterRole", "", "cluster-reconciler-impersonator"}} {
		if !cl.has(o[0], o[1], o[2]) {
			t.Errorf("%v was deleted on the first apply without an inventory", o)
		}
	}
	if got, want := cl.inventory("flux-system"), []string{"Deployment/flux-system/source-controller", "Namespace/flux-system"}; !reflect.DeepEqual(got, want) {
		t.Errorf("recorded inventory = %v, want %v (what this apply sent)", got, want)
	}

	// The objects it found were never recorded, so the next apply leaves them too.
	applyRelease(t, spec)
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("an object no inventory recorded was deleted on a later apply: %v", got)
	}
}

// A failed apply proves nothing about the render's unconfirmed UIDs. It prunes
// nothing, preserves the prior inventory, and retains the lock until explicit
// recovery. A later successful retry can then reconcile the prior inventory.
func TestChartPrune_FailedApplyPrunesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	setRelease(stream(docDeployment, docWebhookPolicy, docImpersonator))
	applyRelease(t, spec)

	docExtra := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: introduced-then-failed\ndata: {}"
	setRelease(stream(docDeployment, docExtra))
	t.Setenv("FORGE_FAKE_FAIL_APPLY", "1")
	rc := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err == nil {
		t.Fatal("the apply was made to fail and reported success")
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("a failed apply pruned %v", got)
	}
	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Error("a failed apply deleted a departed object")
	}
	inv := strings.Join(cl.inventory("flux-system"), " ")
	if strings.Contains(inv, "introduced-then-failed") {
		t.Fatalf("failed apply recorded an unconfirmed UID: %s", inv)
	}
	if !strings.Contains(inv, "NetworkPolicy/flux-system/allow-webhooks") {
		t.Fatalf("failed apply forgot prior inventory: %s", inv)
	}

	// The apply recovers: everything that left the render goes, and an entry
	// for an object the failed apply never created is simply forgotten.
	t.Setenv("FORGE_FAKE_FAIL_APPLY", "")
	// The failed command retained its lock. In this fake all requests are
	// synchronous and finished, so explicitly attest the terminated holder.
	lock, err := getChartObject(context.Background(), "k3d-test", chartStateEntry(chartLockName(spec)))
	if err != nil || lock == nil {
		t.Fatalf("failed apply did not retain lock: %v", err)
	}
	holder := strMap(lock.raw["data"])["holder"].(string)
	if err := RecoverChartLock(context.Background(), "k3d-test", spec.Owner, spec.Name, lock.Metadata.UID, lock.Metadata.ResourceVersion, "terminated:"+holder); err != nil {
		t.Fatal(err)
	}
	setRelease(stream(docDeployment))
	applyRelease(t, spec)
	if cl.has("NetworkPolicy", "flux-system", "allow-webhooks") || cl.has("ClusterRole", "", "cluster-reconciler-impersonator") {
		t.Error("the successful apply after a failed one did not prune the departed objects")
	}
	if got, want := cl.inventory("flux-system"), []string{"Deployment/flux-system/source-controller", "Namespace/flux-system"}; !reflect.DeepEqual(got, want) {
		t.Errorf("inventory after recovery = %v, want %v", got, want)
	}
}

// Two applies of one chart at once. This apply (release N+1, which drops the
// NetworkPolicy) reads its inventory, and while it applies, a CONCURRENT
// applier lands a new ClusterRole carrying this chart's exact stamp. The new
// object was not in the inventory this apply read, so this apply must not
// delete it, though it is stamped and absent from this render. Then, between
// this apply's read and write of the inventory, the concurrent applier records
// its object in violation of the lock. The compare-and-swap must refuse to
// overwrite that foreign inventory write, and no deletion may follow.
func TestChartPrune_ConcurrentApplierIsNotPruned(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()

	setRelease(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)
	invKey := chartStateNamespace + "/" + chartInventoryName(spec.Name, spec.Owner)
	before := cl.state()

	concurrentRole := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "added-concurrently", "labels": map[string]any{
			ChartOwnerLabel: chartOwnerFixture, WorkloadLabel: "flux",
		}},
	}
	cl.injectOn("apply", concurrentRole)

	// The concurrent applier's inventory write: everything it read, plus its
	// new ClusterRole.
	var invObj map[string]any
	for _, o := range before.Objects {
		md := strMap(o["metadata"])
		if o["kind"] == "ConfigMap" && fmt.Sprintf("%v/%v", md["namespace"], md["name"]) == invKey {
			invObj = o
		}
	}
	if invObj == nil {
		t.Fatalf("release N recorded no inventory at %s", invKey)
	}
	theirs, err := decodeChartInventory(fmt.Sprint(strMap(invObj["data"])[chartInventoryDataKey]))
	if err != nil {
		t.Fatal(err)
	}
	theirs[inventoryEntry{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Namespace: "flux-system", Name: "added-concurrently"}] = "their-apply"
	data, _ := theirs.encode()
	concurrentInv := jsonRoundTrip(invObj)
	strMap(concurrentInv["data"])[chartInventoryDataKey] = data
	cl.injectOn("replace", concurrentInv)

	setRelease(stream(docDeployment))
	rc := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err == nil {
		t.Fatal("foreign inventory write must fail CAS, never overwrite or prune")
	}
	if !cl.has("ClusterRole", "", "added-concurrently") || !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Fatal("inventory conflict deleted an object")
	}
	if got := cl.deletes(); len(got) != 0 {
		t.Fatalf("pruned after inventory conflict: %v", got)
	}
}

// Unlike a newly introduced object, a retained object is already in both
// appliers' snapshots. A stale applier must not delete it after the newer
// applier successfully retained it. This interleaving needs no goroutines:
// pause A after its inventory read, let B finish, then resume A's prune.
func TestChartPrune_StaleApplierCannotDeleteWinnersRetainedObject(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	setRelease(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)

	// A intends to remove the policy and reads the old inventory, then pauses.
	setRelease(docDeployment)
	staleRender := renderRelease(t, spec)
	stale, err := beginChartPrune(context.Background(), "k3d-test", staleRender, "stale-A")
	if err != nil || stale == nil {
		t.Fatalf("fixture: A must hold its lock and inventory: %v", err)
	}

	// B cannot overlap A. A's lock cannot expire, even if A is suspended.
	setRelease(stream(docDeployment, docWebhookPolicy))
	winner := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{winner}, true); err == nil {
		t.Fatal("B overlapped A's non-expiring lock")
	}
	// A stops before explicit recovery, then B completes. Model a stale
	// callback resuming after recovery: it must reject its obsolete lock UID.
	if err := stale.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	applyRelease(t, spec)

	// A resumes. Its inventory merge must not just preserve B's entry after
	// deleting the very object that entry represents.
	if _, err := stale.afterApply(context.Background()); err != nil {
		t.Logf("stale applier refused: %v", err)
	}
	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Fatalf("stale A deleted the policy B retained; deletes=%v", cl.deletes())
	}
}

// A chart that drops a custom resource AND the cluster stops serving its kind
// (its CRD was removed): no object of the kind can exist, so the entry is
// forgotten rather than warned about on every deploy forever, and nothing is
// deleted.
func TestChartPrune_EntryOfAnUnservedKindIsForgotten(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	docWidget := "apiVersion: acme.dev/v1\nkind: Widget\nmetadata:\n  name: w\nspec: {}"
	setRelease(stream(docDeployment, docWidget))
	applyRelease(t, spec)

	t.Setenv("FORGE_FAKE_UNSERVED_KIND", "Widget")
	setRelease(stream(docDeployment))
	applyRelease(t, spec)
	if got := cl.deletes(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
	if got, want := cl.inventory("flux-system"), []string{"Deployment/flux-system/source-controller", "Namespace/flux-system"}; !reflect.DeepEqual(got, want) {
		t.Errorf("inventory = %v, want %v (the unserved kind's entry forgotten)", got, want)
	}
}

// An inventoried object whose stamp was taken off (someone adopted it) is left
// alone when the chart drops it, and is no longer tracked.
func TestChartPrune_DisownedObjectIsLeftAndForgotten(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	setRelease(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)

	state := cl.state()
	for _, o := range state.Objects {
		if objKey(o) == fakeKey("NetworkPolicy", "flux-system", "allow-webhooks") {
			delete(strMap(strMap(o["metadata"])["labels"]), ChartOwnerLabel)
		}
	}
	saveFakeState(cl.path, state)

	setRelease(stream(docDeployment))
	applyRelease(t, spec)
	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Error("an object that no longer carries this chart's stamp was deleted")
	}
	for _, e := range cl.inventory("flux-system") {
		if strings.Contains(e, "allow-webhooks") {
			t.Errorf("a disowned object is still tracked: %v", cl.inventory("flux-system"))
		}
	}
}

// A render that cannot be read in full is not a statement about what the chart
// declares, so nothing is pruned from it.
func TestDesiredChartObjects_UnreadableOrEmptyRenderIsNotUsable(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      "",
		"no objects": "# just a comment\n---\n",
		"malformed":  "apiVersion: v1\nkind: ConfigMap\nmetadata: [oops\n",
	} {
		if _, ok := desiredChartObjects(in, "flux-system"); ok {
			t.Errorf("%s: desiredChartObjects reported a usable render", name)
		}
	}
	if set, ok := desiredChartObjects(docDeployment, "flux-system"); !ok || !set.contains(chartObjectRef{"Deployment", "flux-system", "source-controller"}) {
		t.Errorf("a namespace-less Deployment must be indexed under the chart's namespace; ok=%v set=%+v", ok, set)
	}
	if set, _ := desiredChartObjects(docImpersonator, "flux-system"); !set.contains(chartObjectRef{"ClusterRole", "", "cluster-reconciler-impersonator"}) {
		t.Error("a cluster-scoped object must match by kind and name alone")
	}
}

// decideChartPrune's guards, one row each, without a cluster. Every live object
// here is a departed inventory entry; the verdict is what the prune does.
func TestDecideChartPrune_Guards(t *testing.T) {
	spec := fluxLikeSpec()
	stamped := func(kind, ns, name string, mod func(*liveChartObject)) liveChartObject {
		var o liveChartObject
		o.APIVersion, o.Kind, o.Metadata.Name, o.Metadata.Namespace = "v1", kind, name, ns
		o.Metadata.Labels = map[string]string{ChartOwnerLabel: spec.Owner, WorkloadLabel: spec.Name}
		if mod != nil {
			mod(&o)
		}
		return o
	}
	desired, _ := desiredChartObjects(stream(
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: current",
		docImpersonator,
	), "flux-system")

	for _, tc := range []struct {
		name string
		live liveChartObject
		want chartPruneVerdict
	}{
		{"departed and stamped", stamped("ConfigMap", "flux-system", "gone", nil), pruneDelete},
		{"another prune annotation value", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.Annotations = map[string]string{PruneAnnotation: "enabled"}
		}), pruneDelete},
		{"cluster-scoped, recorded under the chart's old namespace, still rendered",
			stamped("ClusterRole", "", "cluster-reconciler-impersonator", nil), pruneForget},
		{"stamp removed", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) { o.Metadata.Labels = nil }), pruneForget},
		{"another env's stamp now", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.Labels[ChartOwnerLabel] = "acme.staging"
		}), pruneForget},
		{"another chart's stamp now", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.Labels[WorkloadLabel] = "cert-manager"
		}), pruneForget},
		{"already being deleted", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.DeletionTimestamp = "2026-01-01T00:00:00Z"
		}), pruneForget},
		{"prune: disabled", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.Annotations = map[string]string{PruneAnnotation: PruneDisabled}
		}), pruneKeep},
		{"ownerReferences", stamped("ConfigMap", "flux-system", "x", func(o *liveChartObject) {
			o.Metadata.OwnerReferences = []json.RawMessage{[]byte(`{}`)}
		}), pruneKeep},
		{"PersistentVolumeClaim", stamped("PersistentVolumeClaim", "flux-system", "data", nil), pruneKeep},
		{"StatefulSet", stamped("StatefulSet", "flux-system", "db", nil), pruneKeep},
		{"Secret", stamped("Secret", "flux-system", "s", nil), pruneKeep},
		{"PersistentVolume", stamped("PersistentVolume", "", "pv", nil), pruneKeep},
		{"Namespace", stamped("Namespace", "", "n", nil), pruneKeep},
		{"CustomResourceDefinition", stamped("CustomResourceDefinition", "", "c", nil), pruneKeep},
	} {
		got, why := decideChartPrune(tc.live, desired, spec)
		if got != tc.want {
			t.Errorf("%s: verdict %d, want %d (%s)", tc.name, got, tc.want, why)
		}
		if got == pruneKeep && why == "" {
			t.Errorf("%s: a kept object must say why", tc.name)
		}
	}
}

// The inventory round-trips through its ConfigMap data, sorted by object, and
// a corrupt one is an error rather than an empty inventory: an empty one would
// read as "nothing was ever applied" and silently forget every object.
func TestChartInventory_EncodeDecode(t *testing.T) {
	inv := chartInventory{
		{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Namespace: "flux-system", Name: "b"}: "run-2",
		{Kind: "ConfigMap", Namespace: "flux-system", Name: "a"}:                                       "run-1",
	}
	data, err := inv.encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(data, `"ClusterRole"`) > strings.Index(data, `"ConfigMap"`) {
		t.Errorf("entries are not sorted by object:\n%s", data)
	}
	back, err := decodeChartInventory(data)
	if err != nil || !reflect.DeepEqual(back, inv) {
		t.Errorf("round trip = %v, %v; want %v", back, err, inv)
	}
	for _, bad := range []string{"", "{", `[{"kind":"","name":"x"}]`} {
		if _, err := decodeChartInventory(bad); err == nil {
			t.Errorf("decodeChartInventory(%q) accepted a corrupt inventory", bad)
		}
	}
}

// One inventory per chart AND per (project, env), with a name a ConfigMap can
// carry whatever characters the chart or owner hold.
func TestChartInventoryName(t *testing.T) {
	a := chartInventoryName("flux", "acme.prod")
	if a != chartInventoryName("flux", "acme.prod") {
		t.Error("the name must be stable across applies")
	}
	if a == chartInventoryName("flux", "acme.staging") || a == chartInventoryName("cert-manager", "acme.prod") {
		t.Error("two owners or two charts must not share an inventory")
	}
	dns := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for _, n := range []string{a, chartInventoryName("Weird_Chart.Name", "Acme_X.prod"), chartInventoryName(strings.Repeat("x", 200), "o")} {
		if !dns.MatchString(n) || len(n) > 253 {
			t.Errorf("%q is not a valid ConfigMap name", n)
		}
	}
}

// Namespace labels declared on a HelmChart land on the Namespace forge creates
// and CONVERGE: a changed value is updated, a key that is no longer declared is
// removed, and a key another manager set is left alone. The apiserver's own
// kubernetes.io/metadata.name is never disturbed.
func TestChartNamespaceMetadata_ConvergesWithoutFightingOtherManagers(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	setRelease := fakeHelmReleases(t)
	setRelease(stream(docDeployment))
	spec := fluxLikeSpec()
	spec.NamespaceLabels = map[string]string{
		"pod-security.kubernetes.io/enforce": "baseline",
		"team":                               "platform",
	}
	spec.NamespaceAnnotations = map[string]string{"owner": "sre"}

	applyRelease(t, spec)
	got := cl.labels("Namespace", "", "flux-system")
	if got["pod-security.kubernetes.io/enforce"] != "baseline" || got["team"] != "platform" {
		t.Fatalf("declared labels missing after the first apply: %v", got)
	}
	if got["kubernetes.io/metadata.name"] != "flux-system" {
		t.Errorf("the apiserver's own label was disturbed: %v", got)
	}

	// Another actor labels the Namespace; forge does not own that key.
	cl.setForeignLabel("Namespace", "", "flux-system", "istio-injection", "enabled")

	// Re-apply with a changed value and one declared key dropped.
	spec.NamespaceLabels = map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}
	applyRelease(t, spec)

	got = cl.labels("Namespace", "", "flux-system")
	if got["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Errorf("a changed declared label did not converge: %v", got)
	}
	if _, stale := got["team"]; stale {
		t.Errorf("a label forge stopped declaring was left behind: %v", got)
	}
	if got["istio-injection"] != "enabled" {
		t.Errorf("a label another manager set was stripped: %v", got)
	}
	if got["kubernetes.io/metadata.name"] != "flux-system" {
		t.Errorf("the apiserver's own label was stripped: %v", got)
	}
	if got[ChartOwnerLabel] != chartOwnerFixture {
		t.Errorf("the Namespace is not stamped with the chart owner: %v", got)
	}
}

// With nothing declared the Namespace is exactly what it always was: no labels
// or annotations beyond forge's own stamps.
func TestNamespaceManifest_NoMetadataByDefault(t *testing.T) {
	m, ok := parseDoc(namespaceManifest("flux-system", nil, nil))
	if !ok || m.Kind != "Namespace" || m.Metadata.Name != "flux-system" {
		t.Fatalf("namespaceManifest = %+v ok=%v", m, ok)
	}
	if len(m.Metadata.Labels) != 0 || len(m.Metadata.Annotations) != 0 {
		t.Errorf("an undeclared Namespace carries metadata: %+v", m.Metadata)
	}
	with, _ := parseDoc(namespaceManifest("psa", map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}, map[string]string{"a": "b"}))
	if with.Metadata.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || with.Metadata.Annotations["a"] != "b" {
		t.Errorf("declared metadata not rendered: %+v", with.Metadata)
	}
}

// A chart may not declare a label forge stamps on the Namespace it creates.
// RenderHelmChart refuses the spec, naming the key, before it runs helm.
// Without the refusal the declared value would decide the outcome silently: it
// would replace forge's app.kubernetes.io/name and managed-by, which
// stampDocAppLabel only defaults, and it would be overwritten on the forced
// forge.dev/* keys.
func TestRenderHelmChart_RefusesForgeOwnedNamespaceMetadata(t *testing.T) {
	requirePOSIXFake(t, "helm")
	// A helm that leaves a mark, so the test proves the refusal comes BEFORE the
	// render, which is a network operation.
	dir := t.TempDir()
	mark := filepath.Join(dir, "helm-ran")
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte("#!/bin/sh\ntouch "+mark+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		want        string
	}{
		{"app name", map[string]string{AppNameLabel: "flux2"}, nil, "label app.kubernetes.io/name"},
		{"managed-by", map[string]string{AppManagedByLabel: "flux"}, nil, "label app.kubernetes.io/managed-by"},
		{"workload", map[string]string{WorkloadLabel: "other"}, nil, "label forge.dev/workload"},
		{"chart owner", map[string]string{ChartOwnerLabel: "acme.staging"}, nil, "label forge.dev/chart-owner"},
		{"any forge.dev key", map[string]string{"forge.dev/anything": "x"}, nil, "label forge.dev/anything"},
		{"apiserver's name", map[string]string{"kubernetes.io/metadata.name": "x"}, nil, "label kubernetes.io/metadata.name"},
		{"forge.dev annotation", nil, map[string]string{"forge.dev/note": "x"}, "annotation forge.dev/note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := fluxLikeSpec()
			spec.NamespaceLabels = tc.labels
			spec.NamespaceAnnotations = tc.annotations
			// A PSA label beside it is fine on its own and must not mask the refusal.
			if spec.NamespaceLabels == nil {
				spec.NamespaceLabels = map[string]string{}
			}
			spec.NamespaceLabels["pod-security.kubernetes.io/enforce"] = "restricted"
			_, err := RenderHelmChart(context.Background(), spec)
			if err == nil {
				t.Fatalf("RenderHelmChart accepted namespace metadata %v / %v, which sets a key forge owns", tc.labels, tc.annotations)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"flux"`) {
				t.Errorf("the refusal must name the chart and the key (%q): %v", tc.want, err)
			}
			if _, statErr := os.Stat(mark); statErr == nil {
				t.Error("helm ran for a spec that must be refused before rendering")
			}
		})
	}
}

// The precedence the refusal protects: a declared PSA label lands beside
// forge's own stamps on the Namespace, and forge's stamps are forge's values.
func TestRenderHelmChart_NamespaceCarriesDeclaredAndForgeLabels(t *testing.T) {
	setRelease := fakeHelmReleases(t)
	setRelease(docDeployment)
	spec := fluxLikeSpec()
	spec.NamespaceLabels = map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}
	out, err := RenderHelmChart(context.Background(), spec)
	if err != nil {
		t.Fatalf("RenderHelmChart: %v", err)
	}
	var ns *parsedDoc
	for _, doc := range splitDocs(out) {
		if m, ok := parseDoc(doc); ok && m.Kind == "Namespace" {
			ns = &m
		}
	}
	if ns == nil {
		t.Fatalf("no Namespace in the render:\n%s", out)
	}
	for k, want := range map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted",
		AppNameLabel:                         "flux",
		AppManagedByLabel:                    "forge",
		WorkloadLabel:                        "flux",
		ChartOwnerLabel:                      chartOwnerFixture,
	} {
		if got := ns.Metadata.Labels[k]; got != want {
			t.Errorf("Namespace label %s = %q, want %q (labels %v)", k, got, want, ns.Metadata.Labels)
		}
	}
}

// ChartOwner shares CRDOwner's spelling and, like it, is empty when either half
// is unknown so the stamp and the prune switch off together.
func TestChartOwner(t *testing.T) {
	if got := ChartOwner("acme", "prod"); got != "acme.prod" {
		t.Errorf("ChartOwner = %q", got)
	}
	if ChartOwner("", "prod") != "" || ChartOwner("acme", " ") != "" {
		t.Error("an unnameable owner must be empty")
	}
}
