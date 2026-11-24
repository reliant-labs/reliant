// Copyright (c) 2026 Reliant Labs

package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Gemini CLI and Cursor Agent under credentials: none: still signed in this
// release, signed out from the next minor (owner, 2026-11-23, "with a warning
// first"). One warning names every such CLI the command calls, bare or by a
// path; a comment, an echoed word or credentials: inherit is clean.
func TestCredentialsNoneWarnsGeminiAndCursorOfTheNextMinor(t *testing.T) {
	src := func(credentials, command string) Input {
		wf, err := wfyaml.ParseWorkflow([]byte(`name: fixture
entry: [review]
nodes:
- id: review
  type: run
  timeout: 15m
  args:
    credentials: ` + credentials + `
    command: |
` + command + `
- id: done
  type: save_message
  args: {role: assistant, content: 'exit {{ nodes.review.exit_code }}'}
edges:
- {from: review, default: done}
`))
		require.NoError(t, err)
		return Input{Workflow: wf}
	}
	both := "      gemini -p 'Review the diff' > review.md\n      ~/.local/bin/cursor-agent -p 'Summarise it' >> review.md"

	got := findingsOf(Check(src("none", both)), RuleCredentialsNoneSignIn)
	require.Len(t, got, 1)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Contains(t, got[0].Message, "will hide ~/.cursor and ~/.config/cursor, ~/.gemini from the command in the next minor release")
	assert.Contains(t, got[0].Message, "so cursor-agent, gemini will run signed out")
	assert.Equal(t, "run it with credentials: inherit (the CLI then uses its sign-in on the machine)", got[0].Fix)

	assert.Empty(t, findingsOf(Check(src("inherit", both)), RuleCredentialsNoneSignIn))
	assert.Empty(t, findingsOf(Check(src("none", "      # gemini is not called here\n      echo 'run gemini later'")), RuleCredentialsNoneSignIn))
}
