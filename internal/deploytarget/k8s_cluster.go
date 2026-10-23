package deploytarget

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/internal/cluster"
)

// K8sClusterProvider is the full Go implementation for the
// K8sCluster deploy target. It wraps internal/cluster.Apply — the
// existing render-KCL → kubectl-apply → wait-rollouts pipeline that
// `forge env deploy` / `forge cluster reload` / `forge env up` share.
//
// The provider takes the env-wide knobs off the ServiceGroup (which
// got them from the first K8sCluster ref in the group). The per-
// service knobs are reflected on the rendered manifests by KCL — this
// provider doesn't re-apply them, it just hands the right env / image
// tag / namespace to the cluster pipeline and lets the renderer do
// the rest.
type K8sClusterProvider struct {
	// ApplyOptsBuilder lets callers customize cluster.ApplyOpts before
	// the provider invokes cluster.Apply. The forge CLI uses this to
	// plumb through MainK, EnvConfigKV, Prune
	// from the rendered KCL — fields the provider itself doesn't know
	// about. A nil builder means "use the group's namespace+image tag
	// and let cluster.Apply default everything else", which is enough
	// for tests but not for the real forge env deploy path.
	ApplyOptsBuilder func(group ServiceGroup) cluster.ApplyOpts

	// Runner is the os/exec indirection used by OBSERVE ONLY, to shell
	// `kubectl get deployment -o json`. Nil falls back to the package
	// default.
	//
	// Deploy deliberately does NOT route through it: it delegates to
	// cluster.Apply, which owns its own execution, the KCL render and the
	// rollout wait. Threading this field into it would change deploy
	// behaviour, which is not what adding a read verb is for.
	Runner commandRunner
}

// runner returns the commandRunner Observe shells kubectl through.
func (p K8sClusterProvider) runner() commandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return defaultRunner
}

// declaredContext resolves the kubectl context for a group. The context is
// purely DECLARATIVE: the group's own declared cluster (group.Cluster, from
// KCL forge.K8sCluster.cluster, which IS the kubectl context name) is the
// only source. There is NO CLI override and NO fall-back to kubectl's
// current/active context, so a multi-cluster env routes each group to its
// own declared cluster. An empty result means the group carried no declared
// cluster.
func (p K8sClusterProvider) declaredContext(group ServiceGroup) string {
	return group.Cluster
}

// K8sClusterProviderID is the registry id of the cluster provider — the
// groups whose deploy is a `kubectl apply` against a cluster.
//
// Named rather than spelled as a bare literal at each site, the way
// HostedProviderID already is. There is no behavioural weight to it: the
// dispatcher looks a provider up by this id, and the string is unchanged.
const K8sClusterProviderID = "k8s-cluster"

// Name returns the provider identifier.
func (K8sClusterProvider) Name() string { return K8sClusterProviderID }

// Deploy invokes cluster.Apply for the group. The provider doesn't
// re-render KCL or re-walk services — that work is already done at
// the dispatcher layer; this just hands cluster.Apply the env-wide
// knobs (namespace, image tag) and lets it shell `kcl run` against
// the env's main.k.
//
// Deploy is ApplyNoWait followed by the rollout wait. A dispatcher deploying
// several cluster groups uses ApplyNoWait for each and waits once, so one
// cluster's rollout is never awaited before another cluster it depends on has
// been applied.
func (p K8sClusterProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	pending, err := p.ApplyNoWait(ctx, group)
	if err != nil {
		return err
	}
	return cluster.WaitRollouts(ctx, pending)
}

// ApplyNoWait applies the group to its cluster — render, select, scope,
// apply, and the pre-rollout Job gate — and returns the rollout still to be
// awaited, for the caller to hand to cluster.WaitRollouts together with every
// other cluster's. See cluster.ApplyNoWait for why the two halves are
// separable.
//
// Every error, and the pending rollout's own verdict, names the group's
// namespace and cluster: in a multi-cluster deploy that is the only thing
// that says which cluster a failure belongs to.
func (p K8sClusterProvider) ApplyNoWait(ctx context.Context, group ServiceGroup) (*cluster.PendingRollout, error) {
	if group.Namespace == "" {
		return nil, errors.New("k8s-cluster: ServiceGroup.Namespace is empty (forge.yaml or K8sCluster.namespace must declare it)")
	}
	var opts cluster.ApplyOpts
	if p.ApplyOptsBuilder != nil {
		opts = p.ApplyOptsBuilder(group)
	} else {
		// Fallback shape — tests that don't plumb a builder still get
		// a defensible default. The real forge env deploy path always
		// passes a builder.
		opts = cluster.ApplyOpts{
			ImageTag:  group.ImageTag,
			Namespace: group.Namespace,
		}
	}
	label := fmt.Sprintf("k8s-cluster deploy (ns=%s, cluster=%s)", group.Namespace, group.Cluster)
	pending, err := cluster.ApplyNoWait(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return pending.WithLabel(label), nil
}
