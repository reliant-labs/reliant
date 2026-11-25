package fluxdelivery

// SELF-HOSTED DELIVERY: an env with `applier = "flux"` and no control plane,
// reconciled by a Flux forge bootstraps in the env's own cluster
// (kcl/lib/flux_delivery.k, "Self-hosted delivery"). The template and the input
// are the hub's; what differs is WHERE the objects live and AS WHOM each tree
// applies:
//
//   - each env's delivery lives in its own namespace ([SelfHostedNamespace]),
//     so two envs on one cluster are two bindings that share only the
//     controllers;
//   - every applier is IN-CLUSTER, tree or flat alike: the cluster Flux
//     applies to is the one it runs in, so there is no kubeconfig to name;
//   - a delivery with identities applies each tree as a derived identity:
//     [PreServiceAccount] for `pre` trees (the cluster-scoped kinds, RBAC and
//     config a release rests on), [ApplyServiceAccount] for rollout and post
//     trees. [NoneServiceAccount] is the controller's default, granted
//     nothing, so a Kustomization that names neither is Forbidden;
//   - a delivery without identities names none: kustomize-controller
//     applies as itself (cluster-admin), which is what the owner opted into.

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

// The names a self-hosted bootstrap and its writer share with
// kcl/lib/flux_delivery.k. TestKCLConstantsAgree pins each one.
const (
	// ChartName is the bootstrap platform chart's name, the `--target` that
	// installs it (FLUX_DELIVERY_CHART_NAME).
	ChartName = "flux-delivery"
	// PreServiceAccount, ApplyServiceAccount and NoneServiceAccount are the
	// apply identities in an env's delivery namespace (FLUX_DELIVERY_PRE_SA,
	// FLUX_DELIVERY_APPLY_SA, FLUX_DELIVERY_NONE_SA).
	PreServiceAccount   = "forge-delivery-pre"
	ApplyServiceAccount = "forge-delivery-apply"
	NoneServiceAccount  = "forge-delivery-none"
	// SelfHostedPullSecret is the copy of the registry pull Secret the
	// bootstrap writes into the delivery namespace; an input names it as
	// source.pullSecret (FLUX_DELIVERY_PULL_SECRET).
	SelfHostedPullSecret = "forge-delivery-pull"
	// EnvLabel carries the env on every self-hosted delivery object
	// (FLUX_DELIVERY_ENV_LABEL).
	EnvLabel = "forge.dev/delivery-env"
	// selfHostedNamespacePrefix starts every derived delivery namespace.
	selfHostedNamespacePrefix = "forge-delivery-"
)

// SelfHostedNamespace is an env's default delivery namespace:
// forge-delivery-<project>-<env>, the same composition as KCL's
// flux_delivery_namespace. A Bundle may name its own (FluxDelivery.namespace);
// the render reports the one in effect, and a writer reads that rather than
// recomposing it.
func SelfHostedNamespace(project, env string) string {
	return selfHostedNamespacePrefix + project + "-" + env
}

// SelfHostedServiceAccount is the identity a self-hosted tree (with identities) of
// phase applies as: [PreServiceAccount] for `pre`, [ApplyServiceAccount] for
// rollout and post.
func SelfHostedServiceAccount(phase release.Phase) string {
	if phase == release.PhasePre {
		return PreServiceAccount
	}
	return ApplyServiceAccount
}

// SelfHosted is how a self-hosted delivery applies, for [ValidateSelfHosted].
type SelfHosted struct {
	// ApplyIdentities is FluxDelivery.apply_identities: every tree names its derived
	// identity. False: no tree names one.
	ApplyIdentities bool
}

// ValidateSelfHosted is [Validate] for a self-hosted binding: every structural
// refusal of the hub, except that an in-cluster TREE is the norm here rather
// than a breach, plus the identity rules above. A self-hosted input that names
// a kubeconfig is refused: there is no other cluster to reach.
func ValidateSelfHosted(in BindingInput, mode SelfHosted) error {
	if err := validate(in, &mode); err != nil {
		return err
	}
	for _, a := range in.Appliers {
		if !a.InCluster || a.KubeConfigSecret != "" {
			return fmt.Errorf("%w: self-hosted applier %s names kubeConfigSecret %q: a self-hosted Flux applies only to the cluster it runs in",
				ErrNoClusterCredential, a.Name, a.KubeConfigSecret)
		}
		if a.Phase == "" {
			return fmt.Errorf("%w: self-hosted applier %s is a flat cluster; a self-hosted bundle is split into trees",
				ErrInvalidTree, a.Name)
		}
		want := ""
		if mode.ApplyIdentities {
			want = SelfHostedServiceAccount(a.Phase)
		}
		if a.ServiceAccountName != want {
			return fmt.Errorf("%w: self-hosted %s tree %s applies as %q, want %q (identities %t). A pre tree carries the "+
				"cluster-scoped kinds only %s is granted, and a tree under the wrong identity is Forbidden part-way through a release",
				ErrNoClusterCredential, a.Phase, a.Name, a.ServiceAccountName, want, mode.ApplyIdentities, PreServiceAccount)
		}
	}
	return nil
}
