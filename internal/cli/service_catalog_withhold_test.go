package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// A control plane whose layer validator predates the catalog refuses the
// whole bundle, so a reader that does not name the feature never gets it.
func TestServiceCatalogIsWithheldFromAReaderThatDoesNotNameIt(t *testing.T) {
	files := []bundle.ObservabilityFile{
		{Path: "observability/metrics-schema.json", Data: []byte("{}")},
		{Path: servicecatalog.BundlePath, Data: []byte("{}")},
	}
	for name, tc := range map[string]struct {
		reader   *release.BundleReader
		withheld bool
	}{
		// The claimable path: built against the machine ledger, recorded on a
		// control plane whose capabilities were never read.
		"no control plane asked (nil reader, claimable)": {nil, true},
		"control plane naming it":                        {&release.BundleReader{Features: release.BundleDocFeatures()}, false},
		"control plane predating it":                     {&release.BundleReader{Features: []string{release.BundleFeatureUptimeChecks}}, true},
		"control plane with no RPC":                      {&release.BundleReader{}, true},
	} {
		got, withheld := withholdServiceCatalog(files, tc.reader)
		if withheld != tc.withheld {
			t.Errorf("%s: withheld = %v, want %v", name, withheld, tc.withheld)
		}
		want := len(files)
		if tc.withheld {
			want--
		}
		if len(got) != want {
			t.Errorf("%s: %d files, want %d", name, len(got), want)
		}
		for _, f := range got {
			if tc.withheld && f.Path == servicecatalog.BundlePath {
				t.Errorf("%s: the catalog was sent", name)
			}
		}
	}
	if _, withheld := withholdServiceCatalog(files[:1], &release.BundleReader{}); withheld {
		t.Error("a bundle with no catalog reports nothing withheld")
	}
	// A composition bundle never carries it: its assets ride the 1 MiB config blob.
	if got, dropped := dropServiceCatalog(files); !dropped || len(got) != 1 || got[0].Path == servicecatalog.BundlePath {
		t.Errorf("dropServiceCatalog = %v, %v; want the catalog dropped", got, dropped)
	}
}

// The claimable path builds its bundle against the machine ledger (no control
// plane is asked what it decodes) and then records those bytes on the hosted
// control plane. A prod control plane whose layer validator predates the
// catalog refused that bundle whole, so a bundle built with no reader must not
// carry it.
func TestABundleBuiltWithNoControlPlaneReaderWithholdsTheCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("writes the on-disk release ledger for real; runs in task test")
	}
	dir := newLedgerTestProject(t, "catalog-claimable")
	stubEnvShape(t, "catalog-claimable")
	raw, _ := json.Marshal(servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/shop"})
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(servicecatalog.GeneratedPath)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalogWithheldNoted.Delete("prod")
	var errOut bytes.Buffer
	if _, err := writeEnvBundle(context.Background(), dir, "prod", bundleBuildInputs{
		Release: "v1", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: &errOut,
	}); err != nil {
		t.Fatalf("write the bundle: %v", err)
	}
	if !strings.Contains(errOut.String(), "the service catalog was not uploaded") {
		t.Fatalf("a bundle built with no control-plane reader carried the catalog (no withheld note):\n%s", errOut.String())
	}
}
