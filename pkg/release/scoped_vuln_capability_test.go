package release

import (
	"slices"
	"testing"
)

func TestScopedVulnExemptionsRequireEvaluationCapability(t *testing.T) {
	s := Shape{DeliveryPolicy: &ShapeDeliveryPolicy{VulnGate: ShapeVulnGate{ScopedExemptions: []ShapeVulnScopedExempt{{ID: "CVE-2026-1234", Artifacts: []string{"api"}}}}}}
	for _, features := range [][]string{nil, BundleDocFeatures(), {CapabilityEvaluatesScopedVulnExemptions}} {
		r := &BundleReader{Features: features}
		if err := r.CheckScopedVulnExemptions(s); err == nil {
			t.Fatalf("reader %v accepted unsupported scoped policy", features)
		}
	}
	if slices.Contains(BundleDocFeatures(), CapabilityEvaluatesScopedVulnExemptions) {
		t.Fatal("decoding must not advertise evaluation")
	}
	for _, r := range []*BundleReader{nil, {Features: append(BundleDocFeatures(), CapabilityEvaluatesScopedVulnExemptions)}} {
		if err := r.CheckScopedVulnExemptions(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&BundleReader{}).CheckScopedVulnExemptions(Shape{}); err != nil {
		t.Fatal(err)
	}
	s.DeliveryPolicy.VulnGate.ScopedExemptions = nil
	s.DeliveryPolicy.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "CVE-2026-1234"}}
	if err := (&BundleReader{}).CheckScopedVulnExemptions(s); err != nil {
		t.Fatal(err)
	}
}
