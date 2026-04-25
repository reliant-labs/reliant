package openai

import (
	_ "embed"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ============================================================================
// OPENAI-FAMILY PROMPT HELPERS
// ============================================================================

//go:embed openai_prompt.txt
var openAIFamilyAgentGuidance string

// SupportsOpenAIFamilyGuidance reports whether the model should receive the
// shared GPT-5.x/Codex-style prompt contract.
func SupportsOpenAIFamilyGuidance(model models.Model) bool {
	switch model.ID {
	case models.GPT55, models.GPT54, models.GPT54Mini, models.GPT54Pro, models.GPT53Codex, models.GPT53CodexSpark, models.GPT52Codex, models.GPT52, models.GPT52Pro:
		return true
	}

	return strings.HasPrefix(strings.TrimSpace(model.APIModel), "gpt-5")
}

// OpenAIFamilyAgentGuidance returns the shared GPT-5.4-aligned contract for
// OpenAI-family reasoning and coding models.
func OpenAIFamilyAgentGuidance(model models.Model) string {
	if !SupportsOpenAIFamilyGuidance(model) {
		return ""
	}
	return openAIFamilyAgentGuidance
}

// AppendOpenAIFamilyGuidance appends the shared guidance as a final prompt
// block so request construction can stay consistent across OpenAI and Codex.
func AppendOpenAIFamilyGuidance(prompts []string, model models.Model) []string {
	combined := append([]string(nil), prompts...)
	if guidance := OpenAIFamilyAgentGuidance(model); guidance != "" {
		combined = append(combined, guidance)
	}
	return combined
}
