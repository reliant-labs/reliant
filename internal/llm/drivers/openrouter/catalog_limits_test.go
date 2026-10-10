// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdata/openrouter_models.json is GET https://openrouter.ai/api/v1/models
// (public, no credential) fetched 2026-10-10, reduced to the models
// models.yaml maps to `openrouter` and to the two limits reliant uses.
// Refresh with:
//
//	ids=$(awk '/- driver: openrouter/{getline; print $2}' internal/llm/models/definitions/models.yaml | sort -u | jq -R . | jq -s -c .)
//	curl -s https://openrouter.ai/api/v1/models | jq --indent 1 --argjson ids "$ids" \
//	  '{data: [.data[] | select(.id as $i | $ids | index($i)) | {id, context_length,
//	   top_provider: {context_length: .top_provider.context_length,
//	   max_completion_tokens: .top_provider.max_completion_tokens}}] | sort_by(.id)}'
type openRouterCapture struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		TopProvider   struct {
			ContextLength       int `json:"context_length"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
	} `json:"data"`
}

// TestCatalogOpenRouterLimitsMatchCapture pins every openrouter mapping's
// window and max output to what OpenRouter itself reports. The max output is
// also the max_tokens reliant requests there, so a catalog value above
// OpenRouter's max_completion_tokens is a request it may refuse.
func TestCatalogOpenRouterLimitsMatchCapture(t *testing.T) {
	body, err := os.ReadFile("testdata/openrouter_models.json")
	require.NoError(t, err)
	var capture openRouterCapture
	require.NoError(t, json.Unmarshal(body, &capture))

	type limits struct{ window, maxOutput int }
	published := map[string]limits{}
	for _, m := range capture.Data {
		published[m.ID] = limits{window: m.TopProvider.ContextLength, maxOutput: m.TopProvider.MaxCompletionTokens}
	}

	for _, def := range models.MustGetRegistry().ListModelsByProvider("openrouter") {
		for _, p := range def.Providers {
			if p.Driver != "openrouter" {
				continue
			}
			want, ok := published[p.APIModel]
			if !assert.Truef(t, ok, "%s maps openrouter api_model %q, which the capture lacks: refresh it", def.ID, p.APIModel) {
				continue
			}
			assert.Equalf(t, want.window, models.EffectiveContextWindow(&def, "openrouter"),
				"%s@openrouter window: OpenRouter reports context_length %d", def.ID, want.window)
			assert.Equalf(t, want.maxOutput, models.EffectiveMaxOutputTokens(&def, "openrouter"),
				"%s@openrouter max output: OpenRouter reports max_completion_tokens %d", def.ID, want.maxOutput)
		}
	}
}
