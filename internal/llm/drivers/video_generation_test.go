// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

var veoModelIDs = []string{"veo-3.1-generate", "veo-3.1-fast-generate", "veo-3.1-lite-generate"}

// videoModelIDs is every video model, Omni included.
var videoModelIDs = append([]string{"gemini-omni-1.1-flash"}, veoModelIDs...)

// TestVideoModels_DeclareVideoOutputAndTags pins the registry shape the video
// path depends on: the modality is the hard filter that keeps a text model off
// the video endpoint, the tag is what the tool's bound selector resolves by,
// and the capability block is what request validation reads.
func TestVideoModels_DeclareVideoOutputAndTags(t *testing.T) {
	registry := models.MustGetRegistry()
	for _, id := range videoModelIDs {
		def, ok := registry.GetDefinition(id)
		if !ok {
			t.Fatalf("%s is not defined in models.yaml", id)
		}
		if !def.Capabilities.CanOutput(models.ModalityVideo) || def.Capabilities.CanOutput(models.ModalityText) {
			t.Errorf("%s produces %v; a video model must declare output_modalities: [video] only", id, def.Capabilities.EffectiveOutputModalities())
		}
		if def.Capabilities.Video == nil || len(def.Capabilities.Video.Durations) == 0 {
			t.Errorf("%s declares no video capability block, so nothing validates requests for it", id)
		}
		if !slices.Contains(registry.TagsOf(id), DefaultVideoGenTag) {
			t.Errorf("%s lacks the %q tag", id, DefaultVideoGenTag)
		}
		for _, p := range def.Providers {
			if _, ok := videoGenBaseURLs[p.Driver]; !ok && p.Driver != "reliant" {
				t.Errorf("%s maps provider %q, which has no video client; it would win selection and fail at call time", id, p.Driver)
			}
		}
	}
	if def, _ := registry.GetDefinition("veo-3.1-lite-generate"); def.Capabilities.Video.MaxReferenceImages != 0 || def.Capabilities.Video.SupportsExtend {
		t.Error("lite has no reference images and no extension")
	}
}

func TestVideoModels_NeverInChatPicker(t *testing.T) {
	registry := models.MustGetRegistry()
	visible := map[string]bool{}
	for _, def := range registry.GetUserVisibleModels() {
		visible[def.ID] = true
	}
	for _, id := range videoModelIDs {
		if visible[id] {
			t.Errorf("%s appears in the chat model picker", id)
		}
	}
}

func TestVideoGenResolution(t *testing.T) {
	registry := models.MustGetRegistry()
	selector := models.ModelSelector{Tags: []string{DefaultVideoGenTag, models.TagFlagship}, RequireOutputModality: models.ModalityVideo}

	resolved, err := registry.Resolve(selector, []string{"gemini"})
	if err != nil {
		t.Fatalf("a user with a gemini key must resolve a video model: %v", err)
	}
	if resolved.Definition.ID != "gemini-omni-1.1-flash" || resolved.Provider.Driver != "gemini" {
		t.Errorf("default = %s on %s, want gemini-omni-1.1-flash on gemini", resolved.Definition.ID, resolved.Provider.Driver)
	}
	if first := registry.TagEntries("video-gen"); len(first) == 0 || first[0].Model != "gemini-omni-1.1-flash" {
		t.Errorf("gemini-omni-1.1-flash must be first in video-gen (order is the default): %v", first)
	}
	if !slices.Contains(registry.TagsOf("gemini-omni-1.1-flash"), models.TagFlagship) {
		t.Error("gemini-omni-1.1-flash must be tagged flagship so [video-gen, flagship] picks it")
	}
	if caps := resolved.Definition.Capabilities.Video; caps == nil || !caps.SupportsEdit || caps.SupportsNegativePrompt {
		t.Errorf("omni supports edit and has no native negative prompt: %+v", caps)
	}

	// The modality filter must stop [video-gen, flagship] degrading onto a
	// flagship TEXT model for a user with no video-capable key.
	resolved, err = registry.Resolve(selector, []string{"anthropic", "openai", "codex"})
	if err == nil {
		t.Errorf("resolved %s for a user with no video provider; want an error", resolved.Definition.ID)
	}

	cheap, err := registry.Resolve(models.ModelSelector{Tags: []string{DefaultVideoGenTag, models.TagCheap}, RequireOutputModality: models.ModalityVideo}, []string{"gemini"})
	if err != nil || cheap.Definition.ID != "veo-3.1-lite-generate" {
		t.Errorf("[video-gen, cheap] = %v, %v; want veo-3.1-lite-generate", cheap, err)
	}
	cinematic, err := registry.Resolve(models.ModelSelector{Tags: []string{DefaultVideoGenTag, "cinematic"}, RequireOutputModality: models.ModalityVideo}, []string{"gemini"})
	if err != nil || cinematic.Definition.ID != "veo-3.1-generate" {
		t.Errorf("[video-gen, cinematic] = %v, %v; want veo-3.1-generate", cinematic, err)
	}

	// Naming a text model explicitly fails with the modality in the message.
	if _, err := registry.Resolve(models.ModelSelector{ID: "claude-5.5-opus", RequireOutputModality: models.ModalityVideo}, []string{"anthropic"}); err == nil {
		t.Error("a text model must not satisfy a video request")
	}
}

// A credits-only user (providers [reliant]) must never silently land on the
// $0.40/s standard Veo through the default tier, and Omni (not on Vertex) has
// no reliant mapping.
func TestVideoGenResolution_ReliantOnly(t *testing.T) {
	registry := models.MustGetRegistry()
	resolve := func(tags ...string) string {
		resolved, err := registry.Resolve(models.ModelSelector{Tags: tags, RequireOutputModality: models.ModalityVideo}, []string{"reliant"})
		if err != nil {
			t.Fatalf("resolve %v: %v", tags, err)
		}
		if resolved.Provider.Driver != "reliant" {
			t.Errorf("%v resolved on %s", tags, resolved.Provider.Driver)
		}
		return resolved.Definition.ID
	}
	if got := resolve("video-gen", "flagship"); got != "veo-3.1-fast-generate" {
		t.Errorf("standard = %s, want veo-3.1-fast-generate", got)
	}
	if got := resolve("video-gen", "cinematic"); got != "veo-3.1-generate" {
		t.Errorf("cinematic = %s, want veo-3.1-generate", got)
	}
	if got := resolve("video-gen", "cheap"); got != "veo-3.1-lite-generate" {
		t.Errorf("fast = %s, want veo-3.1-lite-generate", got)
	}

	omni, _ := registry.GetDefinition("gemini-omni-1.1-flash")
	for _, p := range omni.Providers {
		if p.Driver == "reliant" {
			t.Error("omni must have no reliant mapping: it is not on Vertex")
		}
	}
	for id, apiModel := range map[string]string{
		"veo-3.1-generate": "veo-3.1-generate-001", "veo-3.1-fast-generate": "veo-3.1-fast-generate-001", "veo-3.1-lite-generate": "veo-3.1-lite-generate-001",
	} {
		def, _ := registry.GetDefinition(id)
		found := false
		for _, p := range def.Providers {
			found = found || (p.Driver == "reliant" && p.APIModel == apiModel)
		}
		if !found {
			t.Errorf("%s lacks reliant mapping %s", id, apiModel)
		}
	}
}

func TestVideoGen_UnavailableMessageMentionsCredits(t *testing.T) {
	msg := models.MediaUnavailableMessage(models.MustGetRegistry(), models.ModalityVideo)
	t.Log(msg)
	if !strings.Contains(msg, "Reliant-managed credits") {
		t.Errorf("message %q does not mention credits", msg)
	}
}
