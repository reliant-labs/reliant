package openai

import (
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
)

// The OpenAI API reports no cost for a request, and reliant holds no price
// table to derive one from, so this driver reports token counts and leaves cost
// at zero. It previously multiplied tokens by catalog rates, which produced a
// number that looked authoritative, was written to messages.cost, and drifted
// silently every time OpenAI changed a price.
func TestUsageReportsNoCost(t *testing.T) {
	client := &OpenaiClient{
		Options: llm.DriverOptions{Model: models.Model{ID: "gpt-6-astra"}},
	}

	completion := openai.ChatCompletion{
		Usage: openai.CompletionUsage{PromptTokens: 2000, CompletionTokens: 500},
	}

	usage := client.usage(completion)

	assert.Equal(t, int64(2000), usage.InputTokens)
	assert.Equal(t, int64(500), usage.OutputTokens)
	assert.Zero(t, usage.Cost, "cost must come from the provider, and OpenAI reports none")
}
