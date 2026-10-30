package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const requiresYAML = `name: release
requires:
  tools: [git, gh, task]
  integrations: [github]
  repos: [., control-plane]
entry: [done]
nodes:
- id: done
  type: save_message
  args: {role: assistant, content: done}
`

func TestRequiresRoundTrips(t *testing.T) {
	wf, err := ParseWorkflow([]byte(requiresYAML))
	require.NoError(t, err)
	assert.Equal(t, []string{"git", "gh", "task"}, wf.GetRequires().GetTools())
	assert.Equal(t, []string{"github"}, wf.GetRequires().GetIntegrations())
	assert.Equal(t, []string{".", "control-plane"}, wf.GetRequires().GetRepos())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.Equal(t, wf.GetRequires().GetTools(), again.GetRequires().GetTools())
	assert.Equal(t, wf.GetRequires().GetRepos(), again.GetRequires().GetRepos())
}

func TestRequiresRejectsWhatCannotBeChecked(t *testing.T) {
	for name, block := range map[string]string{
		"unknown key": "requires: {binaries: [git]}",
		"not a list":  "requires: {tools: git}",
		"template":    "requires: {tools: ['{{ inputs.tool }}']}",
		"duplicate":   "requires: {tools: [git, git]}",
		"empty":       "requires: {repos: ['']}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(strings.Replace(requiresYAML, "requires:\n  tools: [git, gh, task]\n  integrations: [github]\n  repos: [., control-plane]", block, 1)))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "requires")
		})
	}
}
