package bundle

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestReaderHonoursScopedVulnExemptions(t *testing.T) {
	shape := release.Shape{DeliveryPolicy: &release.ShapeDeliveryPolicy{VulnGate: release.ShapeVulnGate{ScopedExemptions: []release.ShapeVulnScopedExempt{{ID: "CVE-2026-1234", Artifacts: []string{"api"}}}}}}
	reader := &release.BundleReader{Features: release.BundleDocFeatures()}
	if err := checkReaderHonours(reader, shape, nil); err == nil {
		t.Fatal("decode-only backend accepted scoped policy")
	}
	reader.Features = append(reader.Features, release.CapabilityEvaluatesScopedVulnExemptions)
	if err := checkReaderHonours(reader, shape, nil); err != nil {
		t.Fatal(err)
	}
}
