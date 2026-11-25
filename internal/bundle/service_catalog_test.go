package bundle

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func TestServiceCatalogRidesVerifiedExistingLayer(t *testing.T) {
	c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/shop"}
	raw, _ := json.Marshal(c)
	built := mustBuild(t, withObservability(ObservabilityFile{Path: servicecatalog.BundlePath, Data: raw}))
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no layer")
	}
	if got := unpackLayer(t, layer)[servicecatalog.BundlePath]; got != string(raw) {
		t.Fatal("catalog bytes changed")
	}
	summary, err := release.ValidateManifestsLayer(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Artifacts) != 1 || summary.Artifacts[0] != servicecatalog.BundlePath {
		t.Fatalf("artifact missing: %+v", summary)
	}
	plain := mustBuild(t, buildFixture())
	if built.Doc.ConfigDigest != plain.Doc.ConfigDigest || built.Digest == plain.Digest {
		t.Fatal("metadata should affect bundle only, not deployed config")
	}
	if servicecatalog.BundlePath != release.BundleServiceCatalogPath {
		t.Fatal("bundle paths diverged")
	}
}
