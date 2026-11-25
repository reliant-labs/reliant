package fluxdelivery_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// TestRenderMatchesTypedBuilderGoldens is the adoption-parity evidence on
// forge's side: the template, given the input the hub's converger would
// compute, renders the objects control-plane's typed builder
// (fluxtenant.BuildBundle) emits today — testdata/fluxtenant/, frozen copies of
// control-plane's bundle goldens as of control-plane 17c9f7e6.
//
// The comparison is exact apart from a stated delta, and the delta is the
// point of the test: it is what the first ResourceSet apply changes on a live
// object.
//
//   - METADATA (no generation bump, no Flux reconcile): the prune opt-out on
//     every object, the fence annotation (which the converger already stamps
//     on the source after BuildBundle, so it is no change there), and
//     flux-operator's owner labels.
//   - SPEC: `deletionPolicy: Orphan` on every Kustomization. This is a SPEC
//     field the typed builder never set, so adopting a live Kustomization
//     bumps its metadata.generation once and Flux reconciles the same bytes
//     once. To make adoption a true no-op the typed builder must start
//     emitting the same field before the ResourceSet takes over.
//
// control-plane's own TestResourceSetRenderEqualsTypedBuilder (C2) replaces
// this with the live builder; this frozen copy goes when that lands.
func TestRenderMatchesTypedBuilderGoldens(t *testing.T) {
	t.Parallel()
	for golden, in := range map[string]fluxdelivery.BindingInput{
		"bundle_flat_connected.golden.json": flatConnected(),
		"bundle_flat_hosted.golden.json":    flatHosted(),
		"bundle_trees_v2.golden.json":       treesV2(),
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "fluxtenant", golden))
		if err != nil {
			t.Fatal(err)
		}
		var typed struct {
			OCIRepository  map[string]any
			Kustomizations []map[string]any
		}
		if err := json.Unmarshal(raw, &typed); err != nil {
			t.Fatal(err)
		}
		want := append([]map[string]any{typed.OCIRepository}, typed.Kustomizations...)
		got := render(t, in)
		if len(got) != len(want) {
			t.Fatalf("%s: %d objects rendered, the typed builder emits %d", golden, len(got), len(want))
		}
		for i, o := range got {
			o = withoutAdoptionDelta(t, o)
			// Through JSON, so both sides compare as the API server stores
			// them.
			var norm map[string]any
			b, _ := json.Marshal(o.Object)
			_ = json.Unmarshal(b, &norm)
			if !reflect.DeepEqual(norm, want[i]) {
				gotJSON, _ := json.MarshalIndent(norm, "", "  ")
				wantJSON, _ := json.MarshalIndent(want[i], "", "  ")
				t.Errorf("%s object %d differs from the typed builder beyond the adoption delta:\n--- render ---\n%s\n--- typed ---\n%s",
					golden, i, gotJSON, wantJSON)
			}
		}
	}
}

// withoutAdoptionDelta removes exactly the delta TestRenderMatchesTypedBuilderGoldens
// states, and fails if any part of it is missing — the delta is asserted, not
// merely tolerated.
func withoutAdoptionDelta(t *testing.T, o *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	o = o.DeepCopy()
	annotations := o.GetAnnotations()
	if annotations[fluxdelivery.PruneAnnotation] != fluxdelivery.PruneDisabled {
		t.Errorf("%s %s lacks the prune opt-out", o.GetKind(), o.GetName())
	}
	delete(annotations, fluxdelivery.PruneAnnotation)
	if o.GetKind() == fluxdelivery.KindOCIRepository {
		if annotations[fluxdelivery.FenceAnnotation] == "" {
			t.Errorf("the source lacks the fence annotation")
		}
		delete(annotations, fluxdelivery.FenceAnnotation)
	}
	if len(annotations) > 0 {
		t.Errorf("%s %s carries annotations beyond the delta: %v", o.GetKind(), o.GetName(), annotations)
	}
	unstructured.RemoveNestedField(o.Object, "metadata", "annotations")
	labels := o.GetLabels()
	delete(labels, "resourceset.fluxcd.controlplane.io/name")
	delete(labels, "resourceset.fluxcd.controlplane.io/namespace")
	o.SetLabels(labels)
	if o.GetKind() == fluxdelivery.KindKustomization {
		if policy, _, _ := unstructured.NestedString(o.Object, "spec", "deletionPolicy"); policy != "Orphan" {
			t.Errorf("Kustomization %s deletionPolicy %q", o.GetName(), policy)
		}
		unstructured.RemoveNestedField(o.Object, "spec", "deletionPolicy")
	}
	return o
}
