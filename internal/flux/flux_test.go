package flux

import "testing"

// TestChartProvidedBy_ByReferenceNotName is the SHARED-FLUX RULE.
//
// Two Flux installs in one cluster collide on the same cluster-scoped CRDs and
// the same `flux-system`, and the collision is not clean: whichever applies
// last owns the CRDs and the other's Kustomizations reconcile against a
// controller replaced underneath them. Detection must therefore key off the
// CHART, not the chart's freely-chosen `name`.
func TestChartProvidedBy_ByReferenceNotName(t *testing.T) {
	t.Parallel()
	if !ChartProvidedBy(ChartOCI) {
		t.Fatal("the pinned chart reference is not recognised as providing Flux")
	}
	// Spellings of one reference.
	for _, ref := range []string{
		"ghcr.io/fluxcd-community/charts/flux2",
		"OCI://ghcr.io/fluxcd-community/charts/flux2",
		"oci://ghcr.io/fluxcd-community/charts/flux2/",
	} {
		if !ChartProvidedBy(ref) {
			t.Errorf("ChartProvidedBy(%q) = false; it is the same chart under a different spelling", ref)
		}
	}
	// A DIFFERENT chart does not provide Flux, however it is named — this
	// is the half that would be wrong under a name heuristic.
	for _, ref := range []string{
		"oci://docker.io/envoyproxy/gateway-helm",
		"https://charts.jetstack.io",
		"oci://ghcr.io/some-fork/charts/flux2",
		"",
	} {
		if ChartProvidedBy(ref) {
			t.Errorf("ChartProvidedBy(%q) = true; only the pinned chart provides Flux", ref)
		}
	}
}
