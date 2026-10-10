// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// A stalled stream used to be summarized as "Request to the AI provider timed
// out" — matched on the word "timeout" — which named neither the model nor the
// provider and suggested nothing. The stall error now carries its own one-line
// explanation ahead of its chat marker, and that sentence is the summary.
func TestExtractLLMErrorSummary_ProviderStreamStalled(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sentence string
		cause    string
	}{
		"no output at all": {
			sentence: "AI provider stopped responding: claude-5.5-sonnet on anthropic accepted the request but sent no output for 5m0s (usually an exhausted subscription or an overloaded provider)",
			cause:    "llm stream content stall timeout: provider sent only keepalives for 5m0s with no content block open",
		},
		"silent mid-reply": {
			sentence: "AI provider stopped responding: claude-5.5-sonnet on anthropic went silent partway through its reply for 30m0s",
			cause:    "llm stream content stall timeout: a content block stayed open for 30m0s with only keepalives",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The retry-exhaustion row: Temporal's ActivityError frame around
			// CallLLM's (now flattened) error.
			errMsg := "activity error (type: CallLLM, scheduledEventID: 31708, startedEventID: 31929, identity: 70040@host): " +
				"failed to stream LLM response: " + tc.sentence + " [RELIANT_PROVIDER_STREAM_STALLED:anthropic]: " + tc.cause +
				" (type: ProviderStreamStalled, retryable: false)"
			assert.Equal(t, tc.sentence, extractLLMErrorSummary(errMsg))
			// "exhausted subscription" is advice, not a billing signal: a
			// stall must stay on its own retry schedule, never be taken for
			// credit exhaustion and failed at once.
			assert.False(t, drivererrors.IsProviderCreditExhaustion(errors.New(errMsg)))
		})
	}
}
