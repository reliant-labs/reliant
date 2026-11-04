package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// scopeRequiredSecretsToClusters decides, for each declared forge.ExternalSecret,
// which of the env's clusters it must exist in.
//
// ── Why this exists ──────────────────────────────────────────────────────
//
// A multi-cluster env does not need every Secret on every cluster. #251 made
// the preflight's cluster list the full set the dispatch applies to, which is
// right for the report's all_kube_contexts and wrong for the Secret check:
// control-plane's prod daemon cluster receives five PriorityClasses and a
// pre-pull DaemonSet, and nothing in control-plane-prod or cert-manager, yet
// every prod deploy was refused for lacking the app's eight Secrets there.
//
// ── The rule ─────────────────────────────────────────────────────────────
//
// A Secret is namespace-local, and a forge.ExternalSecret names no consumer
// and no cluster, so its namespace is the only handle there is. A Secret in
// namespace N is required on exactly the clusters that:
//
//  1. receive a rendered namespaced object in N. "Receive" is answered by
//     attributeRenderedObjects, i.e. by the deploy's own router
//     (cluster.ScopeManifestsToGroup), not by a second model of it. An object
//     with no namespace that is not a known cluster-scoped kind counts as the
//     deploy namespace;
//  2. or host a forge.HelmChart rendered into N (the chart's cluster, else the
//     env's primary). Chart output is rendered at apply time and never appears
//     in the KCL stream, so cert-manager's cloudflare-api-token would otherwise
//     look unconsumed.
//
// A Secret neither rule places is UNATTRIBUTABLE and keeps the conservative
// every-cluster check; it is returned separately so the caller can say so.
// Single-cluster envs are returned untouched — there is nothing to scope.
//
// manifests must be the FULL render, before any --target filter: a targeted
// deploy still checks every declared prerequisite, and attributing it against
// a filtered stream would drop the very consumers that place it.
func scopeRequiredSecretsToClusters(
	required []cluster.RequiredSecret,
	manifests string,
	groups []deploytarget.ServiceGroup,
	entities *KCLEntities,
	deployNamespace string,
) (scoped, unattributed []cluster.RequiredSecret) {
	if len(required) == 0 {
		return required, nil
	}
	objects, clusters := attributeRenderedObjects(manifests, groups, entities)
	if len(clusters) < 2 {
		return required, nil
	}

	consumers := map[string]map[string]struct{}{} // namespace -> clusters
	consume := func(namespace, kctx string) {
		if namespace == "" || kctx == "" {
			return
		}
		if consumers[namespace] == nil {
			consumers[namespace] = map[string]struct{}{}
		}
		consumers[namespace][kctx] = struct{}{}
	}
	for _, o := range objects {
		if clusterScopedKinds[o.Kind] {
			continue
		}
		namespace := o.Namespace
		if namespace == "" {
			namespace = deployNamespace
		}
		for _, c := range o.Clusters {
			consume(namespace, c)
		}
	}
	if entities != nil {
		primary := declaredEnvContext(entities, groups)
		for _, chart := range entities.HelmCharts {
			kctx := chart.Cluster
			if kctx == "" {
				kctx = primary
			}
			consume(chart.Namespace, kctx)
		}
	}

	scoped = make([]cluster.RequiredSecret, 0, len(required))
	for _, rs := range required {
		placed := consumers[rs.Namespace]
		if len(placed) == 0 {
			unattributed = append(unattributed, rs)
			scoped = append(scoped, rs)
			continue
		}
		rs.Contexts = make([]string, 0, len(placed))
		for c := range placed {
			rs.Contexts = append(rs.Contexts, c)
		}
		sort.Strings(rs.Contexts)
		scoped = append(scoped, rs)
	}
	return scoped, unattributed
}

// preflightDocumentContexts is the deploy's router as the preflight needs it:
// which of the env's clusters one rendered document is applied to, so a
// workload's Secret and ConfigMap references are checked on the cluster that
// workload runs on rather than on the env's primary. It is the same
// attribution `forge env render` prints in its `# cluster:` headers
// (clustersForDoc over cluster.ScopeManifestsToGroup), never a second model.
//
// Nil for an env with fewer than two clusters: there is nothing to route, and
// the preflight checks every reference on the deploy target.
//
// groups and entities must be the WHOLE env's topology, not a --target
// subset — a targeted deploy routes its objects by the same topology the
// untargeted one does.
func preflightDocumentContexts(groups []deploytarget.ServiceGroup, entities *KCLEntities) func(doc string) []string {
	clusters := envClusterOrder(groups, entities)
	if len(clusters) < 2 {
		return nil
	}
	scopes := envClusterScopes(groups, entities)
	return func(doc string) []string {
		return clustersForDoc(doc, clusters, scopes)
	}
}

// unattributedSecretsNote explains why a Secret is being required on every
// cluster, so a miss on a cluster that plainly does not use it reads as a
// declaration to fix rather than a forge bug. Empty when there is nothing to say.
func unattributedSecretsNote(unattributed []cluster.RequiredSecret) string {
	if len(unattributed) == 0 {
		return ""
	}
	names := make([]string, 0, len(unattributed))
	for _, rs := range unattributed {
		names = append(names, rs.Namespace+"/"+rs.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("preflight: no rendered object or forge.HelmChart in the namespace of %s, "+
		"so forge cannot tell which cluster consumes it and requires it on every cluster this env deploys to",
		strings.Join(names, ", "))
}

// clusterScopedKinds are the kinds that carry no namespace, so an empty
// metadata.namespace on one of them says nothing about the deploy namespace.
// Namespace itself is here on purpose: the router replicates an unlabelled
// Namespace to every cluster, and a Namespace is not a consumer of the Secrets
// inside it. An unlisted cluster-scoped kind only over-attributes (one extra,
// conservative check), never under-attributes.
var clusterScopedKinds = map[string]bool{
	"Namespace":                      true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"CustomResourceDefinition":       true,
	"RuntimeClass":                   true,
	"PriorityClass":                  true,
	"StorageClass":                   true,
	"IngressClass":                   true,
	"GatewayClass":                   true,
	"ClusterIssuer":                  true,
	"PersistentVolume":               true,
	"APIService":                     true,
	"CSIDriver":                      true,
	"ValidatingWebhookConfiguration": true,
	"MutatingWebhookConfiguration":   true,
}
