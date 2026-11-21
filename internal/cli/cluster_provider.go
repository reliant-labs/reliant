// Package cli — the cluster-provisioning provider registry.
//
// This mirrors internal/deploytarget's Provider/Registry shape
// (deploytarget.go), one level earlier in the pipeline: deploytarget
// dispatches a rendered SERVICE to the pipeline that ships it somewhere
// (k8s-cluster / external / compose / host-infra); this dispatches a
// declared CLUSTER to the pipeline that ensures it exists in the first
// place, before any service can be deployed into it.
package cli

import (
	"context"
	"fmt"
)

// ClusterProvider ensures ONE declared cluster exists and is healthy
// enough to receive a deploy. It is the cluster-provisioning analogue of
// deploytarget.Provider — see internal/deploytarget/deploytarget.go —
// which plays the same dispatch role one layer up, per SERVICE rather
// than per CLUSTER.
//
// Deliberately ONE method. Every current caller (reconcileDeclaredClusters,
// prepareDeployCluster) asks exactly one question — "does this cluster
// exist and is it ready" — and the create/start/heal distinction k3d needs
// internally is an implementation detail of ITS provider, not a decision
// any caller of the registry makes. Adding a second method here (e.g. a
// separate Start or a Teardown) would be modelling k3d's own ordering in
// the interface rather than what the registry's callers need; per the
// project's package-boundary rule, a six-method interface is a concrete
// type in disguise, and a two-method one is already worth scrutinizing.
//
// CONTEXT-MINTING ORDERING — the reason Ensure carries this contract
// rather than just "create the cluster":
//
// verifyDeclaredContextsExist (deploy.go) is the guard that refuses a
// deploy whose declared kubectl context isn't present in the local
// kubeconfig — the mechanism that makes a wrong-cluster deploy impossible.
// It runs in the DEPLOY phase, strictly after the CLUSTER phase
// (reconcileDeclaredClusters) has already returned — see
// upBuildDeployPhases (up.go) and prepareDeployCluster→
// resolveDeployKubectlContext (deploy.go), both of which call the cluster
// phase before ever building the deploy groups the context guard reads.
//
// For k3d this ordering is invisible because k3d mints the context as a
// side effect of the SAME command that creates the cluster:
// createK3dCluster's `k3d cluster create` writes the `k3d-<name>` context
// into the default kubeconfig, and mergeK3dKubeconfigFn repairs that merge
// on the hung-client recovery path. By the time reconcileDeclaredClusters
// returns, `.context` already resolves — so the ordering guarantee below
// has always held for k3d, just never had to be written down because
// nothing violated it.
//
// A non-k3d provider does NOT get this for free. A vcluster's kubeconfig
// context exists only after `vcluster connect` (or equivalent) writes it;
// a freshly-created GKE cluster's context exists only after `gcloud
// container clusters get-credentials` does the same. If a future
// ClusterProvider deferred that mint to the deploy phase, or skipped it,
// verifyDeclaredContextsExist would still fire — correctly, the context
// really is missing — but the operator reads a kubeconfig error at deploy
// time for what is actually an incomplete cluster-provisioning step. That
// is a strictly worse failure to debug than the same problem surfacing
// where it happened.
//
// So the contract every ClusterProvider.Ensure implementation MUST
// uphold: by the time Ensure returns nil, entity.Context (the kubectl
// context string forge derived for this cluster) is resolvable in the
// LOCAL kubeconfig. Ensure runs entirely inside the cluster phase, which
// always completes before the deploy phase's context guard runs — so
// upholding the contract here is what keeps a missing-context failure
// attributable to cluster provisioning, not to deploy.
type ClusterProvider interface {
	// Ensure creates the entity's cluster if absent, starts it if stopped,
	// and heals known-flaky runtime state if already running — then mints
	// (or verifies) its kubectl context per the ordering contract above.
	// declared is the full cluster list for this env, passed through
	// because ordering across DECLARED clusters matters (a secondary must
	// be ensured after the owner it nests on; see isNestedSecondary).
	Ensure(ctx context.Context, entity ClusterEntity, declared []ClusterEntity, projectDir, env string) error
}

// clusterProviderRegistry maps a ClusterEntity.Provider string (the KCL
// Cluster.provider discriminator, kcl/schema.k) to the ClusterProvider
// that implements it.
var clusterProviderRegistry = map[string]ClusterProvider{
	"k3d":      k3dClusterProvider{},
	"vcluster": notImplementedClusterProvider{provider: "vcluster"},
	"gke":      notImplementedClusterProvider{provider: "gke"},
}

// lookupClusterProvider resolves one entity's declared provider. Called
// PER-ENTITY by reconcileDeclaredClusters (not once for the whole
// process) so an env that eventually mixes providers — a k3d dev cluster
// alongside a declared-but-unimplemented gke one — dispatches each to its
// own provider rather than colliding on shared dispatch state.
//
// An empty Provider is treated as "k3d", matching the KCL schema's own
// default (kcl/schema.k Cluster.provider). This also keeps a
// hand-constructed ClusterEntity{Name: "x"} — every existing test literal
// — resolving to k3d exactly as it always has.
func lookupClusterProvider(providerID string) (ClusterProvider, error) {
	if providerID == "" {
		providerID = "k3d"
	}
	p, ok := clusterProviderRegistry[providerID]
	if !ok {
		return nil, fmt.Errorf(
			"cluster provider %q is not registered (want one of: k3d, vcluster, gke)", providerID)
	}
	return p, nil
}

// notImplementedClusterProvider backs a Cluster.provider value the KCL
// schema accepts — so a project can declare its target topology (e.g.
// "this will be a vcluster") before the provider exists — but that forge
// cannot yet reconcile. It refuses loudly and immediately, rather than
// silently no-op'ing or quietly falling back to k3d, either of which
// would leave a deploy landing somewhere the operator never declared.
type notImplementedClusterProvider struct {
	provider string
}

func (p notImplementedClusterProvider) Ensure(_ context.Context, entity ClusterEntity, _ []ClusterEntity, _, _ string) error {
	return fmt.Errorf(
		"cluster %q declares provider=%q, which forge does not implement yet — only provider=\"k3d\" is ensured today",
		entity.Name, p.provider)
}

// k3dClusterProvider is the ClusterProvider wrapping today's k3d
// reconcile — the same create-if-absent / start-if-stopped / heal-if-
// running pipeline this package has always run, now reached through the
// registry instead of being the reconcile loop's only behavior. All of
// its test-substitution seams (clusterRuntimeStateFn, createDeclaredClusterFn,
// etc., cluster_phase.go) are unchanged and stay k3d-scoped: they are
// invoked only from the k3d path below, so a future provider gains no
// entanglement with them merely by existing in the same registry.
type k3dClusterProvider struct{}

func (k3dClusterProvider) Ensure(ctx context.Context, entity ClusterEntity, declared []ClusterEntity, projectDir, env string) error {
	return ensureDeclaredCluster(ctx, entity, declared, projectDir, env)
}
