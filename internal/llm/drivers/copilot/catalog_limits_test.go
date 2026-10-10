// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogCopilotLimitsMatchCapture pins every copilot mapping's window and
// max output to what Copilot's GET /models reports
// (testdata/copilot_models.json, recorded 2026-10-04):
// max_context_window_tokens and max_output_tokens. Copilot serves several
// models smaller than their makers publish (claude-haiku-4.5 at 144,000 /
// 32,000, claude-sonnet-5 at 64,000 output), and the max output is the
// max_tokens reliant requests there, so the per-provider overrides in
// models.yaml must track this capture exactly.
//
// max_prompt_tokens is not pinned: it is the live advertised limit
// (ReportAvailability), which lowers the prompt ceiling at runtime.
func TestCatalogCopilotLimitsMatchCapture(t *testing.T) {
	body, err := os.ReadFile("testdata/copilot_models.json")
	require.NoError(t, err)
	var capture struct {
		Data []struct {
			ID           string `json:"id"`
			Capabilities struct {
				Limits struct {
					MaxContextWindowTokens int `json:"max_context_window_tokens"`
					MaxOutputTokens        int `json:"max_output_tokens"`
				} `json:"limits"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &capture))

	type limits struct{ window, maxOutput int }
	published := map[string]limits{}
	for _, m := range capture.Data {
		published[m.ID] = limits{window: m.Capabilities.Limits.MaxContextWindowTokens, maxOutput: m.Capabilities.Limits.MaxOutputTokens}
	}

	for _, def := range models.MustGetRegistry().ListModelsByProvider("copilot") {
		for _, p := range def.Providers {
			if p.Driver != "copilot" {
				continue
			}
			want, ok := published[p.APIModel]
			if !assert.Truef(t, ok, "%s maps copilot api_model %q, which the capture lacks: refresh it", def.ID, p.APIModel) {
				continue
			}
			assert.Equalf(t, want.window, models.EffectiveContextWindow(&def, "copilot"),
				"%s@copilot window: Copilot reports max_context_window_tokens %d", def.ID, want.window)
			assert.Equalf(t, want.maxOutput, models.EffectiveMaxOutputTokens(&def, "copilot"),
				"%s@copilot max output: Copilot reports max_output_tokens %d", def.ID, want.maxOutput)
		}
	}
}
