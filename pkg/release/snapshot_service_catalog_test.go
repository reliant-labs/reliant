package release

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// The service catalog is one of a snapshot's assets: an env deploy's evidence
// snapshot carries every artifact its legacy bundle's layer would, the
// catalog included, and the layer check requires each to be an asset.
func TestSnapshotAdmitsTheServiceCatalogAsAnAsset(t *testing.T) {
	s, _ := managedWorkloadSnapshot(t)
	raw, err := json.Marshal(servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	s.Assets = []SnapshotAsset{{Path: BundleServiceCatalogPath, Data: raw, Digest: SpecDigest(raw)}}
	if err := s.Validate(); err != nil {
		t.Fatalf("a snapshot carrying the service catalog must validate: %v", err)
	}

	// The allowlist is still an allowlist.
	for _, path := range []string{"forge/other.json", "service-catalog.json", "forge/../forge/service-catalog.json"} {
		bad := s
		bad.Assets = []SnapshotAsset{{Path: path, Data: raw, Digest: SpecDigest(raw)}}
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "snapshot asset") {
			t.Errorf("asset %q: Validate = %v, want it refused", path, err)
		}
	}
	dup := s
	dup.Assets = append(append([]SnapshotAsset(nil), s.Assets...), s.Assets[0])
	if err := dup.Validate(); err == nil {
		t.Error("a duplicate service catalog asset was admitted")
	}
}
