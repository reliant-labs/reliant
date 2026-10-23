package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// ── The `--target` vocabulary and the targeted deploy's topology ─────────────
//
// `forge env render --list` attributes every rendered object to a GROUP
// (cluster.ManifestGroup) and a set of clusters (the deploy's own router,
// cluster.ScopeManifestsToGroup). `--target` must accept exactly the groups
// `--list` prints and must ship each selected object to the clusters `--list`
// says it lands on. Two separate facts were drifting from that:
//
//  1. VOCABULARY. validateDeployTargets knew only ENTITY names (services,
//     operators, frontends, charts). A group that exists only as rendered
//     objects — a Bundle's `additional_manifests` stamped with their own app
//     name (control-plane dev's `openbao`, `workspace-proxy-bridge`,
//     `fake-gcs`) — was printed by `--list` and refused by `--target`.
//
//  2. TOPOLOGY. The deploy built its groups, and from them the multi-cluster
//     scope, from the TARGET-FILTERED entities. A targeted deploy therefore
//     saw only the clusters its targets happen to own: `--target
//     prod-daemon-cluster` left one cluster, so scoping switched off (a single
//     cluster needs none) and the Secret preflight attributed every declared
//     Secret to the one cluster it could see. And a manifest-only target owns
//     no entity at all, so it produced no group and fell through to the
//     unscoped direct apply against the env's primary context — a `fake-gcs`
//     that `--list` routes to cp-daemon would have been applied to the hub.
//
// The fix separates the two concerns. The ROUTING topology is always the whole
// env's (buildDeployGroupsForEnv over the unfiltered entities); `--target`
// only SELECTS which objects ship, and the clusters that receive them follow
// from the topology rather than from which entities survived the filter.

// validateTargetsAgainstRender checks targets against the env's full
// `--target` vocabulary: every deployable entity name plus every group the
// rendered stream attributes an object to. manifests must be the FULL render
// (before any --target filter).
func validateTargetsAgainstRender(e *KCLEntities, targets []string, manifests string) error {
	return validateDeployTargets(e, targets, cluster.ManifestGroups(manifests)...)
}

// targetedK8sGroups returns the k8s-cluster groups a TARGETED deploy applies:
// one per cluster that at least one selected object lands on, each keeping the
// full-env group's coordinates (namespace, registry) so its apply is scoped by
// the full-env GroupScope exactly as `forge env render --list` attributes it.
//
// Non-k8s groups (external / compose / host-infra / firebase / static-site) are
// not routed by manifest and are taken from targetedGroups unchanged — the
// entity filter already narrowed them to the named apps.
//
// selected is the target-filtered stream; fullGroups the whole env's groups;
// targetedGroups the groups of the target-filtered entities. A selection that
// lands nowhere (every selected entity is non-k8s) yields only the non-k8s
// groups.
func targetedK8sGroups(selected string, fullGroups, targetedGroups []deploytarget.ServiceGroup, entities *KCLEntities) []deploytarget.ServiceGroup {
	var out []deploytarget.ServiceGroup
	for _, g := range targetedGroups {
		if g.ProviderID != "k8s-cluster" {
			out = append(out, g)
		}
	}

	// The clusters the selection lands on, as the render attributes them.
	objects, _ := attributeRenderedObjects(selected, fullGroups, entities)
	lands := map[string]bool{}
	for _, o := range objects {
		for _, c := range o.Clusters {
			lands[c] = true
		}
	}
	// A targeted operator / cronjob / chart owns no service group of its own
	// yet must still ride some cluster's apply; keep any k8s group the
	// entity filter selected even if no rendered object attributes to it
	// (a chart-only target renders its objects at apply time, not in KCL).
	for _, g := range targetedGroups {
		if g.ProviderID == "k8s-cluster" && g.Cluster != "" {
			lands[g.Cluster] = true
		}
	}

	seen := map[string]bool{}
	for _, g := range fullGroups {
		if g.ProviderID != "k8s-cluster" || !lands[g.Cluster] || seen[g.Cluster] {
			continue
		}
		seen[g.Cluster] = true
		out = append(out, g)
	}
	return out
}

// clustersDeployedTo lists, sorted, the kubectl contexts a deploy's k8s groups
// apply to — the clusters it WRITES. The Secret preflight checks each declared
// Secret only on the intersection of this set and the clusters that consume it.
func clustersDeployedTo(groups []deploytarget.ServiceGroup) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" || g.Cluster == "" || seen[g.Cluster] {
			continue
		}
		seen[g.Cluster] = true
		out = append(out, g.Cluster)
	}
	sort.Strings(out)
	return out
}

// restrictSecretsToDeployedClusters narrows each attributed Secret's contexts
// to the clusters this deploy writes. A Secret that no written cluster
// consumes is dropped from the check entirely: a deploy that touches only the
// daemon cluster has no business requiring the hub's IdP credentials.
//
// An UNATTRIBUTED Secret (no consumer found — Contexts empty) keeps the
// conservative check, narrowed to the written clusters: forge cannot say it is
// unused, but it can say it is not this deploy's to break. deployed empty
// (no k8s group) returns the input unchanged.
func restrictSecretsToDeployedClusters(required []cluster.RequiredSecret, deployed []string) []cluster.RequiredSecret {
	if len(deployed) == 0 {
		return required
	}
	writes := map[string]bool{}
	for _, c := range deployed {
		writes[c] = true
	}
	out := make([]cluster.RequiredSecret, 0, len(required))
	for _, rs := range required {
		if len(rs.Contexts) == 0 {
			rs.Contexts = append([]string(nil), deployed...)
			out = append(out, rs)
			continue
		}
		var keep []string
		for _, c := range rs.Contexts {
			if writes[c] {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			continue
		}
		rs.Contexts = keep
		out = append(out, rs)
	}
	return out
}

// describeTargetVocabulary renders the sorted `--target` vocabulary for an
// error message, one line, with the manifest-only groups marked so the reader
// can tell an app from a bundle of raw manifests.
func describeTargetVocabulary(entityNames, manifestOnly []string) string {
	names := append([]string(nil), entityNames...)
	for _, g := range manifestOnly {
		names = append(names, g+" (manifests)")
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// errUnknownTargets formats the typo error. Kept beside the vocabulary it
// reports so the two cannot describe different sets.
func errUnknownTargets(unknown, entityNames, manifestOnly []string) error {
	return fmt.Errorf("unknown --target %s; available in env: %s",
		strings.Join(unknown, ", "), describeTargetVocabulary(entityNames, manifestOnly))
}
