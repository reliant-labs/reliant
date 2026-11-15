package cluster

import (
	"strings"
	"testing"
)

// TestMissingCRDHintDoesNotRecommendExperimentalChannel pins the remediation
// text forge prints when a rendered kind has no CRD on the target cluster.
//
// The hint used to name experimental-install.yaml. That is wrong for every
// kind forge actually renders, and wrong in the direction that costs the most
// to discover: the standard channel carries GA HTTPRoute, GRPCRoute, Gateway
// (including spec.allowedListeners) and ListenerSet, and it is what
// internal/templates/ingress/envoy/VERSION pins and what a cloud cluster runs
// (verified on GKE: channel=standard, bundle-version=v1.6.2). An operator who
// followed the hint would install the experimental bundle on their local
// cluster and make dev DIVERGE from prod while "fixing" a missing CRD —
// exactly the drift the pinned bundle exists to prevent.
//
// Asserted on the rendered string rather than on a constant so the test fails
// if the URL is reintroduced anywhere in this block.
func TestMissingCRDHintDoesNotRecommendExperimentalChannel(t *testing.T) {
	hint := FormatPreflightReport(PreflightResult{
		MissingCRDs: []string{`Gateway (gateway.networking.k8s.io/v1) — required by "public"`},
	})

	if strings.Contains(hint, "experimental-install.yaml") {
		t.Errorf("missing-CRD hint still recommends the experimental channel:\n%s", hint)
	}
	if !strings.Contains(hint, "standard-install.yaml") {
		t.Errorf("missing-CRD hint should point at the standard channel:\n%s", hint)
	}
}
