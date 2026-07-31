// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFitBashOutputToBudget verifies that a large bash result is shrunk so its
// JSON envelope stays within MaxOutputSize. Before this, the shell tool emitted
// a JSON envelope larger than MaxOutputSize and the generic tool_wrapper
// truncation head+tail-cut the JSON string, corrupting the envelope.
func TestFitBashOutputToBudget(t *testing.T) {
	// ~44KB stdout + ~20KB stderr: comfortably over the budget once encoded.
	stdout := strings.Repeat("stdout line of output\n", 2000)
	stderr := strings.Repeat("stderr warning line\n", 1000)

	o, e := fitBashOutputToBudget(stdout, stderr, 0, MaxOutputSize)

	encoded, err := json.Marshal(BashOutput{Stdout: o, Stderr: e, ExitCode: 0})
	require.NoError(t, err)

	// Must fit the budget so tool_wrapper leaves it untouched.
	assert.LessOrEqualf(t, len(encoded), MaxOutputSize,
		"encoded bash output (%d bytes) must fit MaxOutputSize (%d)", len(encoded), MaxOutputSize)

	// The wrapper's size check must agree that no further truncation is needed —
	// this is the exact condition that previously corrupted the JSON.
	_, wrapperTruncated, sizeErr := CheckOutputSize("bash", string(encoded))
	assert.False(t, wrapperTruncated, "tool_wrapper should not re-truncate a budgeted bash result")
	assert.NoError(t, sizeErr)

	// Result must remain valid, round-trippable JSON.
	var rt BashOutput
	require.NoError(t, json.Unmarshal(encoded, &rt), "budgeted bash output must be valid JSON")

	// Head+tail preservation: both streams keep their (identical) content lines.
	assert.Contains(t, o, "stdout line of output", "stdout head/tail should be preserved")
	assert.Contains(t, e, "stderr warning line", "stderr head/tail should be preserved")
	assert.Contains(t, o, "lines truncated", "stdout should carry a truncation marker")
}

// TestFitBashOutputToBudget_SmallOutputUnchanged verifies output that already
// fits is returned verbatim (no needless truncation markers).
func TestFitBashOutputToBudget_SmallOutputUnchanged(t *testing.T) {
	stdout := "hello world\n"
	stderr := "a warning\n"

	o, e := fitBashOutputToBudget(stdout, stderr, 0, MaxOutputSize)

	assert.Equal(t, stdout, o)
	assert.Equal(t, stderr, e)
}

// TestFitBashOutputToBudget_LargeStdoutEmptyStderr covers the common case of a
// chatty command with no stderr.
func TestFitBashOutputToBudget_LargeStdoutEmptyStderr(t *testing.T) {
	stdout := strings.Repeat("x", 60000)

	o, e := fitBashOutputToBudget(stdout, "", 1, MaxOutputSize)

	encoded, err := json.Marshal(BashOutput{Stdout: o, Stderr: e, ExitCode: 1})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), MaxOutputSize)

	var rt BashOutput
	require.NoError(t, json.Unmarshal(encoded, &rt))
}
