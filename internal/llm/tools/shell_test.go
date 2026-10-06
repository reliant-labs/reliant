// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFitShellOutputToBudget verifies that a large shell result is shrunk so its
// JSON envelope stays within MaxOutputSize. Before this, the shell tool emitted
// a JSON envelope larger than MaxOutputSize and the generic tool_wrapper
// truncation head+tail-cut the JSON string, corrupting the envelope.
func TestFitShellOutputToBudget(t *testing.T) {
	t.Parallel()
	// ~44KB stdout + ~20KB stderr: comfortably over the budget once encoded.
	stdout := strings.Repeat("stdout line of output\n", 2000)
	stderr := strings.Repeat("stderr warning line\n", 1000)

	o, e := fitShellOutputToBudget(stdout, stderr, len(stdout), len(stderr), 0, MaxOutputSize)

	encoded, err := json.Marshal(ShellOutput{Stdout: o, Stderr: e, ExitCode: 0})
	require.NoError(t, err)

	// Must fit the budget so tool_wrapper leaves it untouched.
	assert.LessOrEqualf(t, len(encoded), MaxOutputSize,
		"encoded bash output (%d bytes) must fit MaxOutputSize (%d)", len(encoded), MaxOutputSize)

	// The wrapper's size check must agree that no further truncation is needed —
	// this is the exact condition that previously corrupted the JSON.
	_, wrapperTruncated, sizeErr := CheckOutputSize(ShellToolName, string(encoded))
	assert.False(t, wrapperTruncated, "tool_wrapper should not re-truncate a budgeted bash result")
	assert.NoError(t, sizeErr)

	// Result must remain valid, round-trippable JSON.
	var rt ShellOutput
	require.NoError(t, json.Unmarshal(encoded, &rt), "budgeted bash output must be valid JSON")

	// Head+tail preservation: both streams keep their (identical) content lines.
	assert.Contains(t, o, "stdout line of output", "stdout head/tail should be preserved")
	assert.Contains(t, e, "stderr warning line", "stderr head/tail should be preserved")
	assert.Contains(t, o, "lines truncated", "stdout should carry a truncation marker")
}

// TestFitShellOutputToBudget_SmallOutputUnchanged verifies output that already
// fits is returned verbatim (no needless truncation markers).
func TestFitShellOutputToBudget_SmallOutputUnchanged(t *testing.T) {
	t.Parallel()
	stdout := "hello world\n"
	stderr := "a warning\n"

	o, e := fitShellOutputToBudget(stdout, stderr, MaxShellOutputLength, MaxShellOutputLength/2, 0, MaxOutputSize)

	assert.Equal(t, stdout, o)
	assert.Equal(t, stderr, e)
}

// TestFitShellOutputToBudget_OneMarkerCountingTheRealOutput: a stream cut by
// BOTH its per-stream cap and the JSON budget carries one marker, and that
// marker counts lines of the command's real output.
//
// The bug this pins: the caller capped each stream first and then fitted the
// already-capped text to the budget. The second cut removed the first cut's
// marker along with the middle of the text, and its own marker counted only
// the lines removed from the capped copy. A workflow run step that printed
// thousands of `rsync --delete` lines was stored with "[17 lines truncated]".
func TestFitShellOutputToBudget_OneMarkerCountingTheRealOutput(t *testing.T) {
	t.Parallel()
	const total = 5000
	var b strings.Builder
	for i := range total {
		fmt.Fprintf(&b, "deleting private/var/folders/T/item-%05d/\n", i)
	}
	stdout := b.String() // ~200KB: over both the 16KB cap and the 24KB budget
	stderr := strings.Repeat("rsync: delete_file: unlink failed: Operation not permitted (1)\n", 2000)

	o, e := fitShellOutputToBudget(stdout, stderr, MaxShellOutputLength, MaxShellOutputLength/2, 0, MaxOutputSize)

	encoded, err := json.Marshal(ShellOutput{Stdout: o, Stderr: e})
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), MaxOutputSize, "the cut must still fit the budget")

	markers := regexp.MustCompile(`\.\.\. \[(\d+) lines truncated\] \.\.\.`).FindAllStringSubmatch(o, -1)
	require.Len(t, markers, 1, "a stream carries exactly one truncation marker")
	hidden, err := strconv.Atoi(markers[0][1])
	require.NoError(t, err)
	shown := strings.Count(o, "item-")
	// Lines shown plus lines the marker says are hidden must account for the
	// whole output. The head and the tail can each end mid-line, so a line can
	// be counted on both sides of a cut: allow one per side.
	assert.InDelta(t, total, shown+hidden, 2,
		"shown %d + hidden %d must account for all %d lines of the output", shown, hidden, total)
}

// TestFitShellOutputToBudget_LargeStdoutEmptyStderr covers the common case of a
// chatty command with no stderr.
func TestFitShellOutputToBudget_LargeStdoutEmptyStderr(t *testing.T) {
	t.Parallel()
	stdout := strings.Repeat("x", 60000)

	o, e := fitShellOutputToBudget(stdout, "", len(stdout), 0, 1, MaxOutputSize)

	encoded, err := json.Marshal(ShellOutput{Stdout: o, Stderr: e, ExitCode: 1})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), MaxOutputSize)

	var rt ShellOutput
	require.NoError(t, json.Unmarshal(encoded, &rt))
}
