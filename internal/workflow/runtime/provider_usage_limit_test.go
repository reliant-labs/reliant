// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// usageLimitErr is the shape CallLLM returns when Anthropic answers 429 with a
// multi-hour Retry-After: the driver's marked sentence inside the stream-error
// wrap, followed by the SDK's own 429 text.
func usageLimitErr() error {
	sentence := chatmarkers.ProviderUsageLimitLead + " (Anthropic, 7-day window) — resets Tue Sep 29 20:00 UTC (in 45h39m). Switch to another provider, or send a message after it resets"
	return fmt.Errorf("failed to stream LLM response: LLM streaming error: %s: %w",
		chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, "2026-09-29T20:00:00Z", sentence),
		errors.New(`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests {"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`))
}

// TestClassifyErrorFailsAProviderUsageLimitAtOnce: the 429 inside would be
// retried as transient; a window measured in hours must fail the turn now so
// the executor shows it and pauses, instead of five attempts of silence.
func TestClassifyErrorFailsAProviderUsageLimitAtOnce(t *testing.T) {
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, classifyError(usageLimitErr()), &appErr)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, "ProviderUsageLimit", appErr.Type())
}

// TestErrorSummaryKeepsTheResetTime: the generic "Rate limited by the AI
// provider" would match first and drop when the limit lifts — the one thing
// the user needs.
func TestErrorSummaryKeepsTheResetTime(t *testing.T) {
	summary := extractLLMErrorSummary(usageLimitErr().Error())
	assert.Contains(t, summary, "7-day window")
	assert.Contains(t, summary, "resets Tue Sep 29 20:00 UTC")
	assert.NotContains(t, summary, "[", "the summary is shown as-is; the marker must not leak into it")
}
