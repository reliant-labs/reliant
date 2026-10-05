// Copyright (c) 2025 Reliant Labs
package activities

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

func parseWorkflow(t *testing.T, yaml string) *reliantv1.Workflow {
	t.Helper()
	wf, err := v2.ParseWorkflowProtoBytesNoValidation([]byte(yaml))
	require.NoError(t, err)
	return wf
}

func builtinLoader(t *testing.T) func(string) (*reliantv1.Workflow, error) {
	return func(ref string) (*reliantv1.Workflow, error) {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(strings.TrimPrefix(ref, "builtin://") + ".yaml")
		if err != nil {
			return nil, fmt.Errorf("no builtin %s", ref)
		}
		return parseWorkflow(t, string(data)), nil
	}
}

// Nodes that cannot run at all without a machine are HARD requirements: a
// no-machine run of such a workflow is refused at launch and at trigger write.
func TestMachineRequirements_HardRequirementsNameTheNode(t *testing.T) {
	cfg := newPreflightConfig()
	for name, yaml := range map[string]string{
		"run node": `
name: shell-step
nodes:
  - id: build
    type: run
    args: {command: "make"}
`,
		"create_worktree node": `
name: wt
nodes:
  - id: branch
    type: create_worktree
    args: {name: "x"}
`,
		"invoke_tool of a machine tool": `
name: inv
nodes:
  - id: look
    type: invoke_tool
    args: {tool: view, params: {file_path: README.md}}
`,
		"explicit workflow daemon": `
name: pinned
daemon: {id: d-1}
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: hi}
`,
		"nested inline run": `
name: outer
nodes:
  - id: inner
    type: workflow
    args:
      inline:
        name: inner
        nodes:
          - id: build
            type: run
            args: {command: "make"}
`,
	} {
		t.Run(name, func(t *testing.T) {
			req := v2.MachineRequirements(parseWorkflow(t, yaml), nil, nil, cfg)
			require.NotEmpty(t, req.Hard, "%s must hard-require a machine", name)
		})
	}
}

// Statically known machine tools are reported separately from hard
// requirements: a chat may still run (filtered) but an automation is refused.
func TestMachineRequirements_ToolListsAreReportedSeparately(t *testing.T) {
	cfg := newPreflightConfig()

	literal := parseWorkflow(t, `
name: lit
nodes:
  - id: agent
    type: call_llm
    args:
      model: {id: mock}
      tools_config:
        preloaded_tools: ["tag:shell", "fetch"]
`)
	req := v2.MachineRequirements(literal, nil, nil, cfg)
	assert.Empty(t, req.Hard)
	require.NotEmpty(t, req.Tools)
	assert.Contains(t, strings.Join(req.Tools, " "), "shell")
	assert.NotContains(t, strings.Join(req.Tools, " "), "fetch", "fetch runs without a machine")

	// The builtin agent's tools come from its `tools` input. At its default
	// (tag:coding:default) it reaches the shell and the file tools; with
	// tools: [tag:web] it needs no machine at all.
	agent, err := builtinLoader(t)("builtin://agent")
	require.NoError(t, err)
	atDefault := v2.MachineRequirements(agent, nil, builtinLoader(t), cfg)
	assert.Empty(t, atDefault.Hard, "the agent has no node that hard-requires a machine")
	require.NotEmpty(t, atDefault.Tools, "the agent's default tools need a machine")
	assert.Contains(t, strings.Join(atDefault.Tools, " "), "input `tools`")

	webOnly := v2.MachineRequirements(agent, map[string]any{"tools": []any{"tag:web"}}, builtinLoader(t), cfg)
	assert.True(t, webOnly.None(), "agent with tools [tag:web] needs no machine, got %+v", webOnly)
}

// Workflows that only reach server work need nothing.
func TestMachineRequirements_ServerOnlyWorkflowNeedsNothing(t *testing.T) {
	cfg := newPreflightConfig()
	wf := parseWorkflow(t, `
name: digest
nodes:
  - id: research
    type: call_llm
    args:
      model: {id: mock}
      tools_config:
        preloaded_tools: ["tag:web", "tag:planning"]
        loadable_tools: ["tag:web"]
  - id: post
    type: action
    args:
      uses: http/request@1
      with: {method: GET, url: "https://example.com"}
`)
	assert.True(t, v2.MachineRequirements(wf, nil, nil, cfg).None())
}

// A ref to a sub-workflow is followed through the loader, so a workflow that
// delegates its shell work is not mistaken for a server-only one.
func TestMachineRequirements_FollowsRefs(t *testing.T) {
	cfg := newPreflightConfig()
	wf := parseWorkflow(t, `
name: wrapper
nodes:
  - id: compete
    type: workflow
    args: {ref: "builtin://parallel-compete"}
`)
	assert.NotEmpty(t, v2.MachineRequirements(wf, nil, builtinLoader(t), cfg).Hard)
}

// RequiresDaemon runs inside workflow code, so its answer is part of replay. It
// must not change for any workflow because of this analysis.
func TestRequiresDaemonIsUnchangedByTheNoMachineAnalysis(t *testing.T) {
	cfg := newPreflightConfig()
	viewOnly := callLLMWorkflow("view")
	assert.False(t, v2.RequiresDaemon(viewOnly, cfg), "view is any-placed; RequiresDaemon keeps saying so")
	assert.NotEmpty(t, v2.MachineRequirements(viewOnly, nil, nil, cfg).Tools,
		"…while the no-machine analysis knows view reads the user's disk")
}
