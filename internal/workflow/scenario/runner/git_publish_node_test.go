// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// A publish step in a worked workflow: an agent makes changes in a worktree with
// no credentials in its shell, git_publish pushes them, and the graph routes on
// what happened.
const gitPublishYAML = `
name: publish-flow
inputs:
  repo: {type: string, default: "acme/app"}
  issue: {type: integer, default: 12}
entry: [wt]
nodes:
  - id: wt
    type: create_worktree
    args:
      name: "issue-{{ inputs.issue }}"
  - id: work
    type: run
    args:
      command: "make fix"
      credentials: none
  - id: publish
    type: git_publish
    args:
      worktree: "{{ nodes.wt.path }}"
      repo: "{{ inputs.repo }}"
      branch: "autopilot/issue-{{ inputs.issue }}"
      allow: ["autopilot/*"]
      message: "Fix issue {{ inputs.issue }}"
  - id: pushed
    type: save_message
    args:
      role: assistant
      content: "pushed {{ nodes.publish.branch }} at {{ nodes.publish.sha }} ({{ nodes.publish.files_changed }} files)"
  - id: nothing
    type: save_message
    args:
      role: assistant
      content: "nothing to publish on {{ nodes.publish.branch }}"
  - id: refused
    type: save_message
    args:
      role: assistant
      content: "refused: {{ nodes.publish.refusal }}"
edges:
  - from: wt
    default: work
  - from: work
    default: publish
  - from: publish
    cases:
      - condition: "nodes.publish.refused == true"
        to: refused
      - condition: "nodes.publish.pushed == true"
        to: pushed
    default: nothing
`

func publishScenario(output map[string]interface{}) *scenario.Scenario {
	return &scenario.Scenario{
		Name: "publish",
		Events: []scenario.SimulatedEvent{
			{Node: "wt", Output: map[string]interface{}{"path": "/work/issue-12", "branch": "worktree/issue-12"}},
			{Node: "work", Output: map[string]interface{}{"exit_code": 0}},
			{Node: "publish", Output: output},
		},
	}
}

func TestGitPublishNode_PushedRoutesOnItsOutputs(t *testing.T) {
	res := runYAMLScenario(t, gitPublishYAML, publishScenario(map[string]interface{}{
		"pushed": true, "branch": "autopilot/issue-12", "sha": "abc1234", "files_changed": 3,
	}))
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Contains(t, res.Execution.NodesCompleted, "publish")
	assert.Equal(t, []string{"pushed autopilot/issue-12 at abc1234 (3 files)"}, savedContent(res, "pushed"))
	assert.Empty(t, savedContent(res, "nothing"))
	assert.Empty(t, savedContent(res, "refused"))
}

// No changes: no push, and not an error. The run carries on down the default edge.
func TestGitPublishNode_NothingToPublishIsNotAnError(t *testing.T) {
	res := runYAMLScenario(t, gitPublishYAML, publishScenario(map[string]interface{}{
		"pushed": false, "branch": "autopilot/issue-12", "sha": "abc1234",
	}))
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, []string{"nothing to publish on autopilot/issue-12"}, savedContent(res, "nothing"))
	assert.Empty(t, savedContent(res, "pushed"))
}

// A refusal (policy or the remote) is an output the graph routes on, not a failed node.
func TestGitPublishNode_RefusalIsAnOutputNotAFailure(t *testing.T) {
	res := runYAMLScenario(t, gitPublishYAML, publishScenario(map[string]interface{}{
		"refused": true, "refusal": `"main" is the repository's default branch`,
	}))
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Equal(t, []string{`refused: "main" is the repository's default branch`}, savedContent(res, "refused"))
	assert.Empty(t, savedContent(res, "pushed"))
}

// An unmocked publish never pushes anything: the runner answers every node-type
// activity with an empty output, so "pushed" is not true and the graph takes its
// default edge. It cannot reach a machine or GitHub from a scenario.
func TestGitPublishNode_UnmockedPublishesNothing(t *testing.T) {
	res := runYAMLScenario(t, gitPublishYAML, &scenario.Scenario{
		Name: "unmocked",
		Events: []scenario.SimulatedEvent{
			{Node: "wt", Output: map[string]interface{}{"path": "/work/issue-12"}},
			{Node: "work", Output: map[string]interface{}{"exit_code": 0}},
		},
	})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Empty(t, savedContent(res, "pushed"))
	assert.Empty(t, savedContent(res, "refused"))
	assert.Len(t, savedContent(res, "nothing"), 1)
}
