// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
)

// reasoningAccumulator collects the reasoning OpenRouter streams on a delta:
// a plain `reasoning` string and/or `reasoning_details[]` entries
// (https://openrouter.ai/docs/use-cases/reasoning-tokens#reasoning-details-api-shape).
// The two carry the same text, so the string wins when present and the
// details are only a fallback; counting both would double every word.
//
// Anthropic models also deliver their thinking signature as a
// `reasoning.text` detail ("signature" field). The docs require passing
// reasoning back on tool-use turns, so it is kept for replay.
type reasoningAccumulator struct {
	text      strings.Builder
	signature string
}

// consume records one delta's reasoning and returns the new thinking text to
// stream ("" when the delta had none).
func (a *reasoningAccumulator) consume(delta map[string]interface{}) string {
	var fromDetails strings.Builder
	if details, ok := delta["reasoning_details"].([]interface{}); ok {
		for _, raw := range details {
			detail, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			switch detail["type"] {
			case "reasoning.text":
				if t, ok := detail["text"].(string); ok {
					fromDetails.WriteString(t)
				}
				if sig, ok := detail["signature"].(string); ok && sig != "" {
					a.signature = sig
				}
			case "reasoning.summary":
				if s, ok := detail["summary"].(string); ok {
					fromDetails.WriteString(s)
				}
			}
		}
	}

	chunk := fromDetails.String()
	if s, ok := delta["reasoning"].(string); ok && s != "" {
		chunk = s
	}
	a.text.WriteString(chunk)
	return chunk
}

// applyReasoning copies accumulated reasoning onto the terminal response.
func (a *reasoningAccumulator) applyReasoning(resp *llm.DriverResponse) {
	resp.Thinking = a.text.String()
	resp.ThinkingSignature = a.signature
}

// parseStreamUsage reads the final-chunk usage object into streamUsage.
func parseStreamUsage(usage map[string]interface{}, streamUsage *llm.TokenUsage) {
	if pt, ok := usage["prompt_tokens"].(float64); ok {
		streamUsage.InputTokens = int64(pt)
	}
	if ct, ok := usage["completion_tokens"].(float64); ok {
		streamUsage.OutputTokens = int64(ct)
	}
	if tt, ok := usage["total_tokens"].(float64); ok {
		streamUsage.TokenCount = int64(tt)
	}
	// reasoning_tokens is a subset of completion_tokens, never additive.
	if details, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
		if rt, ok := details["reasoning_tokens"].(float64); ok {
			streamUsage.ReasoningTokens = int64(rt)
		}
	}
}

// applyClaudeReasoning adds OpenRouter's reasoning config to a Claude request
// and drops temperature, which Anthropic rejects alongside extended thinking.
func (c *Client) applyClaudeReasoning(request map[string]interface{}) {
	effort := c.Options.ReasoningEffort
	if effort == "" || effort == "disabled" {
		return
	}
	request["reasoning"] = map[string]string{"effort": effort}
	delete(request, "temperature")
}
