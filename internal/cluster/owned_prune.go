package cluster

// Owned-object prune — removing inert objects an env STOPPED rendering.
//
// `kubectl apply` adds and updates; nothing reconciles the SET. So an object a
// release replaced stays behind forever: the SC7 prod adoption of forge.OpenBao
// left the previous store's PodDisruptionBudget (two budgets over one set of
// pods blocked every node drain), its ingress NetworkPolicy, its RBAC and its
// auth-delegator ClusterRoleBinding in the cluster, to be deleted by hand.
//
// THE DESIGN, and the alternatives it was chosen over.
//
//	A. Ownership label + live prune set (this file). Every object the apply
//	   sends is stamped forge.dev/owner=<project>.<env>. After a full apply that
//	   came up healthy, the objects that CARRY that label, are absent from the
//	   render and are of an inert kind are deleted. Stateless: the cluster's own
//	   labels are the inventory, as for the CRD prune (crd_prune.go).
//	B. An inventory ConfigMap of applied UIDs behind a cluster-side lock, as the
//	   HelmChart prune does (chart_prune.go) and Flux does. Exact, and it survives
//	   a label edit, but it adds a lock and a state object to every env and still
//	   cannot prune what was applied before it existed.
//	C. Diff against the last applied BUNDLE from the release ledger. No stamp,
//	   but it needs that ledger on the deploying machine, and a partial or failed
//	   earlier apply makes the baseline wrong in the direction that deletes things.
//
// A is chosen because forge already uses it for CRDs, because "forge created it"
// is then a fact written on the object rather than inferred from a record that
// may be missing, and because the plan can list exactly what a deploy would
// delete by reading the same label.
//
// WHAT IT WILL DELETE. An object is deleted only when ALL hold:
//
//  1. it carries forge.dev/owner=<this project>.<this env>. The listing is
//     SERVER-SIDE label-selected, so an object forge did not stamp (a chart's, a
//     person's, another env's, anything applied before the stamp existed) is
//     never even a candidate;
//  2. it is of an inert kind (ownedPrunableKinds): it holds no data and owns no
//     data. Workloads stay behind --prune, and Secrets, PVCs, StatefulSets,
//     Namespaces, CRDs (their own prune), Jobs (job_prune.go), ExternalSecrets
//     (deleting one deletes the Secret it owns) and LoadBalancer Services (the
//     address goes with them) are never deleted here;
//  3. it is absent from the stream this apply just rendered FOR THIS CLUSTER,
//     measured BEFORE the hub's delivery units were taken out, so an object the
//     hub converges is never mistaken for a removed one;
//  4. it does not carry the Flux prune exemption
//     (kustomize.toolkit.fluxcd.io/prune: disabled), has no ownerReferences (a
//     controller owns it) and is not already terminating;
//  5. the deletion is made against the UID and resourceVersion just read, so a
//     replacement object cannot be deleted on a stale decision.
//
// WHEN. Only after the rollout it belongs to came up healthy (RolloutWait): the
// old object may be what the old pods still run under (their RBAC, their
// budget), so deleting it before the new ones serve trades an orphan for an
// outage. A full apply only: a --target subset renders part of the env, so an
// object missing from it proves nothing. A deploy that skips or only warns about
// the rollout prunes nothing and says so.
//
// BLAST RADIUS. More than ownedPruneCap candidates is refused wholesale rather
// than deleted in part: that many removals in one deploy means the render is
// wrong, not that the env shrank. An empty render prunes nothing.
//
// A failure is a warning, never a failed deploy: it is housekeeping, and the
// next deploy retries.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// OwnerLabel names the (project, env) whose apply sent an object. It is the
// prune's ownership proof; see the file comment. The value is CRDOwner's.
const OwnerLabel = "forge.dev/owner"

// ownedPruneCap is the most objects one deploy will delete.
const ownedPruneCap = 50

// ObjectOwner is the OwnerLabel value for a (project, env), "" when either half
// is unknown. An owner that cannot be named cannot be proven, so "" disables
// both the stamp and the prune.
func ObjectOwner(project, env string) string { return CRDOwner(project, env) }

// ownedPrunableKinds maps each kind the prune may delete to the kubectl resource
// argument that lists it. Inert kinds only; see the file comment.
var ownedPrunableKinds = map[string]string{
	"ServiceAccount":          "serviceaccounts",
	"Role":                    "roles.rbac.authorization.k8s.io",
	"RoleBinding":             "rolebindings.rbac.authorization.k8s.io",
	"ClusterRole":             "clusterroles.rbac.authorization.k8s.io",
	"ClusterRoleBinding":      "clusterrolebindings.rbac.authorization.k8s.io",
	"NetworkPolicy":           "networkpolicies.networking.k8s.io",
	"PodDisruptionBudget":     "poddisruptionbudgets.policy",
	"ConfigMap":               "configmaps",
	"Service":                 "services",
	"Ingress":                 "ingresses.networking.k8s.io",
	"HorizontalPodAutoscaler": "horizontalpodautoscalers.autoscaling",
}

// ownerStampSkipped are the kinds StampOwnership leaves alone: a CRD has its own
// stamp, and a Secret's metadata is not worth touching for a label nothing reads.
var ownerStampSkipped = map[string]bool{"CustomResourceDefinition": true, "Secret": true}

// StampOwnership labels every document in the stream with OwnerLabel=owner
// except CRDs and Secrets, and passes the rest through unchanged. Only
// metadata.labels is written: never a selector or a pod template, so no
// workload rolls. An empty owner is a no-op.
func StampOwnership(manifests, owner string) string {
	if owner == "" {
		return manifests
	}
	docs := splitDocs(manifests)
	changed := false
	for i, doc := range docs {
		m, ok := parseDoc(doc)
		if !ok || m.Kind == "" || ownerStampSkipped[m.Kind] {
			continue
		}
		b, ok := stampOwnerLabel(doc, owner)
		if !ok {
			continue
		}
		docs[i] = strings.TrimRight(string(b), "\n")
		changed = true
	}
	if !changed {
		return manifests
	}
	return strings.Join(docs, docDelimiter)
}

func stampOwnerLabel(doc, owner string) ([]byte, bool) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil || len(root.Content) == 0 {
		return nil, false
	}
	obj := root.Content[0]
	if obj.Kind != yaml.MappingNode {
		return nil, false
	}
	labels := mappingChild(mappingChild(obj, "metadata"), "labels")
	setScalar(labels, OwnerLabel, owner, true)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// OwnedKeep is an owned object the prune found and will not delete, and why.
type OwnedKeep struct {
	Cluster, Kind, Namespace, Name, Why string
}

// OwnedPruneReport is what a prune deletes or would delete, and what it keeps.
type OwnedPruneReport struct {
	Delete []liveChartObject
	Kept   []OwnedKeep
}

// ObjectRef names one rendered object. An empty Namespace on a namespaced kind
// matches that name in any namespace, the conservative reading for an object
// the render did not place.
type ObjectRef struct{ Kind, Namespace, Name string }

type desiredRefs struct {
	exact, anyNS map[ObjectRef]bool
}

func (d desiredRefs) has(kind, ns, name string) bool {
	return d.exact[ObjectRef{kind, ns, name}] || d.anyNS[ObjectRef{kind, "", name}]
}

func refsOfStream(stream string) (desiredRefs, int) {
	d := desiredRefs{exact: map[ObjectRef]bool{}, anyNS: map[ObjectRef]bool{}}
	n := 0
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		n++
		ref := ObjectRef{m.Kind, m.Metadata.Namespace, m.Metadata.Name}
		if ref.Namespace == "" && !clusterScopedKinds[m.Kind] {
			d.anyNS[ref] = true
			continue
		}
		d.exact[ref] = true
	}
	return d, n
}

// ObjectRefsOf lists the objects of a rendered stream, for a caller that plans a
// prune without applying.
func ObjectRefsOf(stream string) []ObjectRef {
	var out []ObjectRef
	for _, doc := range splitDocs(stream) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		out = append(out, ObjectRef{m.Kind, m.Metadata.Namespace, m.Metadata.Name})
	}
	return out
}

func refsOf(refs []ObjectRef) (desiredRefs, int) {
	d := desiredRefs{exact: map[ObjectRef]bool{}, anyNS: map[ObjectRef]bool{}}
	for _, r := range refs {
		if r.Namespace == "" && !clusterScopedKinds[r.Kind] {
			d.anyNS[r] = true
			continue
		}
		d.exact[r] = true
	}
	return d, len(refs)
}

func ownedPruneResources() string {
	rs := make([]string, 0, len(ownedPrunableKinds))
	for _, r := range ownedPrunableKinds {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	return strings.Join(rs, ",")
}

// decideOwnedPrune is the per-object verdict. The live object has already been
// matched to the owner by the server-side selector; this re-checks it, so the
// guard does not rest on the selector alone.
func decideOwnedPrune(o liveChartObject, owner string) (chartPruneVerdict, string) {
	switch {
	case o.Metadata.Labels[OwnerLabel] != owner:
		return pruneForget, "ownership stamp changed"
	case ownedPrunableKinds[o.Kind] == "":
		return pruneKeep, "not an inert kind; delete by hand"
	case o.Kind == "Service" && ownedServiceHoldsAddress(o):
		return pruneKeep, "load balancer: its external address would go with it; delete by hand"
	case o.Metadata.Annotations[PruneAnnotation] == PruneDisabled:
		return pruneKeep, "prune: disabled"
	case len(o.Metadata.OwnerReferences) > 0:
		return pruneKeep, "owned by a controller"
	case o.Metadata.DeletionTimestamp != "":
		return pruneForget, ""
	default:
		return pruneDelete, ""
	}
}

func ownedServiceHoldsAddress(o liveChartObject) bool {
	spec, _ := o.raw["spec"].(map[string]any)
	t, _ := spec["type"].(string)
	return t == "LoadBalancer"
}

// ownedPruneCandidates lists what a prune on kctx would act on. It reads the
// cluster and writes nothing.
func ownedPruneCandidates(ctx context.Context, kctx string, desired desiredRefs, rendered int, owner string) (OwnedPruneReport, error) {
	var rep OwnedPruneReport
	if strings.TrimSpace(kctx) == "" {
		return rep, errors.New("owned prune: no kubectl context — a delete is never aimed at the current context")
	}
	if owner == "" {
		return rep, errors.New("owned prune: no owner (project and env) — without one, forge cannot prove an object is its own")
	}
	if rendered == 0 {
		return rep, errors.New("owned prune: the render holds no objects, which is a wrong render, not an empty env; pruning nothing")
	}
	out, err := runChartJSON(ctx, kctx, []string{"get", ownedPruneResources(), "-A", "-l", OwnerLabel + "=" + owner, "-o", "json"}, nil)
	if err != nil {
		return rep, fmt.Errorf("owned prune: list objects owned by %s: %w", owner, err)
	}
	var list struct {
		Items []liveChartObject `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return rep, fmt.Errorf("owned prune: parse the owned objects: %w", err)
	}
	for _, o := range list.Items {
		if desired.has(o.Kind, o.Metadata.Namespace, o.Metadata.Name) {
			continue
		}
		switch verdict, why := decideOwnedPrune(o, owner); verdict {
		case pruneDelete:
			rep.Delete = append(rep.Delete, o)
		case pruneKeep:
			rep.Kept = append(rep.Kept, OwnedKeep{kctx, o.Kind, o.Metadata.Namespace, o.Metadata.Name, why})
		}
	}
	sort.Slice(rep.Delete, func(i, j int) bool { return rep.Delete[i].ref().String() < rep.Delete[j].ref().String() })
	sort.Slice(rep.Kept, func(i, j int) bool {
		a, b := rep.Kept[i], rep.Kept[j]
		return a.Kind+"/"+a.Namespace+"/"+a.Name < b.Kind+"/"+b.Namespace+"/"+b.Name
	})
	return rep, nil
}

// PruneOwned deletes the objects this (project, env) stamped that the stream no
// longer renders. It returns what it deleted and what it kept. A candidate set
// over the cap deletes nothing.
func PruneOwned(ctx context.Context, kctx, rendered, owner string) (pruned []string, kept []OwnedKeep, err error) {
	desired, n := refsOfStream(rendered)
	rep, err := ownedPruneCandidates(ctx, kctx, desired, n, owner)
	if err != nil {
		return nil, nil, err
	}
	if len(rep.Delete) > ownedPruneCap {
		return nil, rep.Kept, fmt.Errorf("owned prune: %d objects owned by %s are absent from the render, more than the %d one deploy will delete; "+
			"that is a wrong render, not an env that shrank. Nothing was deleted", len(rep.Delete), owner, ownedPruneCap)
	}
	kept = append(kept, rep.Kept...)
	var errs []error
	for _, c := range rep.Delete {
		// The listing is the decision's evidence; re-read the object so the
		// delete is made against ITS current UID and resourceVersion.
		cur, gerr := getChartObject(ctx, kctx, c.entry())
		if gerr != nil {
			errs = append(errs, gerr)
			continue
		}
		if cur == nil || cur.Metadata.UID != c.Metadata.UID {
			continue
		}
		if verdict, why := decideOwnedPrune(*cur, owner); verdict != pruneDelete {
			if verdict == pruneKeep {
				kept = append(kept, OwnedKeep{kctx, cur.Kind, cur.Metadata.Namespace, cur.Metadata.Name, why})
			}
			continue
		}
		if derr := deleteChartObject(ctx, kctx, *cur); derr != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", cur.ref(), derr))
			continue
		}
		pruned = append(pruned, cur.ref().String())
	}
	sort.Strings(pruned)
	return pruned, kept, errors.Join(errs...)
}

// PlanOwnedPrune is PruneOwned's decision without its effect: what a deploy of
// desired to kctx would delete, and what it would keep. desired is the set of
// objects the deploy renders for that cluster.
func PlanOwnedPrune(ctx context.Context, kctx string, desired []ObjectRef, owner string) (OwnedPruneReport, error) {
	d, n := refsOf(desired)
	return ownedPruneCandidates(ctx, kctx, d, n, owner)
}

// OwnedPruneChanges is a report as the plan's removals.
func (r OwnedPruneReport) OwnedPruneChanges(kctx string) []release.OwnedRemoval {
	out := make([]release.OwnedRemoval, 0, len(r.Delete))
	for _, o := range r.Delete {
		fp, err := release.HashDocument(map[string]any{"prune-owned": o.ref().String(), "uid": o.Metadata.UID})
		if err != nil {
			fp = ""
		}
		out = append(out, release.OwnedRemoval{
			Key:         release.ObjectKey{Cluster: kctx, Kind: o.Kind, Namespace: o.Metadata.Namespace, Name: o.Metadata.Name},
			Fingerprint: fp,
		})
	}
	return out
}

// ownedPruneJob is a prune waiting for its rollout to come up healthy.
type ownedPruneJob struct {
	kctx, rendered, owner string
}

// run is the post-rollout step: housekeeping, so every outcome is printed and
// none fails the deploy.
func (j *ownedPruneJob) run(ctx context.Context) {
	pruned, kept, err := PruneOwned(ctx, j.kctx, j.rendered, j.owner)
	for _, k := range kept {
		fmt.Printf("  kept %s %s%s: %s\n", k.Kind, nsPrefix(k.Namespace), k.Name, k.Why)
	}
	if len(pruned) > 0 {
		fmt.Printf("Pruned %d object(s) no longer rendered by %s: %s\n", len(pruned), j.owner, strings.Join(pruned, ", "))
	}
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
}

func nsPrefix(ns string) string {
	if ns == "" {
		return ""
	}
	return ns + "/"
}
