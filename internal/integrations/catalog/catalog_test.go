package catalog

import (
	"strings"
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

// The form lists params in the manifest's order (required first), so the
// order an author chose has to survive loading: Post message once rendered
// Blocks before Channel and Text because a Struct sorts its keys.
func TestParamOrderFollowsTheManifest(t *testing.T) {
	a, err := MustBuiltin().Resolve("slack/message.post@1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"channel", "text", "blocks", "thread_ts", "reply_broadcast", "unfurl_links", "connection"}
	if got := a.Spec.GetParamOrder(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("param_order = %v, want %v", got, want)
	}
	for _, m := range MustBuiltin().Manifests() {
		for _, act := range m.GetActions() {
			props, _ := act.GetParams().AsMap()["properties"].(map[string]any)
			if len(act.GetParamOrder()) != len(props) {
				t.Errorf("%s/%s: param_order %v does not list every param", m.GetId(), act.GetId(), act.GetParamOrder())
			}
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
