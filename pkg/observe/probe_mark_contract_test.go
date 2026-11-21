// External test package: pkg/release reaches pkg/observe (through
// pkg/telemetry, pkg/dashspec and pkg/alerting), so an in-package test of
// observe that imports release is an import cycle.
package observe_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestProbeAttrIsTheReleaseContract(t *testing.T) {
	if observe.AttrForgeProbe != release.ProbeAttr {
		t.Fatalf("attrForgeProbe = %q, release.ProbeAttr = %q: readers would count marked probes as real traffic", observe.AttrForgeProbe, release.ProbeAttr)
	}
}
