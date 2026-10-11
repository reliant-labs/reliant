// Copyright (c) 2025 Reliant Labs
package models

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every vertexai api_model must be the "Model ID" Google publishes on the
// model's Vertex AI page:
//
//	docs.cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/<slug>
//	docs.cloud.google.com/vertex-ai/generative-ai/docs/models/gemini/<slug>
//
// The vertexai driver puts api_model straight into the publisher-model URL, so
// a wrong id is a 404 on every request — and nothing else in the catalog
// catches it, because the Anthropic-style ids of the other drivers look just
// like it. vertex-claude-4.5-haiku shipped as claude-haiku-4-5@20250512, a
// snapshot date that is not Haiku 4.5's (it was released 2025-10-15) and that
// Vertex does not serve.
//
// Google lists one version per model, the bare Model ID; it does not document
// @YYYYMMDD snapshot ids for these models. control-plane's LiteLLM gateway
// routes the same bare ids (vertex_ai/claude-haiku-4-5) to Vertex in prod.
//
// A model page that announces a retirement date gets a row in
// documentedVertexRetirements instead, once the date is known: its entry
// leaves the catalog and its id redirects to Google's replacement.
var documentedVertexModelIDs = map[string]string{
	// partner-models/claude/opus-5-5
	"claude-5.5-opus":        "claude-opus-5-5",
	"vertex-claude-5.5-opus": "claude-opus-5-5",
	// partner-models/claude/fable-5-1
	"claude-5.1-fable":        "claude-fable-5-1",
	"vertex-claude-5.1-fable": "claude-fable-5-1",
	// partner-models/claude/opus-5
	"vertex-claude-5-opus": "claude-opus-5",
	// partner-models/claude/opus-4-8
	"vertex-claude-4.8-opus": "claude-opus-4-8",
	// partner-models/claude/sonnet-5-5
	"vertex-claude-5.5-sonnet": "claude-sonnet-5-5",
	// partner-models/claude/sonnet-5
	"vertex-claude-5-sonnet": "claude-sonnet-5",
	// partner-models/claude/fable-5
	"vertex-claude-5-fable": "claude-fable-5",
	// partner-models/claude/opus-4-6
	"vertex-claude-4.6-opus": "claude-opus-4-6",
	// partner-models/claude/sonnet-4-5
	"vertex-claude-4.5-sonnet": "claude-sonnet-4-5",
	// partner-models/claude/haiku-4-5
	"vertex-claude-4.5-haiku": "claude-haiku-4-5",
	// models/gemini/3-8-flash. Replaces vertex-gemini-2.5-pro and
	// vertex-gemini-2.5-flash (models/gemini/2-5-pro, 2-5-flash), which Vertex
	// retires on 2026-10-20 — see documentedVertexRetirements.
	"vertex-gemini-3.8-flash": "gemini-3.8-flash",
}

func TestVertexAPIModelsMatchVertexDocs(t *testing.T) {
	reg := MustGetRegistry()

	ids := make([]string, 0, len(documentedVertexModelIDs))
	for id := range documentedVertexModelIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			got, ok := providerAPIModels(t, reg, id)["vertexai"]
			if assert.True(t, ok, "%s has no vertexai mapping", id) {
				assert.Equal(t, documentedVertexModelIDs[id], got,
					"%s: vertexai api_model must be the Model ID on its Vertex AI model page", id)
			}
		})
	}
}

// documentedVertexRetirements is every Vertex model the catalog mapped that
// Google retires, with the replacement Google names first under "Replacement
// model ID" on its lifecycle page:
//
//	docs.cloud.google.com/vertex-ai/generative-ai/docs/learn/model-versions
//
// Checked 2026-10-11. The retired id must keep resolving, to that replacement
// on Vertex: a chat pinned to it would otherwise 404 from the retirement date.
var documentedVertexRetirements = map[string]struct{ retiredOn, replacement string }{
	// gemini-2.5-pro: "gemini-3.8-flash or gemini-3.5-flash"
	"vertex-gemini-2.5-pro": {"2026-10-20", "gemini-3.8-flash"},
	// gemini-2.5-flash: "gemini-3.8-flash or gemini-3.5-flash-lite or
	// gemini-3.1-flash-lite"
	"vertex-gemini-2.5-flash": {"2026-10-20", "gemini-3.8-flash"},
}

func TestRetiredVertexModelsMoveToGooglesReplacement(t *testing.T) {
	reg := MustGetRegistry()
	for id, want := range documentedVertexRetirements {
		t.Run(id, func(t *testing.T) {
			_, defined := reg.GetDefinition(id)
			assert.False(t, defined, "%s is retired on Vertex; it must have no vertexai mapping", id)

			resolved, err := reg.Resolve(ModelSelector{ID: id + "@vertexai"}, []string{"vertexai"})
			if assert.NoError(t, err, "a chat pinned to %s must still resolve", id) {
				assert.Equal(t, "vertexai", resolved.Provider.Driver)
				assert.Equal(t, want.replacement, resolved.Provider.APIModel,
					"%s must move to the replacement Google names on its lifecycle page", id)
				if assert.NotNil(t, resolved.Retirement) {
					assert.Equal(t, want.retiredOn, resolved.Retirement.RetiredOn, "%s: Google's retirement date", id)
				}
			}
		})
	}
}

// A vertexai mapping added without a row above would ship an id nobody
// checked against Google's docs.
func TestEveryVertexMappingHasDocumentedID(t *testing.T) {
	for _, def := range MustGetRegistry().ListModelsByProvider("vertexai") {
		_, ok := documentedVertexModelIDs[def.ID]
		assert.True(t, ok, "%s maps to vertexai but has no row in documentedVertexModelIDs; "+
			"add the Model ID from its Vertex AI model page", def.ID)
	}
}
