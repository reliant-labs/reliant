package fluxdelivery_test

import (
	"encoding/json"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestInputProviderRoundTripsAndRenders pins the pointer's shape: a Static
// provider the bootstrap's selector finds, whose defaultValues reads back as
// the same input, and which renders — through the ResourceSet the bootstrap
// installs — into objects that pass the self-hosted checks.
func TestInputProviderRoundTripsAndRenders(t *testing.T) {
	t.Parallel()
	in := selfHosted(true)
	obj, err := fluxdelivery.InputProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: obj}
	if u.GetKind() != fluxdelivery.KindInputProvider || u.GetName() != fluxdelivery.InputProviderName(in.Env, "") || u.GetNamespace() != in.Namespace {
		t.Fatalf("pointer is %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
	}
	if got := u.GetLabels()[fluxdelivery.InputLabel]; got != "true" {
		t.Errorf("pointer label %s = %q, want \"true\" (the bootstrap ResourceSet selects on it)", fluxdelivery.InputLabel, got)
	}
	if typ, _, _ := unstructured.NestedString(obj, "spec", "type"); typ != fluxdelivery.InputProviderStatic {
		t.Errorf("spec.type = %q, want Static", typ)
	}
	if got := u.GetAnnotations()[fluxdelivery.PointerFenceAnnotation]; got != in.Fence {
		t.Errorf("fence annotation = %q, want %q", got, in.Fence)
	}

	back, err := fluxdelivery.LiveInput(u)
	if err != nil {
		t.Fatal(err)
	}
	wantMap, _ := in.Map()
	gotMap, _ := back.Map()
	if !equalJSON(t, wantMap, gotMap) {
		t.Errorf("LiveInput did not read the input back:\n got %v\nwant %v", gotMap, wantMap)
	}

	// The ResourceSet the bootstrap installs, rendered with this pointer as
	// its one provider.
	rs := map[string]any{
		"apiVersion": fluxdelivery.APIVersion,
		"kind":       fluxdelivery.KindResourceSet,
		"metadata":   map[string]any{"name": fluxdelivery.ResourceSetName(in.Env, ""), "namespace": in.Namespace},
		"spec": map[string]any{
			"serviceAccountName": fluxdelivery.ServiceAccount,
			"inputsFrom":         []any{map[string]any{"apiVersion": fluxdelivery.APIVersion, "kind": fluxdelivery.KindInputProvider}},
			"resourcesTemplate":  fluxdelivery.Template,
		},
	}
	values, _, _ := unstructured.NestedMap(obj, "spec", "defaultValues")
	objs, err := fluxdelivery.Render(rs, fluxdelivery.Provider{Name: u.GetName(), Inputs: []map[string]any{values}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fluxdelivery.CheckRenderedSelfHosted(in, objs, fluxdelivery.SelfHosted{ApplyIdentities: true}); err != nil {
		t.Fatalf("a rendered pointer fails the self-hosted checks: %v", err)
	}
}

func TestLiveInputRefusesAForeignInput(t *testing.T) {
	t.Parallel()
	obj, err := fluxdelivery.InputProvider(selfHosted(true))
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: obj}
	_ = unstructured.SetNestedField(u.Object, "forge.dev/flux-delivery/v0", "spec", "defaultValues", "schema")
	if _, err := fluxdelivery.LiveInput(u); !errors.Is(err, fluxdelivery.ErrInvalidInput) {
		t.Errorf("an older schema: err = %v, want ErrInvalidInput", err)
	}
	_ = unstructured.SetNestedField(u.Object, fluxdelivery.Schema, "spec", "defaultValues", "schema")
	_ = unstructured.SetNestedField(u.Object, "x", "spec", "defaultValues", "unknownKey")
	if _, err := fluxdelivery.LiveInput(u); !errors.Is(err, fluxdelivery.ErrInvalidInput) {
		t.Errorf("an unknown key: err = %v, want ErrInvalidInput", err)
	}
}

// TestCheckTransition is the writer's refusal matrix.
func TestCheckTransition(t *testing.T) {
	t.Parallel()
	live := selfHosted(true)
	live.Fence = "7"

	withFence := func(in fluxdelivery.BindingInput, fence string) fluxdelivery.BindingInput {
		in.Fence = fence
		return in
	}
	dropPost := func(in fluxdelivery.BindingInput) fluxdelivery.BindingInput {
		in.Appliers = append([]fluxdelivery.Applier(nil), in.Appliers[:2]...)
		return in
	}
	retirePost := func(in fluxdelivery.BindingInput) fluxdelivery.BindingInput {
		in.Appliers = append([]fluxdelivery.Applier(nil), in.Appliers...)
		in.Appliers[2].Retire = fluxdelivery.RetireOrphan
		return in
	}
	movePath := func(in fluxdelivery.BindingInput) fluxdelivery.BindingInput {
		in.Appliers = append([]fluxdelivery.Applier(nil), in.Appliers...)
		in.Appliers[1].Path = release.BundleTreePath("other", release.DefaultDeliveryUnit, release.PhaseRollout)
		return in
	}
	retiredLive := retirePost(live)

	for _, tc := range []struct {
		name string
		live *fluxdelivery.BindingInput
		next fluxdelivery.BindingInput
		want error
	}{
		{"first write", nil, withFence(live, "1"), nil},
		{"same fence, new digest", &live, withFence(live, "7"), nil},
		{"newer fence", &live, withFence(live, "8"), nil},
		{"older fence", &live, withFence(live, "6"), fluxdelivery.ErrStaleFence},
		{"unreadable live fence", ptr(withFence(live, "abc")), withFence(live, "8"), fluxdelivery.ErrStaleFence},
		{"unreadable next fence", &live, withFence(live, ""), fluxdelivery.ErrInvalidInput},
		{"drop an un-retired applier", &live, withFence(dropPost(live), "8"), fluxdelivery.ErrUnretiredApplierDropped},
		{"retire, then drop", &retiredLive, withFence(dropPost(live), "8"), nil},
		{"path moved under one name", &live, withFence(movePath(live), "8"), fluxdelivery.ErrPathChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fluxdelivery.CheckTransition(tc.live, tc.next)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func equalJSON(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ja) == string(jb)
}
