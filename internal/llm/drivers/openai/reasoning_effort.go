// Copyright (c) 2025 Reliant Labs
package openai

import "github.com/openai/openai-go/v3/shared"

// reasoningEffort maps our internal effort string onto the wire value.
//
// The SDK's ReasoningEffort constants stop at xhigh, but the field is a plain
// string on the wire, so newer levels pass through by construction. gpt-6-astra
// adds "max" above xhigh; the gpt-5.6 family adds "max" and "ultra".
//
// Unknown values are forwarded rather than clamped because the model's declared
// thinking_levels in models.yaml are what validate an effort, upstream of here.
// Clamping again at the driver would silently downgrade a level the catalog had
// already accepted — which is exactly what the three copies of this switch used
// to do to astra's "max", turning it into "medium" with no error anywhere.
func reasoningEffort(effort string) shared.ReasoningEffort {
	switch effort {
	case "low":
		return shared.ReasoningEffortLow
	case "medium":
		return shared.ReasoningEffortMedium
	case "high":
		return shared.ReasoningEffortHigh
	case "":
		return shared.ReasoningEffortMedium
	default:
		return shared.ReasoningEffort(effort)
	}
}
