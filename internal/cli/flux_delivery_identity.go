package cli

// The APPLY IDENTITIES of a self-hosted delivery (Bundle.applier = "flux",
// FluxDelivery.apply_identities), derived from what the env's bundle contains.
//
// WHY THIS EXISTS. With Flux's impersonation lockdown on, kustomize-controller
// applies every Kustomization as a ServiceAccount, never as itself. #441
// shipped that path with the controller's `default` account and every apply
// failed Forbidden on the Namespaces and CRDs the bundle carried, so it
// shipped the lockdown off instead. The fix is the one `forge cluster connect` already
// uses (cluster_connect_scope.go): THE RENDER CHOOSES THE KINDS. One rule per
// (apiGroup, resource) the trees apply, nothing else, so a release that adds a
// kind is Forbidden until the bootstrap re-derives the grant.
//
// TWO IDENTITIES, BY PHASE. Bundles are split into `pre` / rollout / post
// trees (release.ClassifyPhase): Namespaces, CRDs, ClusterRoles,
// StorageClasses — every cluster-scoped kind — and the config a release rests
// on are `pre`; workloads are rollout; post-rollout Jobs are post.
//
//   - fluxdelivery.PreServiceAccount applies the `pre` trees: a ClusterRole
//     over the cluster-scoped kinds they carry (Namespaces create/get/patch,
//     never delete: a pruned Namespace takes every workload in it), plus
//     their namespaced kinds;
//   - fluxdelivery.ApplyServiceAccount applies rollout and post: namespaced
//     kinds only (a cluster-scoped kind there is refused: it cannot occur);
//   - fluxdelivery.NoneServiceAccount is the controller's default and is
//     granted nothing here, so a Kustomization that names neither identity
//     is Forbidden rather than over-privileged.
//
// SCOPE. `delivery_scope = "namespace"` (default): the namespaced kinds are a
// Role in each namespace the render targets. A Role needs its namespace to
// exist before the first release has created it, so the Namespaces the render
// itself declares ride the grant as bare objects, stamped prune-disabled so the
// chart's prune can never delete one. "cluster": one ClusterRole over the same
// kinds in every namespace.
//
// escalate and bind are pinned by resourceNames to the exact Roles and
// ClusterRoles the render declares or binds to (addRBACPins), as connect pins
// them: an identity that may create RBAC cannot mint a role the render does
// not name.

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// namespaceApplyVerbs are the pre identity's verbs on Namespaces: create one
// the release declares, update it in place, and never delete one.
var namespaceApplyVerbs = []string{"create", "get", "list", "patch", "update", "watch"}

// fluxDeliveryLabels are stamped on every object of a self-hosted delivery.
func fluxDeliveryLabels(env string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "flux-delivery",
		"app.kubernetes.io/managed-by": "forge",
		fluxdelivery.EnvLabel:          env,
	}
}

// objectPhase is the tree an object lands in, by the classifier the bundle's
// layout uses.
func objectPhase(o bundle.RenderedObject) (release.Phase, error) {
	md, _ := o.Body["metadata"].(map[string]any)
	raw, _ := md["annotations"].(map[string]any)
	annotations := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			annotations[k] = s
		}
	}
	return release.ClassifyPhase(o.Kind, o.Name, annotations)
}

// fluxDeliveryGrant is one identity's resolved rules.
type fluxDeliveryGrant struct {
	// ServiceAccount is the identity, in the delivery namespace.
	ServiceAccount string
	// Name names its ClusterRole/Role and bindings: <delivery namespace>-<suffix>.
	Name string
	// ClusterRules are its ClusterRole's rules; empty renders none.
	ClusterRules []ruleSpec
	// NamespaceRules are its Roles' rules, by namespace.
	NamespaceRules map[string][]ruleSpec
}

// fluxDeliveryGrants derives both identities' grants from an annotated render
// stream, for the delivery's one cluster.
func fluxDeliveryGrants(stream string, fd FluxDeliveryEntity) (pre, apply fluxDeliveryGrant, namespaces []string, err error) {
	objs, err := bundle.ParseRenderedStream(stream)
	if err != nil {
		return pre, apply, nil, err
	}
	inPhase := func(want func(release.Phase) bool) func(bundle.RenderedObject) (bool, error) {
		return func(o bundle.RenderedObject) (bool, error) {
			p, err := objectPhase(o)
			return want(p), err
		}
	}
	preNeeds, err := deriveGrantNeedsOf(objs, fd.Cluster, inPhase(func(p release.Phase) bool { return p == release.PhasePre }))
	if err != nil {
		return pre, apply, nil, err
	}
	applyNeeds, err := deriveGrantNeedsOf(objs, fd.Cluster, inPhase(func(p release.Phase) bool { return p != release.PhasePre }))
	if err != nil {
		return pre, apply, nil, err
	}
	if preNeeds.OnCluster == 0 {
		return pre, apply, nil, fmt.Errorf("the env's render puts nothing on cluster %q, so there is nothing to derive a grant from", fd.Cluster)
	}
	if len(applyNeeds.Cluster) > 0 || len(applyNeeds.NamespaceObjects) > 0 {
		var kinds []string
		for r, example := range applyNeeds.Cluster {
			kinds = append(kinds, fmt.Sprintf("%s (%s)", r, example))
		}
		for ns := range applyNeeds.NamespaceObjects {
			kinds = append(kinds, "Namespace/"+ns)
		}
		sort.Strings(kinds)
		return pre, apply, nil, fmt.Errorf("a rollout or post tree carries cluster-scoped kinds (%s), which only the pre identity may write; "+
			"release.ClassifyPhase places every such kind in pre, so this render is not one forge built", strings.Join(kinds, ", "))
	}
	clusterScope := fd.DeliveryScope == "cluster"
	pre = fluxDeliveryGrant{ServiceAccount: fluxdelivery.PreServiceAccount, Name: fd.Namespace + "-pre"}
	apply = fluxDeliveryGrant{ServiceAccount: fluxdelivery.ApplyServiceAccount, Name: fd.Namespace + "-apply"}

	// The pre identity's cluster-scoped half, in every scope.
	preCluster := ruleSet{}
	for r := range preNeeds.Cluster {
		preCluster.add(r, nil, applyVerbs...)
	}
	if len(preNeeds.NamespaceObjects) > 0 {
		preCluster.add(refNamespaces, nil, namespaceApplyVerbs...)
	}
	addRBACPins(preCluster, nil, nil, preNeeds.ClusterRoleNames, preNeeds.ClusterBoundRoles)

	used := map[string]bool{}
	namespaced := func(g *fluxDeliveryGrant, needs grantNeeds, clusterRules ruleSet) {
		if clusterScope {
			union := map[resourceRef]bool{}
			for _, refs := range needs.Namespaced {
				for r := range refs {
					union[r] = true
				}
			}
			for r := range union {
				clusterRules.add(r, nil, applyVerbs...)
			}
			addHealthReads(clusterRules, union)
			addRBACPins(clusterRules, unionByNamespace(needs.RoleNames), unionByNamespace(needs.BindRoles), nil, unionByNamespace(needs.BindClusterRoles))
			return
		}
		g.NamespaceRules = map[string][]ruleSpec{}
		for ns, refs := range needs.Namespaced {
			rs := ruleSet{}
			for r := range refs {
				rs.add(r, nil, applyVerbs...)
			}
			addHealthReads(rs, refs)
			addRBACPins(rs, needs.RoleNames[ns], needs.BindRoles[ns], nil, needs.BindClusterRoles[ns])
			g.NamespaceRules[ns] = rs.rules()
			used[ns] = true
		}
	}
	namespaced(&pre, preNeeds, preCluster)
	pre.ClusterRules = preCluster.rules()
	applyCluster := ruleSet{}
	namespaced(&apply, applyNeeds, applyCluster)
	apply.ClusterRules = applyCluster.rules()

	// The Namespaces a Role lands in that the render itself declares: they
	// must exist before the release that creates them has run.
	for ns := range used {
		if preNeeds.NamespaceObjects[ns] {
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	return pre, apply, namespaces, nil
}

// fluxDeliveryIdentityObjects renders the grants as RBAC objects (and the
// Namespaces their Roles need), sorted.
func fluxDeliveryIdentityObjects(stream string, fd FluxDeliveryEntity) ([]rbacObject, error) {
	pre, apply, namespaces, err := fluxDeliveryGrants(stream, fd)
	if err != nil {
		return nil, err
	}
	labels := func() map[string]any {
		out := map[string]any{}
		for k, v := range fluxDeliveryLabels(fd.Env) {
			out[k] = v
		}
		return out
	}
	rbac := func(kind, namespace, name string, extra map[string]any) rbacObject {
		meta := map[string]any{"name": name, "labels": labels()}
		if namespace != "" {
			meta["namespace"] = namespace
		}
		body := map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": meta}
		for k, v := range extra {
			body[k] = v
		}
		return rbacObject{Kind: kind, Namespace: namespace, Name: name, Body: body}
	}
	var out []rbacObject
	for _, ns := range namespaces {
		out = append(out, rbacObject{Kind: "Namespace", Name: ns, Body: map[string]any{
			"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{
				"name": ns, "labels": labels(),
				// The chart's prune never deletes a Namespace it shares with
				// the release that also declares it.
				"annotations": map[string]any{cluster.PruneAnnotation: cluster.PruneDisabled},
			},
		}})
	}
	for _, g := range []fluxDeliveryGrant{pre, apply} {
		subjects := []any{map[string]any{"kind": "ServiceAccount", "name": g.ServiceAccount, "namespace": fd.Namespace}}
		if len(g.ClusterRules) > 0 {
			out = append(out,
				rbac("ClusterRole", "", g.Name, map[string]any{"rules": rulesBody(g.ClusterRules)}),
				rbac("ClusterRoleBinding", "", g.Name, map[string]any{
					"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": g.Name},
					"subjects": subjects,
				}))
		}
		for ns, rules := range g.NamespaceRules {
			if len(rules) == 0 {
				continue
			}
			out = append(out,
				rbac("Role", ns, g.Name, map[string]any{"rules": rulesBody(rules)}),
				rbac("RoleBinding", ns, g.Name, map[string]any{
					"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": g.Name},
					"subjects": subjects,
				}))
		}
	}
	return sortObjects(out), nil
}

// fluxDeliveryIdentityManifests is fluxDeliveryIdentityObjects as the `---`
// stream that rides the bootstrap chart.
func fluxDeliveryIdentityManifests(stream string, fd FluxDeliveryEntity) (string, error) {
	objs, err := fluxDeliveryIdentityObjects(stream, fd)
	if err != nil {
		return "", err
	}
	docs := make([]string, 0, len(objs))
	for _, o := range objs {
		b, err := yaml.Marshal(o.Body)
		if err != nil {
			return "", fmt.Errorf("marshal %s: %w", o.ref(), err)
		}
		docs = append(docs, strings.TrimRight(string(b), "\n"))
	}
	if len(docs) == 0 {
		return "", nil
	}
	return strings.Join(docs, "\n---\n") + "\n", nil
}
