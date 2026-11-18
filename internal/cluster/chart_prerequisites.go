package cluster

import (
	"context"
	"strings"
)

// Without the secondary group's app ownership map, only explicit cluster
// routing and genuinely env-shared objects can be safely promoted there.
// App-owned, unrouted objects remain with their normal group apply.
func chartPrerequisitesForSecondary(manifests, kctx string) string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		route := m.Metadata.Labels[ClusterRoutingLabel]
		if route == kctx || (route == "" && ManifestGroup(m.Metadata.Labels) == "") {
			out = append(out, doc)
		}
	}
	return strings.Join(out, docDelimiter)
}

// clusterPrerequisiteKinds are the cluster-scoped kinds a chart's objects can
// name before anything in the chart exists, keyed by API group and kind. Each
// is a definition the apiserver serves by itself: it needs no controller, so it
// can land before the charts that install controllers.
//
//   - PriorityClass, RuntimeClass: admission rejects a pod naming a missing
//     one, so a controller Deployment never becomes Available.
//   - StorageClass: a claim naming a missing one stays Pending, and so does
//     the StatefulSet behind it.
//   - IngressClass: an Ingress naming a missing one is never admitted by its
//     controller.
//   - Namespace: a chart's object (or a manifest riding with it) may target a
//     namespace the env declares, with its pod-security labels already set.
//   - CustomResourceDefinition: a chart's Manifests may instantiate a kind
//     the env defines. Applied CRDs are waited to Established.
//
// Not here, on purpose: admission webhooks and APIServices (they route to a
// backend that is not running yet, so they would block the very charts that
// install it), RBAC (nothing is admitted against it), and every custom
// resource such as a GatewayClass or ClusterIssuer (its CRD and controller
// may be what the charts install).
var clusterPrerequisiteKinds = map[[2]string]bool{
	{"scheduling.k8s.io", "PriorityClass"}:               true,
	{"node.k8s.io", "RuntimeClass"}:                      true,
	{"storage.k8s.io", "StorageClass"}:                   true,
	{"networking.k8s.io", "IngressClass"}:                true,
	{"", "Namespace"}:                                    true,
	{"apiextensions.k8s.io", "CustomResourceDefinition"}: true,
}

// ChartClusterPrerequisites selects the declared cluster-scoped objects charts
// may reference (clusterPrerequisiteKinds), so they land before any chart's
// controllers are installed and waited on. A fresh cluster otherwise rejects
// a chart pod naming a PriorityClass the env's own stream declares, because
// that stream is applied after the charts. Callers scope the stream with
// ScopeManifestsToGroup first, and retain the original stream for normal
// reconciliation and pruning: the prerequisites are applied again there.
// Unrouted objects are shared, just as in the normal apply pipeline.
func ChartClusterPrerequisites(manifests string) string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || !clusterPrerequisiteKinds[[2]string{apiGroup(m.APIVersion), m.Kind}] {
			continue
		}
		out = append(out, doc)
	}
	return strings.Join(out, docDelimiter)
}

// ApplyClusterPrerequisites applies a ChartClusterPrerequisites stream to one
// context in a single pass, then waits for its CRDs to be Established, so a
// chart applied next can instantiate them. The shared early-batch primitive
// does both; none of these kinds is namespaced, so no default namespace.
func ApplyClusterPrerequisites(ctx context.Context, kctx, prerequisites string) error {
	crds, namespaces, rest := partitionEarlyBatch(prerequisites)
	return applyEarlyBatch(ctx, kctx, "", crds, joinNonEmpty(namespaces, rest))
}
