// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func runTimeoutErrors(t *testing.T, nodeType, timeout string) []string {
	t.Helper()
	args := "\n    args:\n      command: ./scripts/gate.sh"
	if nodeType != "run" {
		args = ""
	}
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: run-timeout
entry: [gate]
nodes:
  - id: gate
    type: ` + nodeType + `
    timeout: ` + timeout + args + `
`))
	require.NoError(t, err)
	result := &Result{}
	validateStructure(wf, nil, result)
	var msgs []string
	for _, e := range result.Errors() {
		if e.Field == "timeout" {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

// A run node's timeout is the limit on its command, so a gate that takes hours
// declares hours — far past the agent shell's 10 minutes — and loads.
func TestRunNodeTimeoutPastTenMinutesIsValid(t *testing.T) {
	t.Parallel()
	for _, timeout := range []string{"11m", "3h", "12h"} {
		require.Empty(t, runTimeoutErrors(t, "run", timeout), "timeout %s", timeout)
	}
}

// The limit has a ceiling, enforced at load time for a literal timeout.
func TestRunNodeTimeoutPastTheCeilingIsRejected(t *testing.T) {
	t.Parallel()
	msgs := runTimeoutErrors(t, "run", "13h")
	require.Len(t, msgs, 1)
	require.True(t, strings.Contains(msgs[0], "at most 12h0m0s"), msgs[0])

	// The ceiling is the run node's; other nodes keep their own rules.
	require.Empty(t, runTimeoutErrors(t, "call_llm", "13h"))
}
