package cli

// Binding a RENDERED cluster to a CONNECTED one, and refusing the env whose
// declaration cannot be shipped.
//
// A forge bundle may render for several clusters at once (one subtree per
// cluster, keyed by release.BundleClusterPath), so "which cluster does this
// environment deploy to" has more than one answer and the control plane takes
// a repeated field. A binding is one pair: the rendered cluster key — the
// kubectl context, as it appears in the bundle's ClusterPaths — and the
// connected cluster's name.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// resolveClusterBindings turns the env's declared connected-cluster NAMES
// into the ids the wire carries, with one ListClusters call.
//
// ONE CALL, NOT ONE PER BINDING: a multi-cluster env would otherwise make a
// request per target to learn ids it could have read together, and the list is
// already org-scoped.
//
// A name the org does not have is an ERROR, not a dropped binding. Silently
// omitting it would record the environment with no target and report success
// — which is exactly the "accepted and never lands" outcome the render
// refusal exists to prevent.
func resolveClusterBindings(ctx context.Context, c cloudCaller, e *KCLEntities) ([]deploytarget.ClusterBinding, error) {
	declared := clusterBindingsOf(e)
	if len(declared) == 0 {
		return nil, nil
	}
	var resp struct {
		Clusters []wireConnectedCluster `json:"clusters"`
	}
	if err := c.Call(ctx, procListClusters, map[string]any{}, &resp); err != nil {
		return nil, fmt.Errorf("list the organization's connected clusters: %w", err)
	}
	idByName := make(map[string]string, len(resp.Clusters))
	known := make([]string, 0, len(resp.Clusters))
	for _, cl := range resp.Clusters {
		idByName[cl.Name] = cl.ID
		known = append(known, cl.Name)
	}
	out := make([]deploytarget.ClusterBinding, 0, len(declared))
	for _, b := range declared {
		id, ok := idByName[b.ConnectedCluster]
		if !ok {
			sort.Strings(known)
			return nil, fmt.Errorf("cluster %q targets connected cluster %q, which this organization has not "+
				"connected (it has: %s)\n"+
				"fix: forge cluster connect %s --context %s --env <env>",
				b.Cluster, b.ConnectedCluster, strings.Join(known, ", "), b.ConnectedCluster, b.Cluster)
		}
		out = append(out, deploytarget.ClusterBinding{Cluster: b.Cluster, ClusterID: id})
	}
	return out, nil
}

// clusterBindingsOf derives the env's rendered-cluster → connected-cluster
// bindings from its KCL.
//
// Keyed on the CONTEXT rather than the connected name, because the context is
// the bundle's own key and the thing the control plane matches against. Two
// workloads on the same target produce one binding, and the result is sorted
// so a request's bytes do not depend on map iteration order.
func clusterBindingsOf(e *KCLEntities) []clusterBinding {
	if e == nil {
		return nil
	}
	byContext, _ := declaredBindings(e)
	out := make([]clusterBinding, 0, len(byContext))
	for context, names := range byContext {
		out = append(out, clusterBinding{Cluster: context, ConnectedCluster: names[0]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cluster < out[j].Cluster })
	return out
}

// declaredBindings merges every place an env names a connected cluster for a
// context: the primary target, each cluster workload's runtime, and the
// cluster-level ConnectedClusters map (which is how a cluster reached only by
// raw groups — Manifests, HelmChart, Generated — is bound, once). The result
// maps context -> distinct names, sorted; more than one name for a context is
// drift, returned as the second value for the refusal to report.
func declaredBindings(e *KCLEntities) (map[string][]string, []string) {
	seen := map[string]map[string]bool{}
	note := func(context, connected string) {
		if context == "" || connected == "" {
			return
		}
		if seen[context] == nil {
			seen[context] = map[string]bool{}
		}
		seen[context][connected] = true
	}
	note(e.ClusterTarget.field("cluster"), connectedClusterOf(e.ClusterTarget))
	for _, w := range e.Workloads {
		if w.Runtime.Type == RuntimeCluster && w.Runtime.Cluster != nil {
			note(w.Runtime.Cluster.Cluster, w.Runtime.Cluster.ConnectedCluster)
		}
	}
	for context, connected := range e.ConnectedClusters {
		note(context, connected)
	}
	// A Secret the control plane delivers names the connected cluster it goes
	// through, so a cluster ONLY such a Secret reaches is still bound and
	// recorded, and a second name for the same context is drift like any other.
	for _, c := range e.ControlPlaneSecrets {
		note(c.Cluster, c.ConnectedCluster)
	}
	out := make(map[string][]string, len(seen))
	var conflicts []string
	for context, set := range seen {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		out[context] = names
		if len(names) > 1 {
			conflicts = append(conflicts, fmt.Sprintf("%q is bound to %s", context, strings.Join(quoteEach(names), " and ")))
		}
	}
	sort.Strings(conflicts)
	return out, conflicts
}

// clusterBinding is one rendered cluster and the connected cluster it maps
// to, before the connected NAME has been resolved to the id the wire carries.
type clusterBinding struct {
	// Cluster is the rendered cluster / kube-context name — the bundle's own
	// key (release.BundleClusterPath).
	Cluster string
	// ConnectedCluster is the name `forge cluster connect` registered.
	ConnectedCluster string
}

func connectedClusterOf(t *ClusterTargetEntity) string {
	if t == nil {
		return ""
	}
	return t.ConnectedCluster
}

// refuseUnboundClusterTargets refuses, AT RENDER, an env that declares a
// control plane and targets a real cluster with no connected cluster named.
//
// WHY THIS IS A RENDER-TIME REFUSAL rather than a deploy-time one. The env's
// declaration is what the control plane records, and a declaration naming a
// cluster the platform cannot reach is a deploy that is accepted and never
// lands — the failure arrives later, as a Flux reconcile error about a
// credential, two layers from the missing field. Refusing while the author is
// still looking at the KCL costs nothing and names the exact fix.
//
// A k3d CONTEXT IS EXEMPT, and that exemption is the common case: forge
// applies to a local cluster directly and the control plane never dials it, so
// requiring a registration for one would mean connecting a cluster that only
// exists on one laptop.
func refuseUnboundClusterTargets(envName string, e *KCLEntities) error {
	if err := refuseUndeliverableControlPlaneSecrets(envName, e); err != nil {
		return err
	}
	if e == nil || e.ControlPlane == nil {
		return nil
	}
	// bound is every context some declaration ties to a connected cluster;
	// required is every context the env renders a tree for or places
	// something on.
	bound, conflicts := declaredBindings(e)
	if len(conflicts) > 0 {
		return fmt.Errorf("env %q binds one cluster to different connected clusters: %s.\n"+
			"fix: declare each cluster's connected cluster once (Bundle.connected_clusters)",
			envName, strings.Join(conflicts, "; "))
	}
	required := map[string]bool{}
	note := func(context string) {
		if context != "" {
			required[context] = true
		}
	}
	note(e.ClusterTarget.field("cluster"))
	for _, w := range e.Workloads {
		if w.Runtime.Type == RuntimeCluster && w.Runtime.Cluster != nil {
			note(w.Runtime.Cluster.Cluster)
		}
	}
	for _, m := range e.ManifestClusters {
		note(m.Cluster)
	}
	for _, h := range e.HelmCharts {
		note(h.Cluster)
	}
	for _, r := range e.RenderedSecrets {
		note(r.Cluster)
	}
	unbound := map[string]bool{}
	for context := range required {
		if len(bound[context]) == 0 && !isForgeManagedContext(context) {
			unbound[context] = true
		}
	}
	if len(unbound) == 0 {
		return nil
	}
	contexts := make([]string, 0, len(unbound))
	for c := range unbound {
		contexts = append(contexts, c)
	}
	sort.Strings(contexts)

	return fmt.Errorf("env %q declares a control plane and targets cluster %s, which names no "+
		"connected cluster.\n"+
		"The control plane would record this deploy with nowhere to send it: it holds no address, "+
		"CA or credential for a cluster it has not been told about.\n"+
		"fix: register the cluster once, then name it in the target:\n\n"+
		"    forge cluster connect <name> --context %s --env %s\n\n"+
		"    forge.ClusterTarget {\n        cluster = %q\n        namespace = \"...\"\n        connected_cluster = \"<name>\"\n    }\n\n"+
		"or, for a cluster only raw groups (Manifests/HelmChart/Generated) reach, once at cluster level:\n\n"+
		"    connected_clusters = {%q: \"<name>\"}    # on the Bundle\n\n"+
		"(a k3d cluster needs none of this — forge applies to it directly)",
		envName, strings.Join(quoteEach(contexts), ", "), contexts[0], envName, contexts[0], contexts[0])
}

// isForgeManagedContext reports whether a context is a local cluster forge
// creates and can delete. Mirrors kcl/render.k's _is_forge_managed_context
// prefix rule, which is the same signal gateway.k's _is_local_cluster uses.
func isForgeManagedContext(context string) bool {
	return strings.HasPrefix(context, "k3d-") || strings.HasPrefix(context, "kind-")
}

func quoteEach(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// refuseUndeliverableControlPlaneSecrets refuses, AT RENDER, a forge.ControlPlaneSecret
// the control plane has no way to deliver.
//
// Delivery is the control plane writing the Secret into the cluster through a
// connected-cluster credential, so it needs both: a control plane, and the
// connected cluster it writes through. A declaration missing either is
// recorded, never delivered, and reads as a Secret that is merely late.
//
// There is no k3d exemption, unlike refuseUnboundClusterTargets. That one
// exempts a local cluster because forge applies to it directly and the control
// plane never dials it. Delivery is exactly the control plane dialing the
// cluster, so a k3d cluster is deliverable only once it is connected (which is
// how this is tested), and an unconnected one is refused like any other.
//
// KCL refuses the same cases at the declaration (kcl/schema.k, Bundle checks).
// This is the Go side of the same rule for an entity set that did not come
// from that render, and it names the same fix.
func refuseUndeliverableControlPlaneSecrets(envName string, e *KCLEntities) error {
	if e == nil || len(e.ControlPlaneSecrets) == 0 {
		return nil
	}
	if e.ControlPlane == nil {
		return fmt.Errorf("env %q declares control-plane secrets (%s) and no control plane.\n"+
			"Nothing would mint, deliver or renew them.\n"+
			"fix: declare `control_plane = forge.ControlPlane {}` on the Bundle",
			envName, strings.Join(quoteEach(controlPlaneSecretNames(e.ControlPlaneSecrets)), ", "))
	}
	var unbound []ControlPlaneSecretEntity
	for _, c := range e.ControlPlaneSecrets {
		if strings.TrimSpace(c.ConnectedCluster) == "" {
			unbound = append(unbound, c)
		}
	}
	if len(unbound) == 0 {
		return nil
	}
	contexts := map[string]bool{}
	for _, c := range unbound {
		contexts[c.Cluster] = true
	}
	sorted := make([]string, 0, len(contexts))
	for c := range contexts {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)
	return fmt.Errorf("env %q declares control-plane secrets (%s) on cluster %s, which names no connected cluster.\n"+
		"The control plane can only write a Secret into a cluster it holds a credential for, so there is nothing to deliver it with.\n"+
		"fix: register the cluster once, then name it:\n\n"+
		"    forge cluster connect <name> --context %s --env %s\n\n"+
		"    connected_clusters = {%q: \"<name>\"}    # on the Bundle, or ClusterTarget.connected_cluster",
		envName, strings.Join(quoteEach(controlPlaneSecretNames(unbound)), ", "), strings.Join(quoteEach(sorted), ", "),
		sorted[0], envName, sorted[0])
}

func controlPlaneSecretNames(in []ControlPlaneSecretEntity) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}
