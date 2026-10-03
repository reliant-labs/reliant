// Copyright (c) 2025 Reliant Labs
package drivererrors

import (
	"fmt"
	"strings"
)

// ErrEmptyInput is a sentinel error for LLM calls that have no usable input.
// Callers can use errors.Is(err, ErrEmptyInput) to classify deterministically.
var ErrEmptyInput = fmt.Errorf("llm request has no input")

// EmptyInputError describes a request that could not be sent because message
// conversion produced no input items.
type EmptyInputError struct {
	Provider     string
	MessageCount int
	PromptCount  int
	ToolCount    int
}

func (e *EmptyInputError) Error() string {
	return fmt.Sprintf(
		"%s request has no input items after message conversion (messages=%d prompts=%d tools=%d)",
		e.Provider,
		e.MessageCount,
		e.PromptCount,
		e.ToolCount,
	)
}

// Is enables errors.Is(err, ErrEmptyInput).
func (e *EmptyInputError) Is(target error) bool {
	return target == ErrEmptyInput
}

// NewEmptyInputError creates a typed empty-input error for request preflight.
func NewEmptyInputError(provider string, messages int, prompts int, tools int) error {
	return &EmptyInputError{
		Provider:     provider,
		MessageCount: messages,
		PromptCount:  prompts,
		ToolCount:    tools,
	}
}

const (
	// ProviderCreditExhaustedSummary is the user-facing summary for provider-side
	// quota / billing exhaustion. It deliberately does not mention Reliant credit:
	// BYO provider keys and provider subscriptions have their own billing
	// relationship, while reliant-managed credit uses a separate marker path.
	ProviderCreditExhaustedSummary = "AI provider quota or credits are exhausted — check provider billing or switch providers"

	// ProviderLongContextCreditsRequiredSummary is more specific than the generic
	// quota summary because the provider is telling the user how to recover:
	// either add credits for long context, or choose a shorter-context model.
	ProviderLongContextCreditsRequiredSummary = "AI provider credits are required for long context requests — add provider credits or choose a shorter-context model"
)

// SummarizeProviderCreditExhaustion recognizes provider-side credit, quota, and
// billing exhaustion messages that are terminal within a Temporal retry ladder.
// It intentionally avoids broad strings such as "remaining credit": stream-stall
// retry guidance uses that wording while describing a retryable transport stall.
func SummarizeProviderCreditExhaustion(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "usage credits are required for long context requests") {
		return ProviderLongContextCreditsRequiredSummary
	}
	if containsProviderCreditExhaustionSignal(lower) {
		return ProviderCreditExhaustedSummary
	}
	return ""
}

// IsProviderCreditExhaustion reports whether err is provider-side billing/quota
// exhaustion. A true result means retrying the same request cannot succeed until
// the user changes billing/quota state or switches providers.
func IsProviderCreditExhaustion(err error) bool {
	if err == nil {
		return false
	}
	return SummarizeProviderCreditExhaustion(err.Error()) != ""
}

func containsProviderCreditExhaustionSignal(lower string) bool {
	for _, signal := range []string{
		"insufficient_quota",
		"quota_exceeded",
		"quota exceeded",
		"out of credits",
		"billing hard limit",
		"payment required",
		"exceeded your current quota",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}
