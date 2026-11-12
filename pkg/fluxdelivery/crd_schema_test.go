package fluxdelivery_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// The CRD-schema check: every rendered object is held to the schema the API
// server will hold it to — Flux v2.9.5's OCIRepository and Kustomization, and
// flux-operator v0.62.1's ResourceSet (testdata/crds/README.md) — with the API
// server's own code. Structural pruning reports any field the schema does not
// declare (which the API server would DROP, silently: a misspelt
// `deletionPolicy` would be a Kustomization that deletes on removal), and
// OpenAPI and CEL validation report wrong types, enums, patterns and required
// fields.

type crdSchema struct {
	structural *structuralschema.Structural
	validator  validation.SchemaValidator
	cel        *cel.Validator
}

func loadSchema(t *testing.T, props *apiextensionsv1.JSONSchemaProps) crdSchema {
	t.Helper()
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(props, &internal, nil); err != nil {
		t.Fatalf("converting the schema: %v", err)
	}
	structural, err := structuralschema.NewStructural(&internal)
	if err != nil {
		t.Fatalf("the schema is not structural: %v", err)
	}
	validator, _, err := validation.NewSchemaValidator(&internal)
	if err != nil {
		t.Fatalf("schema validator: %v", err)
	}
	return crdSchema{structural: structural, validator: validator, cel: cel.NewValidator(structural, true, 1_000_000)}
}

// fluxSchema loads one of Flux's published crd-schemas JSON files.
func fluxSchema(t *testing.T, file string) crdSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "crds", file))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	// crd-schemas is published in "strict" JSON-schema form, with
	// `additionalProperties: false` added to every object. The CRDs Flux
	// installs carry no such thing — an unknown field is PRUNED, not
	// refused — and the API server's pruning treats a boolean
	// additionalProperties as "keep the key", so leaving it in would hide
	// exactly the fields this check exists to find. Removed, the schema is
	// the installed CRD's.
	doc = dropStrictAdditionalProperties(doc)
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var props apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(raw, &props); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return loadSchema(t, &props)
}

func dropStrictAdditionalProperties(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if b, ok := x["additionalProperties"].(bool); ok && !b {
			delete(x, "additionalProperties")
		}
		for k, child := range x {
			x[k] = dropStrictAdditionalProperties(child)
		}
	case []any:
		for i, child := range x {
			x[i] = dropStrictAdditionalProperties(child)
		}
	}
	return v
}

// resourceSetSchema loads flux-operator's ResourceSet CRD.
func resourceSetSchema(t *testing.T) crdSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "crds", "fluxcd.controlplane.io_resourcesets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	for _, v := range crd.Spec.Versions {
		if v.Name == "v1" && v.Schema != nil {
			return loadSchema(t, v.Schema.OpenAPIV3Schema)
		}
	}
	t.Fatal("the ResourceSet CRD has no v1 schema")
	return crdSchema{}
}

func (s crdSchema) check(t *testing.T, label string, obj map[string]any) {
	t.Helper()
	// Prune a COPY: what it removes is what the API server would drop.
	cp := runtimeCopy(t, obj)
	if pruned := pruning.PruneWithOptions(cp, s.structural, true, structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true}); len(pruned) > 0 {
		t.Errorf("%s: fields the CRD does not declare (the API server would drop them): %v", label, pruned)
	}
	if errs := validation.ValidateCustomResource(field.NewPath(""), obj, s.validator); len(errs) > 0 {
		t.Errorf("%s: schema validation: %v", label, errs.ToAggregate())
	}
	if errs, _ := s.cel.Validate(context.Background(), field.NewPath(""), s.structural, obj, nil, 10_000_000); len(errs) > 0 {
		t.Errorf("%s: CEL validation: %v", label, errs.ToAggregate())
	}
}

func runtimeCopy(t *testing.T, obj map[string]any) map[string]any {
	t.Helper()
	return (&unstructured.Unstructured{Object: obj}).DeepCopy().Object
}

// TestRenderedObjectsMatchFluxCRDs: every object every fixture renders is
// accepted, whole, by the pinned Flux CRD schemas.
func TestRenderedObjectsMatchFluxCRDs(t *testing.T) {
	t.Parallel()
	schemas := map[string]crdSchema{
		fluxdelivery.KindOCIRepository: fluxSchema(t, "ocirepository-source-v1.json"),
		fluxdelivery.KindKustomization: fluxSchema(t, "kustomization-kustomize-v1.json"),
	}
	for _, fx := range fixtures() {
		for _, o := range render(t, fx.in()) {
			s, ok := schemas[o.GetKind()]
			if !ok {
				t.Errorf("%s renders a %s, which has no pinned schema", fx.name, o.GetKind())
				continue
			}
			s.check(t, fx.name+" "+o.GetKind()+" "+o.GetName(), o.Object)
		}
	}
}

// TestResourceSetMatchesItsCRD: the ResourceSet the hub writes is accepted by
// flux-operator's CRD, CEL rules included.
func TestResourceSetMatchesItsCRD(t *testing.T) {
	t.Parallel()
	s := resourceSetSchema(t)
	for _, fx := range fixtures() {
		rs, err := fluxdelivery.ResourceSet(fx.in())
		if err != nil {
			t.Fatal(err)
		}
		s.check(t, fx.name+" ResourceSet", rs)
	}
}

// TestSchemaCheckIsNotVacuous: the check refuses what it exists to refuse —
// an undeclared field, and a value outside an enum.
func TestSchemaCheckIsNotVacuous(t *testing.T) {
	t.Parallel()
	s := fluxSchema(t, "kustomization-kustomize-v1.json")
	k := render(t, flatConnected())[1].DeepCopy()
	_ = unstructured.SetNestedField(k.Object, "Orphan", "spec", "deletePolicy")
	cp := runtimeCopy(t, k.Object)
	if pruned := pruning.PruneWithOptions(cp, s.structural, true, structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true}); len(pruned) == 0 {
		t.Error("a misspelt spec.deletePolicy was not reported as undeclared")
	}
	_ = unstructured.SetNestedField(k.Object, "Keep", "spec", "deletionPolicy")
	if errs := validation.ValidateCustomResource(field.NewPath(""), k.Object, s.validator); len(errs) == 0 {
		t.Error("deletionPolicy Keep passed schema validation; the enum is not checked")
	}
}
