// Package flux holds the identity of the Flux install forge pins — ControlPlane's
// Flux Operator chart and the FluxInstance riding it — and the names a writer
// of Flux objects needs to agree with `forge.flux_chart()`: the per-namespace
// ClusterRoles, the tenant ServiceAccount, and the connected-cluster shard
// (the sharding label, its value, the shard's controller and the ClusterRole a
// namespace binds it to).
//
// It is public so a consumer that writes Kustomizations or RoleBindings for a
// hub (control-plane does) imports the spellings instead of restating them.
// Every string here is also spelled in kcl/lib/flux.k; TestKCLNamesAgree pins
// the pair.
//
// forge does not install Flux into a deploy target and writes no pointer at
// one: a real cluster is reconciled by a control plane's hub, or applied by
// forge directly (Bundle.applier = "forge", Bundle.lifecycle). A consumer that
// needs Flux for its own reasons declares `forge.flux_chart()`, and an env's
// bootstrap-chart install must recognise it by REFERENCE, never by name, so a
// second copy is never installed beside it.
package flux

import "strings"

// The chart forge installs, and the one a consumer declares to be recognised
// as already providing Flux.
//
// THESE STRINGS ARE SHARED WITH KCL and must not drift: `kcl/lib/flux.k`'s
// FLUX_CHART_OCI / FLUX_CHART_VERSION / FLUX_CHART_NAME / FLUX_VERSION /
// FLUX_INSTANCE_NAME are the same values, pinned by TestKCLNamesAgree. The
// reference is the SHARED-FLUX DETECTOR — [ChartProvidedBy] compares a
// declared chart's `oci` against ChartOCI — so a divergence here would make a
// consumer's Flux undetectable and forge would install a second one into a
// cluster that already had one.
const (
	// ChartOCI is ControlPlane's Flux Operator chart. The operator installs
	// Flux itself from the FluxInstance riding the chart.
	ChartOCI = "oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator"
	// ChartVersion is the pinned operator chart version (appVersion
	// v<ChartVersion>; the image is pinned by digest in kcl/lib/flux.k).
	ChartVersion = "0.62.1"
	// FluxVersion is the Flux minor the FluxInstance pins; the operator picks
	// the newest patch of it that its pinned image embeds.
	FluxVersion = "2.9.x"
	// InstanceName is the FluxInstance's name: the operator reconciles only
	// `flux`, in its own namespace.
	InstanceName = "flux"
	// ChartName is the `--target` selector forge's own install uses. A
	// consumer may name its chart anything, which is exactly why detection
	// is by reference and not by this.
	ChartName = "flux-operator"
	// Namespace is Flux's namespace, and where the pointer is written. Not
	// configurable: a second spelling would only let the writer and the
	// controller disagree about where the OCIRepository lives.
	Namespace = "flux-system"
	// ReconcilerRole / SourceRole are the ClusterRoles of the
	// per-namespace RBAC model (kcl/lib/flux.k, `rbac = "namespaced"`): they
	// grant nothing until a RoleBinding in a namespace names them. A writer
	// of a namespace that holds Flux objects binds kustomize-controller to
	// the first and source-controller to the second. Same names as KCL's
	// FLUX_RECONCILER_ROLE / FLUX_SOURCE_ROLE, pinned by
	// kcl/tests/positive_flux_chart_hardened.k and TestKCLRoleNamesAgree: a
	// RoleBinding naming a ClusterRole that is not declared is accepted by
	// the apiserver and grants nothing, so drift would fail late.
	ReconcilerRole = "flux-tenant-reconciler"
	SourceRole     = "flux-tenant-source"
	// DeployServiceAccount is the ServiceAccount the controller impersonates
	// by default and is pinned to.
	DeployServiceAccount = "reliant-deploy-tenant"
	// ShardLabel / ConnectedShard are Flux's sharding label and the value that
	// routes a Kustomization to the connected-cluster controller
	// (`flux_chart(connected_shard = True)`, a FluxInstance shard). A writer of a Kustomization with
	// spec.kubeConfig MUST set it: the admission policy refuses the object
	// otherwise. Same spellings as KCL's FLUX_SHARD_LABEL / FLUX_CONNECTED_SHARD.
	ShardLabel     = "sharding.fluxcd.io/key"
	ConnectedShard = "connected"
	// ConnectedController is the shard's Deployment and ServiceAccount;
	// ConnectedReconcilerRole is the ClusterRole a namespace RoleBinding gives
	// it (Secret reads only, never impersonate).
	ConnectedController     = "kustomize-controller-connected"
	ConnectedReconcilerRole = "flux-connected-reconciler"
)

// ChartProvidedBy reports whether any of the chart references given already
// provides Flux — the SHARED-FLUX RULE.
//
// DETECTION IS BY CHART REFERENCE, NEVER BY NAME, and the distinction is what
// keeps this correct. A HelmChart's `name` is the `--target` selector its
// author picks freely: control-plane calls its hosted Flux "flux", another
// consumer might call it "flux-system" or "gitops", and a name heuristic would
// then decide whether forge installed a SECOND Flux into a cluster that
// already had one. Two Flux installs in one cluster collide on the same
// cluster-scoped CRDs and the same `flux-system` namespace, and the collision
// is not a clean failure: whichever applied last owns the CRDs, and the
// other's Kustomizations reconcile against a controller that was replaced
// underneath them.
//
// The comparison ignores an `oci://` prefix and a trailing slash, because
// those are spellings of one reference rather than different charts.
func ChartProvidedBy(refs ...string) bool {
	for _, ref := range refs {
		if sameChartRef(ref, ChartOCI) {
			return true
		}
	}
	return false
}

func sameChartRef(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		s = strings.TrimPrefix(s, "oci://")
		return strings.TrimSuffix(s, "/")
	}
	a, b = norm(a), norm(b)
	return a != "" && a == b
}
