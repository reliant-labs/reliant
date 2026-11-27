package bundle

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// A sealed snapshot rebuilds its shape from the recorded declaration. A
// declaration recorded before release probes existed rebuilds byte for byte
// (no `release_probes` key, so the same hash), and one that carries probes
// keeps them: dropping them here would silently stop probing an env on
// every snapshot rebuild.
func TestSnapshotShapeInputCarriesReleaseProbes(t *testing.T) {
	probes := []release.ShapeReleaseProbe{{
		Target: "web", Kind: release.ReleaseProbeKindFrontend, Paths: []string{"/"}, Method: "GET",
		RatePerMin: 4, TimeoutSeconds: 10, Source: release.ReleaseProbeSourceDefault,
	}}
	for name, decl := range map[string]release.Shape{
		"recorded before the field": {Kind: release.EnvPersistent},
		"with release probes":       {Kind: release.EnvPersistent, ReleaseProbes: probes},
	} {
		t.Run(name, func(t *testing.T) {
			want, err := decl.Canonical().Encode()
			if err != nil {
				t.Fatal(err)
			}
			in, err := snapshotShapeInput(release.Snapshot{Declaration: &decl})
			if err != nil {
				t.Fatal(err)
			}
			got, err := in.shapeWith(nil).Encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("rebuilt shape differs from the recorded declaration:\n got %s\nwant %s", got, want)
			}
			if has := strings.Contains(string(got), `"release_probes"`); has != (decl.ReleaseProbes != nil) {
				t.Errorf("release_probes present = %v in %s", has, got)
			}
		})
	}
}
