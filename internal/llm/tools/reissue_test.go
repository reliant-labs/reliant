// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only a call that observes state the daemon keeps across a reconnect is sent
// again after its connection dropped; one that may have changed something
// never is, because nothing reports whether its first request ran.
func TestReissueAfterInterruption_OnlyObservations(t *testing.T) {
	for _, tool := range []string{ToolShellList, ToolShellOutput, ToolShellWait, ToolCodeContext} {
		_, ok := ReissueAfterInterruption(tool, `{"process_id":"p"}`, time.Second)
		assert.True(t, ok, "%s observes daemon state and is asked again", tool)
	}
	for _, tool := range []string{
		ShellToolName, ToolShellKill, ToolProveTest, ToolMetadataWriter,
		ToolEdit, ToolWrite, "mcp__github__create_issue", "mcp__serena__find_symbol",
	} {
		_, ok := ReissueAfterInterruption(tool, `{}`, time.Second)
		assert.False(t, ok, "%s may already have run and is never sent twice", tool)
	}
}

// An observation is sent with its input untouched.
func TestReissueAfterInterruption_KeepsTheInput(t *testing.T) {
	in := `{"process_id":"p-1","tail":100}`
	out, ok := ReissueAfterInterruption(ToolShellOutput, in, 5*time.Minute)
	require.True(t, ok)
	assert.Equal(t, in, out)
}

// shell_wait asks again for only what is left of the budget it was given, so
// a wait interrupted minutes in still answers "still running, call again"
// before the executor's ceiling cancels it.
func TestReissueAfterInterruption_ShellWaitKeepsItsBudget(t *testing.T) {
	params := func(t *testing.T, input string) map[string]any {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(input), &m))
		return m
	}

	out, ok := ReissueAfterInterruption(ToolShellWait, `{"process_id":"p-1","timeout_seconds":600,"tail_lines":20}`, 4*time.Minute)
	require.True(t, ok)
	got := params(t, out)
	assert.Equal(t, float64(360), got["timeout_seconds"])
	assert.Equal(t, "p-1", got["process_id"])
	assert.Equal(t, float64(20), got["tail_lines"], "the rest of the input is untouched")

	// No budget named: the default one, minus what was used.
	out, ok = ReissueAfterInterruption(ToolShellWait, `{"process_id":"p-1"}`, 5*time.Minute)
	require.True(t, ok)
	assert.Equal(t, (shellWaitDefaultTimeout - 5*time.Minute).Seconds(), params(t, out)["timeout_seconds"])

	// A budget over the maximum counts as the maximum.
	out, ok = ReissueAfterInterruption(ToolShellWait, `{"process_id":"p-1","timeout_seconds":99999}`, time.Minute)
	require.True(t, ok)
	assert.Equal(t, (shellWaitMaxTimeout - time.Minute).Seconds(), params(t, out)["timeout_seconds"])

	// Past the budget, the call reports the process's state at once.
	out, ok = ReissueAfterInterruption(ToolShellWait, `{"process_id":"p-1","timeout_seconds":30}`, time.Minute)
	require.True(t, ok)
	assert.Equal(t, float64(1), params(t, out)["timeout_seconds"])

	_, ok = ReissueAfterInterruption(ToolShellWait, `not json`, time.Second)
	assert.False(t, ok, "an input it cannot rewrite is not re-sent")
}
