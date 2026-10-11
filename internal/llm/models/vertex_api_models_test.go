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
	// models/gemini/2-5-pro
	"vertex-gemini-2.5-pro": "gemini-2.5-pro",
	// models/gemini/2-5-flash
	"vertex-gemini-2.5-flash": "gemini-2.5-flash",
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

// A vertexai mapping added without a row above would ship an id nobody
// checked against Google's docs.
func TestEveryVertexMappingHasDocumentedID(t *testing.T) {
	for _, def := range MustGetRegistry().ListModelsByProvider("vertexai") {
		_, ok := documentedVertexModelIDs[def.ID]
		assert.True(t, ok, "%s maps to vertexai but has no row in documentedVertexModelIDs; "+
			"add the Model ID from its Vertex AI model page", def.ID)
	}
}
