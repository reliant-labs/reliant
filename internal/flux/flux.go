// Package flux holds the identity of the Flux chart forge pins.
//
// forge does not install Flux into a deploy target and writes no pointer at
// one: a real cluster is reconciled by a control plane's hub, or applied by
// forge directly (Bundle.applier = "forge", Bundle.lifecycle). What survives of
// the old in-cluster reconciler is the chart's identity, because a consumer
// that needs Flux for its own reasons (control-plane's e2e runs one to reconcile
// hosted workloads) declares `forge.flux_chart()`, and an env's bootstrap-chart install
// must recognise it by REFERENCE, never by name, so a second copy is never
// installed beside it. The values below are the Go half of kcl/lib/flux.k.
package flux

import "strings"

// The chart forge installs, and the one a consumer declares to be recognised
// as already providing Flux.
//
// THESE TWO STRINGS ARE SHARED WITH KCL and must not drift:
// `kcl/lib/flux.k`'s FLUX_CHART_OCI / FLUX_CHART_VERSION are the same values,
// and kcl/tests/positive_flux_chart.k pins them against the literals. The
// reference is the SHARED-FLUX DETECTOR — [ChartProvidedBy] compares a
// declared chart's `oci` against ChartOCI — so a divergence here would make a
// consumer's Flux undetectable and forge would install a second one into a
// cluster that already had one.
const (
	// ChartOCI is the community flux2 chart. There is no fluxcd-published
	// chart; this is the canonical install path.
	ChartOCI = "oci://ghcr.io/fluxcd-community/charts/flux2"
	// ChartVersion is the pinned CHART version. Chart 2.19.1 is appVersion
	// v2.9.5 (source-controller and kustomize-controller v1.9.5). The two
	// numbers are unrelated lineages — re-derive with
	// `helm show chart <ChartOCI> --version <v> | grep appVersion` on a
	// bump, never increment.
	ChartVersion = "2.19.1"
	// ChartName is the `--target` selector forge's own install uses. A
	// consumer may name its chart anything, which is exactly why detection
	// is by reference and not by this.
	ChartName = "flux"
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
