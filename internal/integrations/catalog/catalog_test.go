package catalog

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// TestLoadAllEmbeddedManifests is the build-time guard: every embedded
// manifest must decode strictly and pass load-time validation.
func TestLoadAllEmbeddedManifests(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatalf("embedded catalog failed to load: %v", err)
	}
	if len(c.Manifests()) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	for _, m := range c.Manifests() {
		if err := manifest.Validate(m, manifest.TrustCurated); err != nil {
			t.Errorf("%s: %v", m.GetId(), err)
		}
	}
}

func TestResolve(t *testing.T) {
	c := MustBuiltin()
	a, err := c.Resolve("http/request@1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.GetPlacement() != manifest.PlacementServer || !a.Spec.GetMutates() || !a.Spec.GetTool().GetExpose() {
		t.Errorf("unexpected http/request spec: %v", a.Spec)
	}
	for _, bad := range []string{"http/request", "http/request@2", "http/nope@1", "nope/request@1", "http/request@0", "tool/generate_image"} {
		if _, err := c.Resolve(bad); err == nil {
			t.Errorf("%q must not resolve", bad)
		}
	}
}
