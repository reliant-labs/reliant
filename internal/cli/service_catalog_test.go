package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func TestBundleCollectsGeneratedServiceCatalog(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/shop"})
	file := filepath.Join(dir, servicecatalog.GeneratedPath)
	if err := os.WriteFile(file, raw, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := bundleObservabilityFiles(dir, bundleBuildInputs{StrictObservability: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range out {
		if f.Path == servicecatalog.BundlePath {
			found = true
			if string(f.Data) != string(raw) {
				t.Fatal("catalog was altered")
			}
		}
	}
	if !found {
		t.Fatal("catalog not attached to existing bundle")
	}
	// The bundle carries the reduced copy (servicecatalog.ForBundle): the
	// request/response schemas, not the generator's descriptor or the code
	// method sets.
	full, _ := json.Marshal(servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/shop",
		Services:   []servicecatalog.Service{{Name: "shop.v1.OrdersService", ProtoFile: "shop/v1/orders.proto", Descriptor: json.RawMessage(`{"Messages":{"A":[]},"Schemas":{"shop.v1.A":[]}}`)}},
		Components: []servicecatalog.Component{{ID: "Orders", Name: "Orders", Methods: []servicecatalog.CodeMethod{{Name: "Get", Signature: "func() error"}}}}})
	if err := os.WriteFile(file, full, 0644); err != nil {
		t.Fatal(err)
	}
	out, err = bundleObservabilityFiles(dir, bundleBuildInputs{StrictObservability: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out {
		if f.Path == servicecatalog.BundlePath {
			got := string(f.Data)
			if strings.Contains(got, "Messages") || strings.Contains(got, "func() error") || !strings.Contains(got, `"Schemas":{"shop.v1.A":[]}`) || !strings.Contains(got, servicecatalog.DiagnosticBundleMethodSetsOmitted) {
				t.Fatalf("bundle copy not reduced: %s", got)
			}
		}
	}
	if err := os.WriteFile(file, []byte(`{"schema":"bad"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleObservabilityFiles(dir, bundleBuildInputs{StrictObservability: true}, nil); err == nil {
		t.Fatal("invalid schema accepted")
	}
}
