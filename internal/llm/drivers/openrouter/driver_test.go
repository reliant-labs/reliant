// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	openaidriver "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected message.FinishReason
	}{
		{"stop", message.FinishReasonEndTurn},
		{"length", message.FinishReasonMaxTokens},
		{"tool_calls", message.FinishReasonToolUse},
		{"content_filter", message.FinishReasonRefusal},
		{"something_unknown", message.FinishReasonUnknown},
		{"", message.FinishReasonUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapFinishReason(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 529}
	for _, code := range retryable {
		assert.True(t, isRetryableStatus(code), "expected %d to be retryable", code)
	}

	nonRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range nonRetryable {
		assert.False(t, isRetryableStatus(code), "expected %d to not be retryable", code)
	}
}

func TestIsAnthropicModel(t *testing.T) {
	tests := []struct {
		apiModel string
		expected bool
	}{
		{"anthropic/claude-opus-4.6", true},
		{"anthropic/claude-sonnet-4.5", true},
		{"google/gemini-2.5-pro", false},
		{"openai/gpt-5.4", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.apiModel, func(t *testing.T) {
			c := &Client{
				OpenaiClient: &openaidriver.OpenaiClient{
					Options: llm.DriverOptions{
						Model: models.Model{APIModel: tt.apiModel},
					},
				},
			}
			assert.Equal(t, tt.expected, c.isAnthropicModel())
		})
	}
}
