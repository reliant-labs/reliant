// External test package, so this test never makes observe's in-package tests
// import pkg/release: once pkg/release reached pkg/observe (through
// pkg/telemetry and pkg/dashspec), that was an import cycle. pkg/release no
// longer does (its TestImportGraphHasNoObserve), but nothing here depends on
// that staying true.
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
