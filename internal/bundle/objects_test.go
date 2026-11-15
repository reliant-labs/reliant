package bundle

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func fetchedOf(t *testing.T, b Bundle) Fetched {
	t.Helper()
	layer, ok := b.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("bundle has no manifest layer")
	}
	return Fetched{Digest: b.Digest, Doc: b.Doc, Manifests: layer}
}

// Every object the shape names is readable back from the layer under the SAME
// key, so a plan finding's subject finds its document.
func TestObjects_KeyedLikeTheShape(t *testing.T) {
	b := mustBuild(t, buildFixture())
	objects, err := Objects(fetchedOf(t, b))
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	if len(objects) != len(b.Doc.Shape.Objects) {
		t.Fatalf("layer objects = %d, shape objects = %d", len(objects), len(b.Doc.Shape.Objects))
	}
	for _, o := range b.Doc.Shape.Objects {
		if _, ok := objects[o.Key()]; !ok {
			t.Errorf("shape object %s has no document in the layer", o.Key())
		}
	}
}

// A changed field is named by its path; containers and env vars are named by
// their `name`, so inserting one does not mark every later one as changed.
func TestChangedFields_NamesPathsNotValues(t *testing.T) {
	live := map[string]any{"spec": map[string]any{
		"replicas": 2,
		"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "api", "image": "api@sha256:1", "env": []any{
				map[string]any{"name": "LOG_LEVEL", "value": "info"},
				map[string]any{"name": "DB_HOST", "value": "pg"},
			}},
		}}},
	}}
	cand := map[string]any{"spec": map[string]any{
		"replicas": 3,
		"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "migrate", "image": "api@sha256:1"},
			map[string]any{"name": "api", "image": "api@sha256:1", "env": []any{
				map[string]any{"name": "LOG_LEVEL", "value": "debug-with-a-secret-ish-value"},
				map[string]any{"name": "DB_HOST", "value": "pg"},
			}},
		}}},
	}}
	got := ChangedFields(live, cand)
	want := []string{
		"spec.replicas",
		"spec.template.spec.containers[api].env[LOG_LEVEL].value",
		"spec.template.spec.containers[migrate]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedFields = %v, want %v", got, want)
	}
	for _, p := range got {
		if strings.Contains(p, "debug") || strings.Contains(p, "info") {
			t.Fatalf("a path carries a value: %q", p)
		}
	}
	if got := ChangedFields(live, live); len(got) != 0 {
		t.Fatalf("identical documents differ in %v", got)
	}
}
