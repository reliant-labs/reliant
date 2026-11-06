package cli

// SECRETS A BUNDLE NAMES BUT NEVER CARRIES.
//
// A bundle redacts every Secret value (F-13). A Secret in the manifest layer
// was actively harmful: a reconciler applied the redaction marker over the
// cluster's real value, and with prune on it owned the object's lifetime. So a
// Secret is not in the layer at all (bundle.splitSecrets); the bundle's
// document NAMES the ones the env requires.
//
// This file is the naming half: which Secrets an env needs supplied, and the
// refusal for a hub-reconciled env that needs a plain value forge cannot
// deliver. A hub-reconciled (ControlPlane) env holds no context for the
// clusters its Flux applies to, so it cannot write there. See
// [refuseUnsyncableHostedSecrets].

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// envSyncedSecretRefs is the NAMES of the Secrets an env needs forge to sync:
// the render's own `kind: Secret` documents plus the ones forge projects from
// the env's secret store (declared rendered Secrets, a value-resolving
// provider's secret_refs). No values.
func envSyncedSecretRefs(entities *KCLEntities, groups []deploytarget.ServiceGroup, namespace, manifests string) ([]release.BundleSecretRef, error) {
	var refs []release.BundleSecretRef
	docs, err := bundle.SecretDocuments(manifests)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		clusters := d.Clusters
		if len(clusters) == 0 {
			clusters = []string{""}
		}
		for _, c := range clusters {
			refs = append(refs, release.BundleSecretRef{Cluster: c, Namespace: d.Namespace, Name: d.Name})
		}
	}
	for _, p := range placeDeclaredSecrets(entities, groups, namespace) {
		for _, s := range p.secrets {
			refs = append(refs, release.BundleSecretRef{Cluster: p.cluster, Namespace: p.namespace, Name: s.Name})
		}
	}
	if providerResolvesValues(entities) {
		for _, p := range placeProviderSecretRefs(entities, groups, namespace, "") {
			seen := map[string]bool{}
			for _, r := range p.refs {
				if r.SecretName != "" && !seen[r.SecretName] {
					seen[r.SecretName] = true
					refs = append(refs, release.BundleSecretRef{Cluster: p.cluster, Namespace: p.namespace, Name: r.SecretName})
				}
			}
		}
	}
	// A Secret the External Secrets Operator delivers is not one forge syncs:
	// the bundle names the ExternalSecret and ESO writes the Secret.
	return withoutStoreBacked(refs, entities), nil
}

// hostedTierSecretRefs narrows refs to the Secrets the HOSTED tier of an env
// needs supplied: what its own objects consume, and anything placed on no
// cluster this machine applies.
//
// A pure-hosted env applies nothing locally, so every ref stays. A MIXED env's
// cluster tiers are applied from this machine, which syncs their Secrets with
// the apply (applyK8sSecretsFromProvider); a Secret placed on one of those
// clusters is the cluster tier's, and the hosted tier is not refused for it
// unless one of its own workloads names it (a hosted workload runs where the
// platform puts it, so a cluster-tier copy does not reach it). A hosted
// StaticSite consumes no Secret, so it needs none. control-plane's prod shipped
// every cluster tier and then had its hosted reliant-web refused for three
// hub Secrets the site never reads (forge bug 382bc13a).
func hostedTierSecretRefs(entities *KCLEntities, hostedGroups []deploytarget.ServiceGroup, refs []release.BundleSecretRef) []release.BundleSecretRef {
	if entities == nil || !envAppliesLocally(entities) {
		return refs
	}
	local := map[string]bool{}
	for _, mc := range entities.ManifestClusters {
		local[mc.Cluster] = true
	}
	if c := entities.ClusterTarget.field("cluster"); c != "" {
		local[c] = true
	}
	delete(local, deploytarget.HostedProviderID)
	consumed := map[string]bool{}
	for _, g := range hostedGroups {
		for name := range referencedSecretNamesForGroup(entities, g) {
			consumed[name] = true
		}
	}
	var out []release.BundleSecretRef
	for _, r := range refs {
		if r.Cluster != "" && local[r.Cluster] && !consumed[r.Name] {
			continue
		}
		out = append(out, r)
	}
	return out
}

func providerResolvesValues(entities *KCLEntities) bool {
	if entities == nil || entities.SecretProvider == nil || entities.SecretProvider.Type == "rendered" {
		return false
	}
	prov, err := secretProviderFromEntities(entities, projectDirForKCL())
	return err == nil && prov.Kind() == "file"
}

func secretRefLabel(ref release.BundleSecretRef) string {
	if ref.Namespace == "" {
		return ref.Name
	}
	return ref.Namespace + "/" + ref.Name
}

// refuseUnsyncableHostedSecrets stops a hub-reconciled deploy that would ship
// without Secrets its workloads need.
//
// The hub's Flux applies to a cluster forge holds no kubectl context for, so
// forge cannot sync a value there, and a Secret is not in the bundle to carry
// one. Provider-backed secrets (ExternalSecrets / OpenBao references) are
// non-Secret objects and ride the bundle as references, needing nothing. A
// plain store value has no transport on this path, so say so rather than
// deploy a workload that starts without it.
func refuseUnsyncableHostedSecrets(env string, refs []release.BundleSecretRef) error {
	if len(refs) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, r := range refs {
		label := secretRefLabel(r)
		if !seen[label] {
			seen[label] = true
			names = append(names, label)
		}
	}
	sort.Strings(names)
	return fmt.Errorf(
		"env %s is reconciled by its control plane, and it needs Secret value(s) forge cannot deliver: %s\n"+
			"  A bundle never carries a Secret, and forge holds no kubectl context for the clusters the control plane's\n"+
			"  reconciler applies to. Either:\n"+
			"    - back them with a store: give the forge.ExternalSecret a `source = forge.StoreSource {...}` (the\n"+
			"      External Secrets Operator delivers it; set the value with `forge secret set`), which rides the\n"+
			"      bundle as a non-secret object; or\n"+
			"    - run `forge env secrets sync %s` against each cluster yourself before deploying",
		env, strings.Join(names, ", "), env)
}
