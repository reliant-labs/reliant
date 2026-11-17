package fluxdelivery

import rbacv1 "k8s.io/api/rbac/v1"

// The strings shared with kcl/lib/flux_delivery.k. TestKCLConstantsAgree pins
// every one against the KCL declaration of the same value.
const (
	// APIVersion is flux-operator's API group/version for ResourceSet and
	// ResourceSetInputProvider (FLUX_DELIVERY_API_VERSION).
	APIVersion = "fluxcd.controlplane.io/v1"
	// KindResourceSet is the ResourceSet kind.
	KindResourceSet = "ResourceSet"
	// ServiceAccount is the identity a delivery ResourceSet runs as, and the
	// name of its Role and RoleBinding (FLUX_DELIVERY_SA).
	ServiceAccount = "flux-delivery"
	// PruneAnnotation / PruneDisabled are flux-operator's per-object
	// garbage-collection opt-out (FLUX_DELIVERY_PRUNE_ANNOTATION /
	// FLUX_DELIVERY_PRUNE_DISABLED). flux-operator checks it against the LIVE
	// object, on garbage collection and on ResourceSet deletion alike.
	PruneAnnotation = "fluxcd.controlplane.io/prune"
	PruneDisabled   = "disabled"
	// RetireOrphan / RetireTeardown are the retire states an input may carry
	// (FLUX_DELIVERY_RETIRE_ORPHAN / FLUX_DELIVERY_RETIRE_TEARDOWN). See
	// [Input.Retire].
	RetireOrphan   = "orphan"
	RetireTeardown = "teardown"
	// InputLabel is the label a self-hosted ResourceSetInputProvider carries
	// so the delivery ResourceSet's inputsFrom selector finds it
	// (FLUX_DELIVERY_INPUT_LABEL).
	InputLabel = "forge.dev/delivery"
)

// The Flux identities the template renders, pinned to Flux v2.9.5 (source- and
// kustomize-controller v1.9.5) by the CRD-schema test.
const (
	SourceAPIVersion    = "source.toolkit.fluxcd.io/v1"
	KustomizeAPIVersion = "kustomize.toolkit.fluxcd.io/v1"
	KindOCIRepository   = "OCIRepository"
	KindKustomization   = "Kustomization"
	// KubeConfigKey is the key inside every destination kubeconfig Secret the
	// template names. control-plane's fluxhub writes the Secret with the same
	// key; a disagreement reads to kustomize-controller as a missing
	// kubeconfig.
	KubeConfigKey = "value"
	// ShardLabel and its two values. The template derives the value from
	// whether the applier names a kubeconfig, so the connected shard and the
	// kubeConfig field cannot disagree. Same spellings as forge/pkg/flux.
	ShardLabel     = "sharding.fluxcd.io/key"
	ShardMain      = "shard0"
	ShardConnected = "connected"
)

// Rules are the verbs the delivery ServiceAccount holds in its own namespace:
// the two Flux kinds the template renders, and nothing else. No Secret verb at
// all — `create` cannot be bounded by resourceNames, and a Secret `create` in a
// hub namespace mints a token for any ServiceAccount there. `delete` is the
// retire protocol and ResourceSet uninstall; the prune opt-out bounds it.
//
// kcl/lib/flux_delivery.k's flux_delivery_rbac() declares the same rules for a
// self-hosted bootstrap; the KCL agreement test pins the pair.
func Rules() []rbacv1.PolicyRule {
	verbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"source.toolkit.fluxcd.io"}, Resources: []string{"ocirepositories"}, Verbs: append([]string(nil), verbs...)},
		{APIGroups: []string{"kustomize.toolkit.fluxcd.io"}, Resources: []string{"kustomizations"}, Verbs: append([]string(nil), verbs...)},
	}
}
