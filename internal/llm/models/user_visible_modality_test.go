// Copyright (c) 2025 Reliant Labs
package models

import "testing"

// modalityVisibilityYAML covers the three cases that matter for chat-surface
// visibility: a plain chat model with no declared modalities, an image-only
// generation model, and a multi-modal model that emits both.
const modalityVisibilityYAML = `
models:
  - id: chat-model
    name: Chat Model
    tags: [flagship]
    visibility: user
    providers:
      - driver: openai
        api_model: chat

  - id: image-only-model
    name: Image Only Model
    tags: [image-gen]
    visibility: user
    capabilities:
      output_modalities: [image]
    providers:
      - driver: openai
        api_model: image-only

  - id: multimodal-model
    name: Multi-Modal Model
    tags: [image-gen]
    visibility: user
    capabilities:
      output_modalities: [text, image]
    providers:
      - driver: openai
        api_model: multimodal

  - id: dev-image-model
    name: Dev Image Model
    tags: [image-gen]
    visibility: dev
    capabilities:
      output_modalities: [image]
    providers:
      - driver: openai
        api_model: dev-image
`

func modalityVisibilityRegistry(t *testing.T) *ModelRegistry {
	t.Helper()
	reg, err := ParseRegistryFromBytes([]byte(modalityVisibilityYAML))
	if err != nil {
		t.Fatalf("failed to parse test YAML: %v", err)
	}
	return reg
}

func modelIDSet(defs []*ModelDefinition) map[string]bool {
	ids := make(map[string]bool, len(defs))
	for _, def := range defs {
		ids[def.ID] = true
	}
	return ids
}

// TestGetUserVisibleModels_ExcludesNonTextModels is the regression test for
// image-generation models leaking into the chat model picker. Selecting one
// would send a chat completion to a model that has no chat endpoint.
func TestGetUserVisibleModels_ExcludesNonTextModels(t *testing.T) {
	reg := modalityVisibilityRegistry(t)
	ids := modelIDSet(reg.GetUserVisibleModels())

	if ids["image-only-model"] {
		t.Errorf("GetUserVisibleModels() returned image-only-model; an image-only model has no chat endpoint and must not appear on chat-model surfaces")
	}
	if !ids["chat-model"] {
		t.Errorf("GetUserVisibleModels() dropped chat-model; a model that declares no output_modalities must still count as text")
	}
	if ids["dev-image-model"] {
		t.Errorf("GetUserVisibleModels() returned dev-image-model; visibility=dev is excluded independently of modality")
	}
}

// TestGetUserVisibleModels_KeepsMultiModalModels pins the subtlety of filtering
// by capability: a model that emits BOTH text and image can still serve a chat
// request, so excluding it would be a regression in the other direction.
func TestGetUserVisibleModels_KeepsMultiModalModels(t *testing.T) {
	reg := modalityVisibilityRegistry(t)
	ids := modelIDSet(reg.GetUserVisibleModels())

	if !ids["multimodal-model"] {
		t.Errorf("GetUserVisibleModels() dropped multimodal-model; output_modalities [text, image] can serve chat and must remain visible")
	}
}

// TestGetUserVisibleModelsForModality_Image gives the image-generation surface
// the inverse view: user-visible models that can actually produce an image.
func TestGetUserVisibleModelsForModality_Image(t *testing.T) {
	reg := modalityVisibilityRegistry(t)
	ids := modelIDSet(reg.GetUserVisibleModelsForModality(ModalityImage))

	for _, want := range []string{"image-only-model", "multimodal-model"} {
		if !ids[want] {
			t.Errorf("GetUserVisibleModelsForModality(image) missing %q", want)
		}
	}
	if ids["chat-model"] {
		t.Errorf("GetUserVisibleModelsForModality(image) returned chat-model, which emits text only")
	}
	if ids["dev-image-model"] {
		t.Errorf("GetUserVisibleModelsForModality(image) returned dev-image-model; visibility=dev is still excluded")
	}
}

// TestGetUserVisibleModels_RealRegistryExcludesImageModels asserts against the
// production models.yaml, not a fixture: these are the concrete IDs that were
// leaking into the picker.
func TestGetUserVisibleModels_RealRegistryExcludesImageModels(t *testing.T) {
	reg := MustGetRegistry()
	ids := modelIDSet(reg.GetUserVisibleModels())

	for _, imageModel := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2"} {
		if _, defined := reg.GetDefinition(imageModel); !defined {
			t.Fatalf("expected %q to be defined in models.yaml; this test guards its visibility", imageModel)
		}
		if ids[imageModel] {
			t.Errorf("GetUserVisibleModels() returned %q; image-generation models must not appear in the chat model picker", imageModel)
		}
	}

	// The filter must not have emptied the picker.
	if len(ids) == 0 {
		t.Fatal("GetUserVisibleModels() returned no models at all")
	}
	if !ids["claude-4.5-sonnet"] && !ids["gpt-5"] {
		t.Errorf("GetUserVisibleModels() returned %d models but no recognizable flagship chat model; filter is too aggressive", len(ids))
	}
}
