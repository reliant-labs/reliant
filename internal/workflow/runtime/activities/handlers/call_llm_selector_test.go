// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// codexServesOnly is an account availability on which codex serves only the
// named models (every other codex model is hidden), as an authoritative
// /codex/models report would say.
func codexServesOnly(served ...string) models.AvailabilityFunc {
	return func(driver, modelID string) models.ModelAvailability {
		if driver != "codex" {
			return models.ModelAvailability{}
		}
		for _, s := range served {
			if modelID == s {
				return models.ModelAvailability{}
			}
		}
		return models.ModelAvailability{Disabled: true, Reason: "not served"}
	}
}

// An agent turn's tier selection never resolves to a model that cannot write
// text (reliant bug 32f76b4d): with codex serving only its image models, a
// codex-preferred tier falls through to Claude's text model instead of picking
// gpt-image-2.5-*, which the turn would then fail on.
func TestAgentTurnTierNeverResolvesToAnImageModel(t *testing.T) {
	reg := models.MustGetRegistry().WithAvailability(codexServesOnly("gpt-image-2.5-sunburst", "gpt-image-2.5-flare"))
	providers := []string{"codex", "claude"}
	for _, tags := range [][]string{{"flagship"}, {"fast"}, {"cheap"}, {"moderate"}} {
		sel := agentTurnSelector(models.ModelSelector{Tags: tags, Providers: []string{"codex"}})
		r, err := reg.Resolve(sel, providers)
		if err != nil {
			t.Fatalf("%v: %v", tags, err)
		}
		if !r.Definition.Capabilities.CanOutput(models.ModalityText) {
			t.Errorf("%v resolved to %s@%s, which cannot write text", tags, r.Definition.ID, r.Provider.Driver)
		}
		if r.Provider.Driver != "claude" {
			t.Errorf("%v resolved to %s@%s, want Claude's text model: codex serves no text model", tags, r.Definition.ID, r.Provider.Driver)
		}
	}
}

// With codex serving its text models, the selection is unchanged: the filter
// only removes what an agent turn could never use.
func TestAgentTurnTierKeepsTheServedTextModel(t *testing.T) {
	reg := models.MustGetRegistry().WithAvailability(codexServesOnly("gpt-6.1-sol", "gpt-6-luna", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"))
	for tags, want := range map[string]string{"flagship": "gpt-6.1-sol", "cheap": "gpt-6-luna"} {
		r, err := reg.Resolve(agentTurnSelector(models.ModelSelector{Tags: []string{tags}, Providers: []string{"codex"}}), []string{"codex", "claude"})
		if err != nil {
			t.Fatalf("%s: %v", tags, err)
		}
		if r.Definition.ID != want || r.Provider.Driver != "codex" {
			t.Errorf("[%s] resolved to %s@%s, want %s@codex", tags, r.Definition.ID, r.Provider.Driver, want)
		}
	}
}

func TestAgentTurnSelectorLeavesExplicitChoicesAlone(t *testing.T) {
	for name, sel := range map[string]models.ModelSelector{
		"exact id":         {ID: "gpt-image-2.5-sunburst@codex"},
		"media tier":       {Tags: []string{"image-gen", "flagship"}},
		"modality already": {Tags: []string{"fast"}, RequireOutputModality: models.ModalityImage},
	} {
		if got := agentTurnSelector(sel); got.RequireOutputModality != sel.RequireOutputModality {
			t.Errorf("%s: RequireOutputModality %q, want %q unchanged", name, got.RequireOutputModality, sel.RequireOutputModality)
		}
	}
	if got := agentTurnSelector(models.ModelSelector{Tags: []string{"flagship"}}); got.RequireOutputModality != models.ModalityText {
		t.Errorf("a tier selection requires %q, want text", got.RequireOutputModality)
	}
}
