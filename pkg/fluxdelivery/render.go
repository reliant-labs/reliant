package fluxdelivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// The in-process ResourceSet renderer.
//
// It reproduces flux-operator v0.62.1's builder (internal/builder/
// resourceset.go BuildResourceSet / BuildResourcesFromYAML, internal/inputs
// Flatten, and the owner labels and commonMetadata the controller stamps
// before applying) for the shape forge emits: a resourcesTemplate with inline
// inputs or input providers, under the Flatten strategy. It exists so a writer
// can check a render BEFORE it writes (§ the package doc) and so the template
// is testable without a cluster. It never applies anything.
//
// It does not import flux-operator: its builder is internal/, and its module
// is AGPL. The oracle test holds this renderer to the pinned
// `flux-operator build resourceset` on every fixture.
//
// The one deliberate gap: flux-operator also offers `slugify`
// (github.com/gosimple/slug, MPL-2.0). forge's template computes every name in
// Go and calls no slugify, so it is not provided, and a template that called
// it fails to parse here instead of rendering differently.

// ownerLabelGroup is flux-operator's owner-label prefix for ResourceSets.
const ownerLabelGroup = "resourceset.fluxcd.controlplane.io"

// reconcileAnnotation is flux-operator's per-object opt-out from rendering.
const reconcileAnnotation = "fluxcd.controlplane.io/reconcile"

// Provider is the exported inputs of one ResourceSetInputProvider the
// ResourceSet selects (the self-hosted shape). flux-operator adds the
// provider reference to each input; an `id` is the provider's own business
// and, if wanted, belongs in Inputs.
type Provider struct {
	Name   string
	Inputs []map[string]any
}

// Render builds the objects flux-operator would apply for rs, a ResourceSet
// object (as [ResourceSet] or kcl's flux_delivery_resourceset returns it),
// with providers standing in for the ResourceSetInputProviders its inputsFrom
// selects. The objects carry the owner labels and commonMetadata flux-operator
// stamps before it applies.
func Render(rs map[string]any, providers ...Provider) ([]*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{Object: rs}
	if obj.GetAPIVersion() != APIVersion || obj.GetKind() != KindResourceSet {
		return nil, fmt.Errorf("fluxdelivery: render: %s %s is not a %s %s", obj.GetAPIVersion(), obj.GetKind(), APIVersion, KindResourceSet)
	}
	name, namespace := obj.GetName(), obj.GetNamespace()
	if name == "" || namespace == "" {
		return nil, errors.New("fluxdelivery: render: the ResourceSet needs a name and a namespace")
	}
	spec, _, _ := unstructured.NestedMap(rs, "spec")
	for _, unsupported := range []string{"resources", "steps"} {
		if _, ok := spec[unsupported]; ok {
			return nil, fmt.Errorf("fluxdelivery: render: spec.%s is not a shape forge emits, so this renderer does not reproduce it", unsupported)
		}
	}
	if strategy, _, _ := unstructured.NestedString(rs, "spec", "inputStrategy", "name"); strategy != "" && strategy != "Flatten" {
		return nil, fmt.Errorf("fluxdelivery: render: inputStrategy %q; only Flatten is reproduced", strategy)
	}
	tmpl, _ := spec["resourcesTemplate"].(string)
	if tmpl == "" {
		return nil, errors.New("fluxdelivery: render: spec.resourcesTemplate is empty")
	}

	inputs, err := combine(rs, name, namespace, providers)
	if err != nil {
		return nil, err
	}
	_, hasProviders := spec["inputsFrom"]
	var built []*unstructured.Unstructured
	switch {
	case len(inputs) == 0 && hasProviders:
		// flux-operator reconciles an EMPTY set when its providers return
		// nothing — which garbage-collects, and the prune opt-out turns
		// into an orphaning. Nothing is built.
		return nil, nil
	case len(inputs) == 0:
		built, err = renderTemplate(tmpl, nil)
		if err != nil {
			return nil, err
		}
	default:
		for _, in := range inputs {
			objs, err := renderTemplate(tmpl, in)
			if err != nil {
				return nil, err
			}
			built = append(built, objs...)
		}
	}

	var out []*unstructured.Unstructured
	seen := map[string]bool{}
	for _, o := range built {
		if o.GetAnnotations()[reconcileAnnotation] == "disabled" {
			continue
		}
		key := strings.Join([]string{o.GroupVersionKind().Group, o.GetKind(), o.GetNamespace(), o.GetName()}, "/")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, o)
	}
	ownerLabels := map[string]string{ownerLabelGroup + "/name": name, ownerLabelGroup + "/namespace": namespace}
	commonLabels, _, _ := unstructured.NestedStringMap(rs, "spec", "commonMetadata", "labels")
	commonAnnotations, _, _ := unstructured.NestedStringMap(rs, "spec", "commonMetadata", "annotations")
	setCommonMetadata(out, ownerLabels, nil)
	setCommonMetadata(out, commonLabels, commonAnnotations)
	return out, nil
}

// combine is flux-operator's Flatten strategy: the ResourceSet's inline
// inputs first, then each provider's, providers ordered by name, every input
// annotated with the provider it came from. Inputs pass through JSON, as
// they do through the API server, so a number is a float64 here as there.
func combine(rs map[string]any, name, namespace string, providers []Provider) ([]map[string]any, error) {
	var out []map[string]any
	inline, _, _ := unstructured.NestedSlice(rs, "spec", "inputs")
	for i, raw := range inline {
		in, err := viaJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("fluxdelivery: render: spec.inputs[%d]: %w", i, err)
		}
		in["provider"] = map[string]any{"apiVersion": APIVersion, "kind": KindResourceSet, "name": name, "namespace": namespace}
		out = append(out, in)
	}
	sorted := append([]Provider(nil), providers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		for i, raw := range p.Inputs {
			in, err := viaJSON(raw)
			if err != nil {
				return nil, fmt.Errorf("fluxdelivery: render: provider %s input %d: %w", p.Name, i, err)
			}
			in["provider"] = map[string]any{"apiVersion": APIVersion, "kind": "ResourceSetInputProvider", "name": p.Name, "namespace": namespace}
			out = append(out, in)
		}
	}
	return out, nil
}

func viaJSON(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("not an object")
	}
	return out, nil
}

// renderTemplate executes the template for one input set and reads the
// documents back the way fluxcd/pkg/ssa's ReadObjects does: a document with
// no apiVersion, kind or name is DROPPED, not refused — which is why
// [CheckRendered] counts what came back.
func renderTemplate(tmpl string, input map[string]any) ([]*unstructured.Unstructured, error) {
	tp, err := template.New("resourceset").
		Delims("<<", ">>").
		Funcs(sprig.HermeticTxtFuncMap()).
		Funcs(template.FuncMap{"inputs": func() any { return input }}).
		Funcs(template.FuncMap{"toYaml": toYaml, "mustToYaml": mustToYaml}).
		Option("missingkey=error").
		Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("fluxdelivery: render: parsing the template: %w", err)
	}
	var b strings.Builder
	if err := tp.Execute(&b, nil); err != nil {
		return nil, fmt.Errorf("fluxdelivery: render: executing the template: %w", err)
	}
	return readObjects(b.String())
}

func readObjects(doc string) ([]*unstructured.Unstructured, error) {
	reader := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(doc)), 2048)
	var out []*unstructured.Unstructured
	for {
		obj := &unstructured.Unstructured{}
		if err := reader.Decode(obj); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, fmt.Errorf("fluxdelivery: render: reading the rendered objects: %w", err)
		}
		if obj.IsList() {
			if err := obj.EachListItem(func(item runtime.Object) error {
				out = append(out, item.(*unstructured.Unstructured))
				return nil
			}); err != nil {
				return out, err
			}
			continue
		}
		isKustomizeConfig := strings.ToLower(obj.GetKind()) == "kustomization" &&
			strings.HasPrefix(obj.GetAPIVersion(), "kustomize.config.k8s.io/")
		if obj.GetName() != "" && obj.GetKind() != "" && obj.GetAPIVersion() != "" && !isKustomizeConfig {
			out = append(out, obj)
		}
	}
}

func setCommonMetadata(objs []*unstructured.Unstructured, labels, annotations map[string]string) {
	for _, o := range objs {
		if len(labels) > 0 {
			l := o.GetLabels()
			if l == nil {
				l = map[string]string{}
			}
			for k, v := range labels {
				l[k] = v
			}
			o.SetLabels(l)
		}
		if len(annotations) > 0 {
			a := o.GetAnnotations()
			if a == nil {
				a = map[string]string{}
			}
			for k, v := range annotations {
				a[k] = v
			}
			o.SetAnnotations(a)
		}
	}
}

func toYaml(v any) string {
	s, err := mustToYaml(v)
	if err != nil {
		return ""
	}
	return s
}

func mustToYaml(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
