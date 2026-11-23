// Copyright (c) 2026 Reliant Labs
package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A workflow that needs Claude Code and Codex signed in on its machine. The
// scenario's machine has none to check (PreflightRequirements is stubbed), so
// it proves what a scenario can: requires.cli_login parses, validates, and the
// run goes on to its nodes. The unmet path is the requirements and activity
// unit tests'.
const cliLoginYAML = `
name: review-with-vendor-clis
requires:
  tools: [git]
  cli_login: [claude, codex]
entry: [review]
nodes:
  - id: review
    type: run
    timeout: 15m
    args:
      credentials: inherit
      command: "git diff origin/main | claude -p 'Review this diff' > review.md && codex exec 'Summarise review.md'"
  - id: done
    type: save_message
    args: {role: assistant, content: "exit {{ nodes.review.exit_code }}"}
edges:
  - from: review
    default: done
`

func TestScenario_RequiresCLILoginParsesAndTheRunProceeds(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(cliLoginYAML))
	require.NoError(t, err)
	assert.Equal(t, []string{"claude", "codex"}, wf.GetRequires().GetCliLogin())
	for _, e := range validation.StaticAnalysis(wf, nil).Errors() {
		assert.NotContains(t, e.Message, "cli_login", "a known vendor validates")
	}

	res := NewRunner(wf).Run(&scenario.Scenario{Name: "signed in", Events: []scenario.SimulatedEvent{
		{Node: "review", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Contains(t, res.Execution.NodesCompleted, "done")
}

// An unknown vendor is refused when the definition is saved, naming the
// valid ones.
func TestScenario_RequiresCLILoginUnknownVendorFailsValidation(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(strings.Replace(cliLoginYAML, "[claude, codex]", "[claud]", 1)))
	require.NoError(t, err)
	var msgs []string
	for _, e := range validation.StaticAnalysis(wf, nil).Errors() {
		msgs = append(msgs, e.Message)
	}
	assert.Contains(t, msgs, `requires.cli_login: "claud" is not a vendor CLI (claude, codex, gemini, cursor, copilot)`)
}
