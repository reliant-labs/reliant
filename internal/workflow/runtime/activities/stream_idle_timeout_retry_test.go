// Copyright (c) 2025 Reliant Labs
package activities

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// TestStreamIdleTimeoutIsRetryable pins the other half of the stream-idle fix.
//
// Cutting a silent stream is only an improvement if the cut is retried: the
// stalls it replaces were transient (is_terminal=false) and their automatic
// retry succeeded on the first attempt in 12-17s. autoClassify decides that by
// scanning the error text, terminal patterns first, so llm.ErrStreamIdleTimeout
// must miss every terminal pattern and hit a transient one. Reword it and this
// test tells you that you turned a recoverable stall into a failed workflow.
func TestStreamIdleTimeoutIsRetryable(t *testing.T) {
	// Wrapped exactly as call_llm.go reports it.
	wrapped := fmt.Errorf("failed to stream LLM response: %w", llm.ErrStreamIdleTimeout)

	for name, err := range map[string]error{
		"bare":    llm.ErrStreamIdleTimeout,
		"wrapped": wrapped,
	} {
		t.Run(name, func(t *testing.T) {
			classified := ClassifyError(err)
			require.Error(t, classified)

			// A retryable classification is the plain error; a terminal one is a
			// non-retryable temporal.ApplicationError.
			var appErr *temporal.ApplicationError
			assert.NotErrorAs(t, classified, &appErr,
				"a silent stream must be retried, not turned into a non-retryable failure")
			assert.False(t, IsTerminal(classified))
			assert.ErrorIs(t, classified, llm.ErrStreamIdleTimeout,
				"classification must not lose the cause")
			assert.Equal(t, ErrorCategoryNetwork, CategorizeError(err),
				"an idle stream is a network fault and should be reported as one")
		})
	}
}

// TestStalledStreamWithMarkerIsRetryable guards the composed string that the
// user actually gets, not just the sentinel. call_llm.go prefixes the stall
// error with a chatmarkers tail and a human-readable sentence, and
// autoClassify scans that WHOLE string with terminal patterns first. The
// sentence names a subscription and mentions credit, which sits one careless
// edit away from words like "invalid" or "quota exceeded" — either of which
// would turn a retryable stall into a permanently wedged workflow. Reword the
// message and this test tells you before a user finds out.
func TestStalledStreamWithMarkerIsRetryable(t *testing.T) {
	markered := chatmarkers.Wrap(
		chatmarkers.KindProviderStreamStalled,
		"claude-code",
		"the claude-code provider accepted the request but sent no content for 5m0s; retrying. If this repeats, check that the subscription has remaining credit",
	)
	err := fmt.Errorf("failed to stream LLM response: %s: %w", markered, llm.ErrStreamContentStalled)

	classified := ClassifyError(err)
	require.Error(t, classified)

	var appErr *temporal.ApplicationError
	assert.NotErrorAs(t, classified, &appErr,
		"the user-facing stall message must not classify as terminal")
	assert.False(t, IsTerminal(classified))
	assert.ErrorIs(t, classified, llm.ErrStreamContentStalled)

	// The marker must survive classification, or the UI cannot route on it.
	kind, payload, found := chatmarkers.Extract(classified.Error())
	require.True(t, found, "marker must survive error wrapping and classification")
	assert.Equal(t, chatmarkers.KindProviderStreamStalled, kind)
	assert.Equal(t, "claude-code", payload)
}

// TestStreamContentStalledIsRetryable is the same contract for the
// content-stall guard, and the wording is even more fragile here: the message
// describes a provider that sent "only keepalives", and autoClassify checks
// TERMINAL patterns first. Words like "invalid", "unauthorized" or
// "quota exceeded" appearing in this string would wedge the workflow
// permanently instead of retrying — which is precisely the outcome the guard
// exists to prevent, since the stall it catches is transient (the provider
// resumed serving these same chats minutes later).
func TestStreamContentStalledIsRetryable(t *testing.T) {
	wrapped := fmt.Errorf("failed to stream LLM response: %w", llm.ErrStreamContentStalled)

	for name, err := range map[string]error{
		"bare":    llm.ErrStreamContentStalled,
		"wrapped": wrapped,
	} {
		t.Run(name, func(t *testing.T) {
			classified := ClassifyError(err)
			require.Error(t, classified)

			var appErr *temporal.ApplicationError
			assert.NotErrorAs(t, classified, &appErr,
				"a stalled stream must be retried, not turned into a non-retryable failure")
			assert.False(t, IsTerminal(classified))
			assert.ErrorIs(t, classified, llm.ErrStreamContentStalled,
				"classification must not lose the cause")
			assert.Equal(t, ErrorCategoryNetwork, CategorizeError(err),
				"a stalled stream is a network fault and should be reported as one")
		})
	}
}
