package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectRetryHint_AppendsToFirstPrompt(t *testing.T) {
	prompts := []string{"You are a helpful assistant.", "Extra context here."}
	result := injectRetryHint(prompts, 3)

	require.Len(t, result, 2)
	// First prompt should contain the retry hint
	assert.Contains(t, result[0], "<system-reminder>")
	assert.Contains(t, result[0], "retry attempt 3")
	assert.Contains(t, result[0], "fewer tool calls")
	// First prompt should still start with original content
	assert.True(t, strings.HasPrefix(result[0], "You are a helpful assistant."))
	// Second prompt should be untouched
	assert.Equal(t, "Extra context here.", result[1])
}

func TestInjectRetryHint_EmptyPrompts(t *testing.T) {
	result := injectRetryHint(nil, 2)

	require.Len(t, result, 1)
	assert.Contains(t, result[0], "<system-reminder>")
	assert.Contains(t, result[0], "retry attempt 2")
}

func TestInjectRetryHint_DoesNotMutateOriginal(t *testing.T) {
	original := []string{"original prompt"}
	result := injectRetryHint(original, 2)

	// Original should not be modified
	assert.Equal(t, "original prompt", original[0])
	// Result should have the hint
	assert.Contains(t, result[0], "<system-reminder>")
}

func TestInjectRetryHint_IncludesAttemptNumber(t *testing.T) {
	for _, attempt := range []int32{2, 3, 5} {
		result := injectRetryHint([]string{"base"}, attempt)
		assert.Contains(t, result[0], "retry attempt "+strings.TrimSpace(
			strings.Replace(result[0][strings.Index(result[0], "retry attempt ")+len("retry attempt "):], " ", "", 1)[:1],
		))
	}
}

func TestInjectRetryHint_ClosingTag(t *testing.T) {
	result := injectRetryHint([]string{"base"}, 2)
	assert.Contains(t, result[0], "</system-reminder>")
}
