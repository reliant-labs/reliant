package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The owned-object prune, proven against the stateful fake cluster
// (fake_cluster_test.go): each test applies release N as forge applies it
// (stamped), then asks what release N+1 deletes, and reads what is LIVE.

const (
	ownedTestCtx   = "k3d-test"
	ownedTestOwner = "acme.prod"
	ownedNS        = "acme-prod"
)

func ownedDoc(kind, apiVersion, name, namespace, extra string) string {
	ns := ""
	if namespace != "" {
		ns = "\n  namespace: " + namespace
	}
	return fmt.Sprintf("apiVersion: %s\nkind: %s\nmetadata:\n  name: %s%s\n%s", apiVersion, kind, name, ns, extra)
}

// The SC7 adoption's leftovers: the bespoke store's PDB, ingress policy, RBAC and
// auth-delegator binding, plus the workload that stays.
var (
	oldPDB      = ownedDoc("PodDisruptionBudget", "policy/v1", "openbao-pdb", ownedNS, "spec:\n  maxUnavailable: 1\n")
	oldNetpol   = ownedDoc("NetworkPolicy", "networking.k8s.io/v1", "allow-openbao-ingress", ownedNS, "spec:\n  podSelector: {}\n")
	oldRole     = ownedDoc("Role", "rbac.authorization.k8s.io/v1", "openbao-bootstrap", ownedNS, "rules: []\n")
	oldRB       = ownedDoc("RoleBinding", "rbac.authorization.k8s.io/v1", "openbao-bootstrap", ownedNS, "roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: openbao-bootstrap}\n")
	oldDelegate = ownedDoc("ClusterRoleBinding", "rbac.authorization.k8s.io/v1", "openbao-control-plane-prod-auth-delegator", "", "roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: system:auth-delegator}\n")
	newPDB      = ownedDoc("PodDisruptionBudget", "policy/v1", "openbao", ownedNS, "spec:\n  maxUnavailable: 1\n")
	storeDeploy = ownedDoc("Deployment", "apps/v1", "openbao", ownedNS, "spec: {}\n")
	storeSecret = ownedDoc("Secret", "v1", "openbao-unseal", ownedNS, "type: Opaque\n")
)

func applyStamped(t *testing.T, stream string) {
	t.Helper()
	if err := KubectlApply(context.Background(), ownedTestCtx, StampOwnership(stream, ownedTestOwner)); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// The prod scenario. Release N has the bespoke store's objects; release N+1 is
// the forge component's. The replaced PDB, policy, RBAC and binding go; the
// workload, the Secret and everything N+1 renders stay.
//
// Mutation that fails it: delete the PruneOwned call from runOwnedPrunes.
func TestPruneOwned_DeletesWhatTheRenderStoppedProducing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	applyStamped(t, stream(storeDeploy, storeSecret, oldPDB, oldNetpol, oldRole, oldRB, oldDelegate))
	next := stream(storeDeploy, storeSecret, newPDB)
	applyStamped(t, next)

	pruned, kept, err := PruneOwned(context.Background(), ownedTestCtx, next, ownedTestOwner)
	if err != nil {
		t.Fatalf("PruneOwned: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("nothing here is protected, kept = %v", kept)
	}
	if len(pruned) != 5 {
		t.Errorf("pruned %v, want the five replaced objects", pruned)
	}
	for _, gone := range [][3]string{
		{"PodDisruptionBudget", ownedNS, "openbao-pdb"},
		{"NetworkPolicy", ownedNS, "allow-openbao-ingress"},
		{"Role", ownedNS, "openbao-bootstrap"},
		{"RoleBinding", ownedNS, "openbao-bootstrap"},
		{"ClusterRoleBinding", "", "openbao-control-plane-prod-auth-delegator"},
	} {
		if cl.has(gone[0], gone[1], gone[2]) {
			t.Errorf("%v should have been pruned", gone)
		}
	}
	for _, stays := range [][3]string{
		{"Deployment", ownedNS, "openbao"},
		{"Secret", ownedNS, "openbao-unseal"},
		{"PodDisruptionBudget", ownedNS, "openbao"},
	} {
		if !cl.has(stays[0], stays[1], stays[2]) {
			t.Errorf("%v must survive the prune", stays)
		}
	}

	// A second deploy of the same render has nothing left to delete.
	again, _, err := PruneOwned(context.Background(), ownedTestCtx, next, ownedTestOwner)
	if err != nil || len(again) != 0 {
		t.Errorf("the prune is not idempotent: %v, %v", again, err)
	}
}

// forge deletes only what it stamped: an object nobody stamped (applied before
// the stamp, or by a person or a chart), one another env stamped, and the kinds
// and annotations that opt out all survive an absent-from-render verdict.
//
// Mutation that fails it: drop the `-l forge.dev/owner=` selector from the list.
func TestPruneOwned_NeverTouchesWhatForgeDidNotStamp(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	next := stream(storeDeploy)
	applyStamped(t, stream(storeDeploy, oldPDB))

	meta := func(name string, labels map[string]any, ann map[string]any, extra map[string]any) map[string]any {
		md := map[string]any{"name": name, "namespace": ownedNS, "uid": "uid-" + name, "resourceVersion": "900", "labels": labels}
		if ann != nil {
			md["annotations"] = ann
		}
		for k, v := range extra {
			md[k] = v
		}
		return md
	}
	mine := map[string]any{OwnerLabel: ownedTestOwner}
	cl.seed(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("hand-made", map[string]any{}, nil, nil)})
	cl.seed(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("other-env", map[string]any{OwnerLabel: "acme.staging"}, nil, nil)})
	cl.seed(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("exempt", mine, map[string]any{PruneAnnotation: PruneDisabled}, nil)})
	cl.seed(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("controller-owned", mine, nil, map[string]any{"ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "x", "uid": "u"}}})})
	cl.seed(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": meta("public-lb", mine, nil, nil), "spec": map[string]any{"type": "LoadBalancer"}})
	// A kind the prune never lists, even stamped and orphaned.
	cl.seed(map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta("data", mine, nil, nil)})
	cl.seed(map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": meta("db", mine, nil, nil)})

	pruned, kept, err := PruneOwned(context.Background(), ownedTestCtx, next, ownedTestOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || !strings.Contains(pruned[0], "openbao-pdb") {
		t.Errorf("pruned %v, want only the stamped, unprotected openbao-pdb", pruned)
	}
	for _, s := range [][3]string{
		{"NetworkPolicy", ownedNS, "hand-made"}, {"NetworkPolicy", ownedNS, "other-env"}, {"NetworkPolicy", ownedNS, "exempt"},
		{"ConfigMap", ownedNS, "controller-owned"}, {"Service", ownedNS, "public-lb"},
		{"PersistentVolumeClaim", ownedNS, "data"}, {"StatefulSet", ownedNS, "db"}, {"Deployment", ownedNS, "openbao"},
	} {
		if !cl.has(s[0], s[1], s[2]) {
			t.Errorf("%v must survive", s)
		}
	}
	why := map[string]string{}
	for _, k := range kept {
		why[k.Name] = k.Why
	}
	for name, want := range map[string]string{"exempt": "prune: disabled", "controller-owned": "owned by a controller", "public-lb": "load balancer"} {
		if !strings.Contains(why[name], want) {
			t.Errorf("kept %q because %q, want a reason naming %q (all: %v)", name, why[name], want, why)
		}
	}
}

// More removals than a deploy may make is a wrong render, and deletes nothing.
func TestPruneOwned_RefusesAWholesaleRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	var docs []string
	for i := 0; i <= ownedPruneCap; i++ {
		docs = append(docs, ownedDoc("ConfigMap", "v1", fmt.Sprintf("cm-%02d", i), ownedNS, "data: {}\n"))
	}
	applyStamped(t, stream(append([]string{storeDeploy}, docs...)...))
	pruned, _, err := PruneOwned(context.Background(), ownedTestCtx, stream(storeDeploy), ownedTestOwner)
	if err == nil || !strings.Contains(err.Error(), "Nothing was deleted") {
		t.Fatalf("want a refusal, got %v (pruned %v)", err, pruned)
	}
	if len(pruned) != 0 || !cl.has("ConfigMap", ownedNS, "cm-00") || len(cl.deletes()) != 0 {
		t.Errorf("a refused prune deleted something: %v", cl.deletes())
	}
}

// A render with no objects is a wrong render, not an empty env.
func TestPruneOwned_AnEmptyRenderDeletesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	applyStamped(t, stream(storeDeploy, oldPDB))
	if _, _, err := PruneOwned(context.Background(), ownedTestCtx, "", ownedTestOwner); err == nil {
		t.Fatal("an empty render must be refused")
	}
	if !cl.has("PodDisruptionBudget", ownedNS, "openbao-pdb") {
		t.Error("an empty render deleted an object")
	}
	if _, _, err := PruneOwned(context.Background(), ownedTestCtx, stream(storeDeploy), ""); err == nil {
		t.Fatal("no owner must be refused")
	}
	if _, _, err := PruneOwned(context.Background(), "", stream(storeDeploy), ownedTestOwner); err == nil {
		t.Fatal("no kubectl context must be refused")
	}
}

// The plan lists exactly what the prune deletes and writes nothing.
func TestPlanOwnedPrune_ListsWhatPruneDeletesAndWritesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	applyStamped(t, stream(storeDeploy, oldPDB, oldNetpol, oldDelegate))
	desired := []ObjectRef{{"Deployment", ownedNS, "openbao"}, {"PodDisruptionBudget", ownedNS, "openbao"}}

	rep, err := PlanOwnedPrune(context.Background(), ownedTestCtx, desired, ownedTestOwner)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range rep.Delete {
		got = append(got, o.ref().String())
	}
	want := []string{"ClusterRoleBinding/openbao-control-plane-prod-auth-delegator", "NetworkPolicy/" + ownedNS + "/allow-openbao-ingress", "PodDisruptionBudget/" + ownedNS + "/openbao-pdb"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("plan lists %v, want %v", got, want)
	}
	if len(cl.deletes()) != 0 || !cl.has("PodDisruptionBudget", ownedNS, "openbao-pdb") {
		t.Errorf("planning deleted something: %v", cl.deletes())
	}
	changes := rep.OwnedPruneChanges(ownedTestCtx)
	if len(changes) != 3 || changes[0].Fingerprint == "" || changes[0].Key.Cluster != ownedTestCtx {
		t.Errorf("plan changes = %+v", changes)
	}
}

// The prune waits for a healthy rollout: the old objects may be what the old
// pods still run under. A failed wait, and a mode that proves nothing, delete
// nothing; a healthy wait deletes.
//
// Mutation that fails it: run the prune regardless of the verdict.
func TestRunOwnedPrunes_OnlyAfterAHealthyRollout(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	next := stream(storeDeploy)
	applyStamped(t, stream(storeDeploy, oldPDB))
	job := func(mode RolloutMode) []*rolloutWait {
		return []*rolloutWait{{p: &PendingRollout{
			opts:   ApplyOpts{Context: ownedTestCtx, Namespace: ownedNS},
			policy: RolloutPolicy{Mode: mode},
			prune:  &ownedPruneJob{kctx: ownedTestCtx, rendered: next, owner: ownedTestOwner},
		}}}
	}

	runOwnedPrunes(context.Background(), job(RolloutWait), fmt.Errorf("deployment never ready"))
	runOwnedPrunes(context.Background(), job(RolloutWarn), nil)
	if !cl.has("PodDisruptionBudget", ownedNS, "openbao-pdb") {
		t.Fatal("pruned without a healthy rollout")
	}
	runOwnedPrunes(context.Background(), job(RolloutWait), nil)
	if cl.has("PodDisruptionBudget", ownedNS, "openbao-pdb") {
		t.Error("a healthy rollout should have pruned the replaced PDB")
	}
}

// What an apply owes: nothing for a --target subset, an unnamed owner or a
// missing context.
func TestOwnedPruneFor(t *testing.T) {
	base := ApplyOpts{Project: "acme", Env: "prod", Context: ownedTestCtx}
	if ownedPruneFor(base, stream(storeDeploy)) == nil {
		t.Fatal("a full apply of a named owner owes a prune")
	}
	for name, mut := range map[string]func(*ApplyOpts){
		"targets":    func(o *ApplyOpts) { o.Targets = []string{"api"} },
		"no project": func(o *ApplyOpts) { o.Project = "" },
		"no env":     func(o *ApplyOpts) { o.Env = "" },
		"no context": func(o *ApplyOpts) { o.Context = "" },
	} {
		o := base
		mut(&o)
		if ownedPruneFor(o, stream(storeDeploy)) != nil {
			t.Errorf("%s: the apply should owe no prune", name)
		}
	}
}

// The stamp is a label and nothing else: no selector, no template, no Secret, no
// CRD (which has its own), and re-stamping is a no-op.
func TestStampOwnership(t *testing.T) {
	in := stream(storeDeploy, storeSecret, oldDelegate, ownedDoc("CustomResourceDefinition", "apiextensions.k8s.io/v1", "x.acme.dev", "", "spec: {group: acme.dev}\n"))
	out := StampOwnership(in, ownedTestOwner)
	for _, doc := range splitDocs(out) {
		m, _ := parseDoc(doc)
		got := m.Metadata.Labels[OwnerLabel]
		switch m.Kind {
		case "Secret", "CustomResourceDefinition":
			if got != "" {
				t.Errorf("%s was stamped", m.Kind)
			}
		default:
			if got != ownedTestOwner {
				t.Errorf("%s %s owner = %q", m.Kind, m.Metadata.Name, got)
			}
		}
	}
	if again := StampOwnership(out, ownedTestOwner); again != out {
		t.Error("stamping twice changed the stream")
	}
	if StampOwnership(in, "") != in {
		t.Error("an empty owner must be a no-op")
	}
	if !strings.Contains(out, "spec: {}") && !strings.Contains(out, "spec:") {
		t.Error("the stamp lost a spec")
	}
}

// An object the hub converges is not forge's to apply, and is not "removed":
// the prune reads the render BEFORE the hub's units are taken out of it, so a
// hub-converged object is in the desired set and survives.
//
// Mutation that fails it: pass the unit-filtered stream to ownedPruneFor.
func TestOwnedPrune_HubConvergedObjectsAreNotMistakenForRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl (this test binary) through many subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	hubPDB := strings.Replace(newPDB, "metadata:\n", "metadata:\n  labels:\n    forge.dev/delivery-unit: platform\n", 1)
	full := stream(storeDeploy, hubPDB)
	applyStamped(t, stream(storeDeploy, hubPDB, oldPDB))

	opts := ApplyOpts{Project: "acme", Env: "prod", Context: ownedTestCtx, SkipUnits: []string{"platform"}}
	job := ownedPruneFor(opts, full)
	if job == nil {
		t.Fatal("expected a prune")
	}
	job.run(context.Background())
	if !cl.has("PodDisruptionBudget", ownedNS, "openbao") {
		t.Error("the hub-converged PDB was pruned as if the env had stopped rendering it")
	}
	if cl.has("PodDisruptionBudget", ownedNS, "openbao-pdb") {
		t.Error("the replaced PDB should have gone")
	}
}
