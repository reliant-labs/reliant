// Copyright (c) 2025 Reliant Labs
package activities

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErrorClassification_RateLimit verifies that 429 rate limit errors
// are classified as transient (retryable), not terminal. This is critical
// because rate limit errors must exhaust Temporal retries (triggering
// RetryExhausted) rather than being treated as terminal errors that kill
// the workflow immediately.
func TestErrorClassification_RateLimit(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		isTerminal bool
		category   ErrorCategory
	}{
		{
			name:       "429 Too Many Requests is retryable",
			err:        errors.New(`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests`),
			isTerminal: false,
			category:   ErrorCategoryRateLimit,
		},
		{
			name:       "rate_limit_error is retryable",
			err:        errors.New(`rate_limit_error: rate limit exceeded`),
			isTerminal: false,
			category:   ErrorCategoryRateLimit,
		},
		{
			name:       "too many requests is retryable",
			err:        errors.New("too many requests"),
			isTerminal: false,
			category:   ErrorCategoryRateLimit,
		},
		{
			name:       "full anthropic 429 error with JSON body is retryable",
			err:        errors.New(`failed to stream LLM response: LLM streaming error: POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests (Request-ID: req_123) {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`),
			isTerminal: false,
			category:   ErrorCategoryRateLimit,
		},
		{
			name:       "rate limit with retry-after header info",
			err:        errors.New("rate limit exceeded, retry after 7200 seconds"),
			isTerminal: false,
			category:   ErrorCategoryRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := ClassifyError(tt.err)
			require.NotNil(t, classified)
			assert.Equal(t, tt.isTerminal, IsTerminal(classified),
				"expected IsTerminal=%v for: %s", tt.isTerminal, tt.err)

			category := CategorizeError(tt.err)
			assert.Equal(t, tt.category, category,
				"expected category=%s for: %s", tt.category, tt.err)
		})
	}
}

func TestErrorClassification_DNS(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		isTerminal bool
		category   ErrorCategory
	}{
		{
			name:       "DNS no such host is retryable",
			err:        errors.New(`dial tcp: lookup api.anthropic.com: no such host`),
			isTerminal: false,
			category:   ErrorCategoryNetwork,
		},
		{
			name:       "dial tcp connection refused is retryable",
			err:        errors.New(`dial tcp 1.2.3.4:443: connect: connection refused`),
			isTerminal: false,
			category:   ErrorCategoryNetwork,
		},
		{
			name:       "full DNS error chain is retryable",
			err:        errors.New(`failed to stream LLM response: LLM streaming error: Post "https://api.anthropic.com/v1/messages?beta=true": dial tcp: lookup api.anthropic.com: no such host`),
			isTerminal: false,
			category:   ErrorCategoryNetwork,
		},
		{
			name:       "local gcloud reauthentication required is terminal",
			err:        errors.New(`litellm.APIConnectionError: Reauthentication is needed. Please run gcloud auth application-default login to reauthenticate.`),
			isTerminal: true,
			category:   ErrorCategoryTerminal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := ClassifyError(tt.err)
			require.NotNil(t, classified)
			assert.Equal(t, tt.isTerminal, IsTerminal(classified),
				"expected IsTerminal=%v for: %s", tt.isTerminal, tt.err)

			category := CategorizeError(tt.err)
			assert.Equal(t, tt.category, category,
				"expected category=%s for: %s", tt.category, tt.err)
		})
	}
}