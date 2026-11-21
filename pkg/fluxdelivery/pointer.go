package fluxdelivery

// THE SELF-HOSTED POINTER: the one object a `forge env deploy` of an
// `applier = "flux"` env writes to ship a release.
//
// The bootstrap installs the delivery ResourceSet with `inputsFrom` selecting
// [InputLabel]. The pointer is the ResourceSetInputProvider of type Static that
// selector finds, in the env's delivery namespace: its `defaultValues` is the
// env's [BindingInput], so flux-operator renders the release's OCIRepository
// and Kustomizations from it. Keeping the input in its own object, rather than
// inline on the ResourceSet, separates the TEMPLATE (a bootstrap object, read
// by `forge env render`) from the POINTER (a per-deploy object): re-applying the
// bootstrap cannot clobber the pointer, and a deployer needs write access to
// this one kind in one namespace, not to the ResourceSet.
//
// [CheckTransition] is the writer's half of the safety the template cannot
// enforce on its own: comparing the live input with the next one. It is shared
// with the hub's converger, which compares the same two inputs on its
// ResourceSet.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The self-hosted pointer's kind and the annotations a writer reads back.
const (
	// KindInputProvider is flux-operator's ResourceSetInputProvider.
	KindInputProvider = "ResourceSetInputProvider"
	// InputProviderStatic is the provider type whose one exported input is
	// its `spec.defaultValues`.
	InputProviderStatic = "Static"
	// PointerFenceAnnotation carries the writer's fence on the pointer, as
	// decimal digits: the live pointer's fence plus one, allocated by the
	// write that is conditional on that pointer, so the sequence is the
	// cluster's and shared by every deployer. A write that would move it
	// backwards is refused ([ErrStaleFence]); the self-hosted writer also
	// refuses one that reuses it for another promotion.
	PointerFenceAnnotation = "forge.dev/delivery-fence"
	// PointerPromotionAnnotation names the ledger promotion the pointer
	// projects.
	PointerPromotionAnnotation = "forge.dev/delivery-promotion"
	// GrantsRecord is the ConfigMap in a self-hosted delivery namespace that
	// records what the bootstrap granted the apply identities, so a deploy
	// can compare a release's needs with it before it writes anything.
	GrantsRecord = "forge-delivery-grants"
)

// The writer's refusals on a transition from the live input to the next.
var (
	// ErrUnretiredApplierDropped is a write that drops an applier the live
	// input holds without having retired it first. The dropped
	// Kustomization would leave the ResourceSet's inventory still live and
	// still reading the shared source, and when the source moves to a digest
	// without its path, Flux prunes everything it applied.
	ErrUnretiredApplierDropped = errors.New("fluxdelivery: the write drops an applier that was never retired")
	// ErrPathChanged is a write that keeps an applier's name and changes its
	// path. The name is a function of the same coordinates the path is, so
	// this can only be a bug, and a Kustomization whose path moves prunes
	// everything the old path applied.
	ErrPathChanged = errors.New("fluxdelivery: an applier kept its name and changed its path")
	// ErrStaleFence is a write whose fence is older than the live one: a
	// writer that lost a race would move the environment backwards.
	ErrStaleFence = errors.New("fluxdelivery: the write's fence is older than the live input's")
)

// InputProviderName is the name of a binding's pointer: the same as its
// ResourceSet's, in its own kind.
func InputProviderName(env, binding string) string { return ResourceSetName(env, binding) }

// InputProvider is the pointer object for in: a Static
// ResourceSetInputProvider in in.Namespace, labelled so the delivery
// ResourceSet selects it, with in as its `defaultValues` and the fence and
// promotion as annotations.
//
// It validates nothing: call [ValidateSelfHosted] first. It refuses only an
// input it cannot encode.
func InputProvider(in BindingInput) (map[string]any, error) {
	values, err := in.Map()
	if err != nil {
		return nil, err
	}
	if in.Namespace == "" || in.Env == "" {
		return nil, fmt.Errorf("%w: a pointer needs the input's env and namespace", ErrInvalidInput)
	}
	labels := resourceSetLabels()
	labels[InputLabel] = "true"
	labels[EnvLabel] = in.Env
	return map[string]any{
		"apiVersion": APIVersion,
		"kind":       KindInputProvider,
		"metadata": map[string]any{
			"name":      InputProviderName(in.Env, in.Binding),
			"namespace": in.Namespace,
			"labels":    labels,
			"annotations": map[string]any{
				PointerFenceAnnotation:     in.Fence,
				PointerPromotionAnnotation: in.Promotion,
			},
		},
		"spec": map[string]any{
			"type":          InputProviderStatic,
			"defaultValues": values,
		},
	}, nil
}

// LiveInput reads the input a live pointer carries back into a
// [BindingInput]. A pointer whose defaultValues does not decode as one (an
// older schema, a hand edit) is an error: a writer must not compare against an
// input it cannot read.
func LiveInput(pointer *unstructured.Unstructured) (BindingInput, error) {
	raw, found, err := unstructured.NestedMap(pointer.Object, "spec", "defaultValues")
	if err != nil || !found {
		return BindingInput{}, fmt.Errorf("%w: pointer %s/%s has no spec.defaultValues", ErrInvalidInput, pointer.GetNamespace(), pointer.GetName())
	}
	in, err := inputFromMap(raw)
	if err != nil {
		return BindingInput{}, fmt.Errorf("pointer %s/%s: %w", pointer.GetNamespace(), pointer.GetName(), err)
	}
	if in.Schema != Schema {
		return BindingInput{}, fmt.Errorf("%w: pointer %s/%s carries input schema %q, not %q", ErrInvalidInput,
			pointer.GetNamespace(), pointer.GetName(), in.Schema, Schema)
	}
	return in, nil
}

// CheckTransition refuses a write of next over live (nil: nothing is live, a
// first write). The three rules, each the writer's alone to enforce:
//
//   - the fence never moves backwards ([ErrStaleFence]);
//   - every applier the live input holds is in next, unless the live input
//     had already retired it (that applier, or the whole binding) — the
//     first of the two writes that drop one ([ErrUnretiredApplierDropped]);
//   - an applier present in both keeps its path ([ErrPathChanged]).
//
// It does not check that a live retire has been APPLIED before the drop: that
// is an observation of the renderer, which the caller makes.
func CheckTransition(live *BindingInput, next BindingInput) error {
	if live == nil {
		return nil
	}
	nextFence, err := fenceOf(next.Fence)
	if err != nil {
		return fmt.Errorf("%w: the write's fence %q is not a sequence number", ErrInvalidInput, next.Fence)
	}
	liveFence, err := fenceOf(live.Fence)
	if err != nil {
		return fmt.Errorf("%w: the live input's fence %q is not a sequence number, so this write cannot prove it is newer",
			ErrStaleFence, live.Fence)
	}
	if nextFence < liveFence {
		return fmt.Errorf("%w: the live input is at fence %d (promotion %s) and this write is at %d (promotion %s)",
			ErrStaleFence, liveFence, live.Promotion, nextFence, next.Promotion)
	}
	nextPaths := make(map[string]string, len(next.Appliers))
	for _, a := range next.Appliers {
		nextPaths[a.Name] = a.Path
	}
	for _, a := range live.Appliers {
		path, kept := nextPaths[a.Name]
		switch {
		case !kept && live.Retire == "" && a.Retire == "":
			return fmt.Errorf("%w: %s (path %s). Retire it first (appliers[].retire: orphan), wait for the renderer to apply that, then drop it",
				ErrUnretiredApplierDropped, a.Name, a.Path)
		case kept && path != a.Path:
			return fmt.Errorf("%w: %s from %s to %s", ErrPathChanged, a.Name, a.Path, path)
		}
	}
	return nil
}

func fenceOf(s string) (uint64, error) { return strconv.ParseUint(s, 10, 64) }

// inputFromMap is [BindingInput.Map] reversed, strictly: a key the input does
// not declare is an input from some other writer, not this one's.
func inputFromMap(m map[string]any) (BindingInput, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return BindingInput{}, fmt.Errorf("%w: encode the live input: %v", ErrInvalidInput, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in BindingInput
	if err := dec.Decode(&in); err != nil {
		return BindingInput{}, fmt.Errorf("%w: the live input does not decode: %v", ErrInvalidInput, err)
	}
	return in, nil
}
