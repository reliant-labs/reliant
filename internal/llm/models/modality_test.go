// Copyright (c) 2025 Reliant Labs
package models

import (
	"strings"
	"testing"
)

// modalityTestRegistry builds a registry with a text model and an image model
// that deliberately SHARE the "cheap" tag. The shared tag is the point: it is
// what makes the graceful-degradation trap reachable in TestResolve_*.
func modalityTestRegistry(t *testing.T) *ModelRegistry {
	t.Helper()

	reg, err := buildRegistry([]ModelDefinition{
		{
			ID:   "text-cheap",
			Name: "Text Cheap",
			Tags: []string{"cheap", "fast"},
			// No output_modalities: exercises the text default.
			Capabilities: ModelCapabilities{SupportsTools: true},
			Providers:    []ProviderMapping{{Driver: "openai", APIModel: "text-cheap"}},
		},
		{
			ID:   "image-model",
			Name: "Image Model",
			Tags: []string{"cheap", "image-gen"},
			Capabilities: ModelCapabilities{
				OutputModalities: []Modality{ModalityImage},
			},
			Providers: []ProviderMapping{{Driver: "openai", APIModel: "image-model"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	return reg
}

// TestEffectiveOutputModalities_DefaultsToText pins the backward-compatibility
// contract: every model definition predating output_modalities must still
// resolve as a text model. If this regresses, no existing workflow can resolve
// a model at all.
func TestEffectiveOutputModalities_DefaultsToText(t *testing.T) {
	var caps ModelCapabilities

	got := caps.EffectiveOutputModalities()
	if len(got) != 1 || got[0] != ModalityText {
		t.Fatalf("undeclared modalities = %v, want [text]", got)
	}
	if !caps.CanOutput(ModalityText) {
		t.Error("undeclared model should report text output")
	}
	if caps.CanOutput(ModalityImage) {
		t.Error("undeclared model must NOT report image output")
	}
}

func TestCanOutput_ExplicitDeclaration(t *testing.T) {
	imageOnly := ModelCapabilities{OutputModalities: []Modality{ModalityImage}}
	if !imageOnly.CanOutput(ModalityImage) {
		t.Error("image model should report image output")
	}
	// An explicit declaration REPLACES the default rather than adding to it —
	// an image-only model must not silently retain text capability.
	if imageOnly.CanOutput(ModalityText) {
		t.Error("image-only model must not report text output")
	}

	both := ModelCapabilities{OutputModalities: []Modality{ModalityText, ModalityImage}}
	if !both.CanOutput(ModalityText) || !both.CanOutput(ModalityImage) {
		t.Error("multi-modal model should report both modalities")
	}
}

// TestResolve_ModalityFilterBeatsTagDegradation is the reason the modality
// constraint is a hard filter instead of a tag.
//
// Both models carry "cheap". Tag scoring alone ranks "text-cheap" first (it
// matches both requested tags, and wins the tie on definition order). Without
// the modality filter, an image request resolves to a TEXT model and fails
// later inside a driver, far from the actual mistake.
func TestResolve_ModalityFilterBeatsTagDegradation(t *testing.T) {
	reg := modalityTestRegistry(t)
	providers := []string{"openai"}

	// Baseline: confirm the trap is real. Without the constraint, the
	// text model wins — so the filter below is doing genuine work.
	unconstrained, err := reg.Resolve(ModelSelector{Tags: []string{"cheap", "fast"}}, providers)
	if err != nil {
		t.Fatalf("unconstrained resolve: %v", err)
	}
	if unconstrained.Definition.ID != "text-cheap" {
		t.Fatalf("baseline resolved %s, want text-cheap — test no longer exercises the trap",
			unconstrained.Definition.ID)
	}

	// With the constraint, the same selector must reach the image model.
	resolved, err := reg.Resolve(ModelSelector{
		Tags:                  []string{"cheap", "fast"},
		RequireOutputModality: ModalityImage,
	}, providers)
	if err != nil {
		t.Fatalf("constrained resolve: %v", err)
	}
	if resolved.Definition.ID != "image-model" {
		t.Errorf("resolved %s, want image-model", resolved.Definition.ID)
	}
}

// TestResolve_NoCapableModel_FailsClearly pins the error PATH, not just the
// error: selection must fail at selection time with a message naming the
// modality, rather than dispatching to a model that cannot do the work.
func TestResolve_NoCapableModel_FailsClearly(t *testing.T) {
	reg := modalityTestRegistry(t)

	_, err := reg.Resolve(ModelSelector{
		Tags:                  []string{"fast"}, // only the text model has this
		RequireOutputModality: ModalityImage,
	}, []string{"openai"})
	if err == nil {
		t.Fatal("expected an error when no candidate can generate images")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error %q should name the required modality", err)
	}
}

// TestResolve_ByID_RejectsIncapableModel covers the explicit-ID path. Naming a
// model directly must not bypass the capability check.
func TestResolve_ByID_RejectsIncapableModel(t *testing.T) {
	reg := modalityTestRegistry(t)

	_, err := reg.Resolve(ModelSelector{
		ID:                    "text-cheap",
		RequireOutputModality: ModalityImage,
	}, []string{"openai"})
	if err == nil {
		t.Fatal("expected an error when the named model cannot generate images")
	}
	if !strings.Contains(err.Error(), "text-cheap") {
		t.Errorf("error %q should name the offending model", err)
	}
}

// TestResolve_NoConstraint_Unchanged is the regression guard for every existing
// caller: a selector that sets no modality must behave exactly as before.
func TestResolve_NoConstraint_Unchanged(t *testing.T) {
	reg := modalityTestRegistry(t)

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{"cheap"}}, []string{"openai"})
	if err != nil {
		t.Fatalf("unconstrained resolve should still work: %v", err)
	}
	if resolved.Definition.ID != "text-cheap" {
		t.Errorf("resolved %s, want text-cheap (definition order)", resolved.Definition.ID)
	}
}
