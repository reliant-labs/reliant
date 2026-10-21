// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deniedLoopWorkflow / deniedOnceScenario are a shape the retired graph
// simulator and the real runtime disagreed on: after an approval is DENIED
// the runtime re-enters the loop and calls the LLM again, while the simulator
// ended it. The scenario supplies only the simulator's single call_llm event,
// so it PASSED on the simulator and must fail on the runtime with an
// exhausted-mocks mismatch — the proof that `reliant workflow scenario run`
// executes on the runtime.
const deniedLoopWorkflow = `name: denied-loop
apiVersion: "1.0"
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: size(outputs.tool_calls) > 0
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: {tags: [fast]}
        - id: approval
          type: approval
          args:
            title: Approve?
        - id: execute_tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: approval
              condition: size(nodes.call_llm.tool_calls) > 0
        - from: approval
          cases:
            - to: execute_tools
              condition: nodes.approval.status == 'approved'
`

const deniedOnceScenario = `name: denied_once
events:
  - node: agent_loop.call_llm
    output:
      response_text: rm it
      tool_calls:
        - {id: c1, name: shell, input: "{}"}
  - node: agent_loop.approval
    output: {status: denied}
expect:
  outcome: completed
`

func TestWorkflowScenarioRun_ExecutesOnTheRuntime(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "denied-loop.yaml"), []byte(deniedLoopWorkflow), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "denied-loop", "scenarios"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "denied-loop", "scenarios", "denied_once.yaml"), []byte(deniedOnceScenario), 0o644))

	workflows, err := discoverWorkflowsWithScenarios([]string{dir}, "", false)
	require.NoError(t, err)
	require.Len(t, workflows, 1)

	r, err := loadScenarioRunner(workflows[0])
	require.NoError(t, err)
	res := r.Run(workflows[0].Scenarios[0])

	assert.Equal(t, wfscenario.StatusFailed, res.Status)
	assert.Contains(t, strings.Join(res.Mismatches, "\n"),
		`scenario exhausted its mocks for node "agent_loop.call_llm"`,
		"the runtime re-enters the loop after a denial; the retired simulator did not")

	// The command's own exit status reflects it.
	err = runWorkflowScenarios(nil, []string{dir}, "", false, false, true, false, "")
	require.Error(t, err)
}

// A project workflow's project:// refs resolve among the workflows beside it,
// so a scenario can open a referenced workflow's body from the CLI.
func TestWorkflowScenarioRun_ResolvesProjectRefsFromSiblings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parent.yaml"), []byte(`name: parent
apiVersion: "1.0"
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: project://child
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.yaml"), []byte(`name: child
apiVersion: "1.0"
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: {tags: [fast]}
outputs:
  response_text: "{{nodes.draft.response_text}}"
`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "parent", "scenarios"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parent", "scenarios", "opens_child.yaml"), []byte(`name: opens_child
events:
  - node: child.draft
    output: {response_text: done}
expect:
  outcome: completed
  reached: [child, child.draft]
`), 0o644))

	workflows, err := discoverWorkflowsWithScenarios([]string{filepath.Join(dir, "parent.yaml")}, "", false)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	r, err := loadScenarioRunner(workflows[0])
	require.NoError(t, err)
	res := r.Run(workflows[0].Scenarios[0])
	assert.Equal(t, wfscenario.StatusPassed, res.Status, "%v", res.Mismatches)
}

// `--include-builtins` runs the scenarios the binary carries: every file under
// a builtin workflow's <slug>/scenarios/ directory. It used to read only the
// retired testdata/<workflow>_scenarios.yaml files, so the scenario
// directories — embedded, and run by go test — were invisible to the CLI.
func TestWorkflowScenarioDiscovery_IncludesEmbeddedBuiltinScenarioDirs(t *testing.T) {
	workflows, err := discoverWorkflowsWithScenarios(nil, t.TempDir(), true)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, wf := range workflows {
		require.Equal(t, "builtin", wf.Source, "an empty project contributes nothing")
		for _, sc := range wf.Scenarios {
			got[wf.WorkflowName+"/"+sc.Name] = true
		}
	}

	files, err := fs.Glob(builtin.BuiltinScenarioDirsFS, "*/scenarios/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		data, err := fs.ReadFile(builtin.BuiltinScenarioDirsFS, f)
		require.NoError(t, err)
		sc, err := wfscenario.ParseScenarioFile(data, strings.TrimSuffix(path.Base(f), ".yaml"))
		require.NoError(t, err, f)
		key := strings.SplitN(f, "/", 2)[0] + "/" + sc.Name
		assert.True(t, got[key], "%s is embedded but `scenario list --include-builtins` does not list %s", f, key)
	}
}
