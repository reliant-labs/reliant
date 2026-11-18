package release

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// A service catalog grows with the project it describes (reliant's is ~3 MB),
// and it is data, never applied: the per-manifest bound must not refuse it,
// and the snapshot reader must hand it back whole rather than truncated.
func TestServiceCatalogArtifactIsBoundedByTheLayerNotTheManifestBound(t *testing.T) {
	c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/big",
		Processes: []servicecatalog.Process{}, Services: []servicecatalog.Service{}, Bindings: []servicecatalog.Binding{},
		Clients: []servicecatalog.Client{}, Diagnostics: []servicecatalog.Diagnostic{}}
	for i := 0; len(c.Components) < 4000; i++ {
		c.Components = append(c.Components, servicecatalog.Component{ID: "c", Name: "c", Kind: "service", Type: strings.Repeat("t", 400),
			Methods: []servicecatalog.CodeMethod{}, ImplementationCandidates: []servicecatalog.Implementation{}})
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= ManifestsLayerMaxFileBytes {
		t.Fatalf("fixture is %d bytes; it must exceed the %d-byte manifest bound", len(raw), ManifestsLayerMaxFileBytes)
	}
	layer := buildLayer(t,
		layerEntry{Name: "manifests/hub/000-ns.yaml", Body: manifestYAML("Namespace", "app")},
		layerEntry{Name: BundleServiceCatalogPath, Body: string(raw)})
	if _, err := ValidateManifestsLayer(strings.NewReader(string(layer))); err != nil {
		t.Fatalf("a %d-byte catalog must validate: %v", len(raw), err)
	}
	assets, err := ReadSnapshotAssets(layer)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Path != BundleServiceCatalogPath || len(assets[0].Data) != len(raw) {
		t.Fatalf("snapshot assets = %d, want the whole %d-byte catalog", len(assets), len(raw))
	}
}
