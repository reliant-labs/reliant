// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The gateway reports cache reads in the OpenAI-schema field and cache writes
// in an extra field LiteLLM adds. Both have to reach llm.TokenUsage or a
// stalled turn cannot be attributed to a cache miss from the logs alone.
func TestUsage_PopulatesCacheReadAndCreation(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model: models.Model{APIModel: "claude-opus-5-5"},
	}}

	// prompt_tokens INCLUDES cached tokens, per LiteLLM's calculate_usage.
	const body = `{
		"id": "chatcmpl-1",
		"choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 50,
			"total_tokens": 1050,
			"prompt_tokens_details": {"cached_tokens": 800},
			"cache_creation_input_tokens": 150
		}
	}`

	var completion openai.ChatCompletion
	if err := json.Unmarshal([]byte(body), &completion); err != nil {
		t.Fatalf("unmarshal completion: %v", err)
	}

	usage := client.usage(completion, 0, cacheCreationInputTokens(completion.Usage))

	if usage.CacheReadInputTokens != 800 {
		t.Errorf("CacheReadInputTokens = %d, want 800", usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != 150 {
		t.Errorf("CacheCreationInputTokens = %d, want 150", usage.CacheCreationInputTokens)
	}
	// prompt_tokens includes the cached portion, so input is the remainder.
	if usage.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want 200 (prompt_tokens - cached_tokens)", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
}
