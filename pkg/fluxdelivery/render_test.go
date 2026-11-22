package fluxdelivery_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.yaml from the current render")

// render builds the binding's ResourceSet the way a hub writer does and
// renders it in-process.
func render(t *testing.T, in fluxdelivery.BindingInput) []*unstructured.Unstructured {
	t.Helper()
	rs, err := fluxdelivery.ResourceSet(in)
	if err != nil {
		t.Fatalf("ResourceSet: %v", err)
	}
	objs, err := fluxdelivery.Render(rs)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return objs
}

func marshalObjects(t *testing.T, objs []*unstructured.Unstructured) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, o := range objs {
		y, err := yaml.Marshal(o.Object)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("---\n")
		b.Write(y)
	}
	return b.Bytes()
}

// TestRenderGoldens: every fixture's render, whole objects, against its
// golden. A golden is the claim "these are the bytes the cluster is asked to
// hold", so a field added anywhere is a diff to review.
func TestRenderGoldens(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			got := marshalObjects(t, render(t, fx.in()))
			path := filepath.Join("testdata", "golden", fx.name+".yaml")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s (run with -update to create it): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from its golden; run `go test ./pkg/fluxdelivery -run TestRenderGoldens -update` and review the diff.\n--- got ---\n%s", path, got)
			}
		})
	}
}

// TestFixturesValidate: every fixture but the binding one is an input the
// writer may write, so a golden never pins a render of a refused input.
func TestFixturesValidate(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		err := fluxdelivery.Validate(fx.in())
		switch {
		case fx.validates && err != nil:
			t.Errorf("%s: Validate: %v", fx.name, err)
		case !fx.validates && !errors.Is(err, fluxdelivery.ErrBindingUnsupported):
			t.Errorf("%s: Validate = %v, want ErrBindingUnsupported", fx.name, err)
		}
	}
}

// TestRenderedSafetyProperties holds every fixture's render to CheckRendered
// — the prune opt-out, deletionPolicy, suspend, shard/kubeConfig agreement,
// digest pin, verify, and nothing rendered but the binding's own objects.
func TestRenderedSafetyProperties(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		in := fx.in()
		if err := fluxdelivery.CheckRendered(in, render(t, in)); err != nil {
			t.Errorf("%s: %v", fx.name, err)
		}
	}
}

// TestCheckRenderedCatchesAnUnsafeRender proves the property check is not
// vacuous: each mutation of a good render is one the template must never
// produce, and each is refused.
func TestCheckRenderedCatchesAnUnsafeRender(t *testing.T) {
	t.Parallel()
	in := treesV2()
	for name, mutate := range map[string]func(objs []*unstructured.Unstructured) []*unstructured.Unstructured{
		"prune opt-out lost": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			a := objs[1].GetAnnotations()
			delete(a, fluxdelivery.PruneAnnotation)
			objs[1].SetAnnotations(a)
			return objs
		},
		"deletionPolicy mirrors prune": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			_ = unstructured.SetNestedField(objs[2].Object, "MirrorPrune", "spec", "deletionPolicy")
			return objs
		},
		"applier dropped": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			return objs[:len(objs)-1]
		},
		"a Secret rendered": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			s := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret"}}
			s.SetName("registry-pull")
			s.SetNamespace(fxNS)
			return append(objs, s)
		},
		"shard disagrees with kubeConfig": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			l := objs[1].GetLabels()
			l[fluxdelivery.ShardLabel] = fluxdelivery.ShardMain
			objs[1].SetLabels(l)
			return objs
		},
		"source moved to a tag": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			_ = unstructured.SetNestedField(objs[0].Object, "latest", "spec", "ref", "tag")
			return objs
		},
		"path moved": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			_ = unstructured.SetNestedField(objs[3].Object, "manifests/hub", "spec", "path")
			return objs
		},
		"suspended without a reason": func(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
			_ = unstructured.SetNestedField(objs[4].Object, true, "spec", "suspend")
			return objs
		},
	} {
		objs := mutate(render(t, in))
		if err := fluxdelivery.CheckRendered(in, objs); !errors.Is(err, fluxdelivery.ErrUnsafeRender) {
			t.Errorf("%s: CheckRendered = %v, want ErrUnsafeRender", name, err)
		}
	}
}

// TestRenderSchemaGate: an input of any other schema stalls the ResourceSet
// (the template fails) instead of misrendering.
func TestRenderSchemaGate(t *testing.T) {
	t.Parallel()
	in := flatConnected()
	in.Schema = "forge.dev/flux-delivery/v2"
	rs, err := fluxdelivery.ResourceSet(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fluxdelivery.Render(rs); err == nil || !strings.Contains(err.Error(), "forge.dev/flux-delivery/v2") {
		t.Fatalf("Render of a v2 input = %v, want the template's schema refusal", err)
	}
}

// TestRenderMissingKeyStalls: missingkey=error, as flux-operator runs it — an
// input missing a key the template reads is a failed render, never a field
// rendered as "<no value>".
func TestRenderMissingKeyStalls(t *testing.T) {
	t.Parallel()
	rs, err := fluxdelivery.ResourceSet(flatConnected())
	if err != nil {
		t.Fatal(err)
	}
	input := rs["spec"].(map[string]any)["inputs"].([]any)[0].(map[string]any)
	delete(input, "fence")
	if _, err := fluxdelivery.Render(rs); err == nil {
		t.Fatal("Render of an input without fence succeeded; it must fail as flux-operator does")
	}
}

// TestRenderSelfHostedProviderMatchesInline: the self-hosted shape — a
// ResourceSet selecting a ResourceSetInputProvider — renders the same objects
// as the hub's inline shape, apart from the owner labels naming the
// ResourceSet. One template, two input routes.
func TestRenderSelfHostedProviderMatchesInline(t *testing.T) {
	t.Parallel()
	in := flatConnected()
	inline := render(t, in)
	values, err := in.Map()
	if err != nil {
		t.Fatal(err)
	}
	selfHosted := map[string]any{
		"apiVersion": fluxdelivery.APIVersion,
		"kind":       fluxdelivery.KindResourceSet,
		"metadata":   map[string]any{"name": "forge-delivery", "namespace": fxNS},
		"spec": map[string]any{
			"serviceAccountName": fluxdelivery.ServiceAccount,
			"inputsFrom": []any{map[string]any{
				"apiVersion": fluxdelivery.APIVersion, "kind": "ResourceSetInputProvider",
				"selector": map[string]any{"matchLabels": map[string]any{fluxdelivery.InputLabel: "true"}},
			}},
			"resourcesTemplate": fluxdelivery.Template,
		},
	}
	provided, err := fluxdelivery.Render(selfHosted, fluxdelivery.Provider{Name: "delivery-env-prod", Inputs: []map[string]any{values}})
	if err != nil {
		t.Fatal(err)
	}
	if len(provided) != len(inline) {
		t.Fatalf("provider render has %d objects, inline %d", len(provided), len(inline))
	}
	for i := range inline {
		a, b := inline[i].DeepCopy(), provided[i].DeepCopy()
		for _, o := range []*unstructured.Unstructured{a, b} {
			l := o.GetLabels()
			delete(l, "resourceset.fluxcd.controlplane.io/name")
			o.SetLabels(l)
		}
		if !reflect.DeepEqual(a.Object, b.Object) {
			t.Errorf("object %d differs between the inline and the provider route:\ninline   %v\nprovider %v", i, a.Object, b.Object)
		}
	}
	// No provider inputs: flux-operator reconciles an EMPTY set.
	if objs, err := fluxdelivery.Render(selfHosted); err != nil || len(objs) != 0 {
		t.Errorf("Render with no provider inputs = %d objects, %v; want none", len(objs), err)
	}
}

// TestMapIsCompleteAndCanonical: every key the template reads is present,
// with empty collections rather than null, whatever the Go value left nil.
func TestMapIsCompleteAndCanonical(t *testing.T) {
	t.Parallel()
	in := flatConnected()
	in.Labels = nil
	in.Appliers[0].Labels = nil
	m, err := in.Map()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := yaml.Marshal(m)
	if strings.Contains(string(raw), "null") {
		t.Errorf("Map renders null:\n%s", raw)
	}
	a := m["appliers"].([]any)[0].(map[string]any)
	for _, key := range []string{"dependsOn", "externalDependsOn", "health", "healthExprs", "patches", "commonLabels", "labels", "retire", "suspended"} {
		if _, ok := a[key]; !ok {
			t.Errorf("applier map lacks %q", key)
		}
	}
}
