// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// pr-fix's implementer is a builtin://autopilot-agent thread whose shell is
// bound credentials: none and which works in a per-run clone under
// $XDG_CACHE_HOME. The workflow binds the clone as the shell's
// writable_checkouts, templated from the step that prepared it; these follow
// that value from the parent's YAML to the call the executor dispatches.

const prFixLikeWorkflow = `
name: pr-fix-like
entry: [prepare]
nodes:
  - id: prepare
    type: run
    args:
      command: "echo"
  - id: prep
    type: compute
    outputs:
      p: "{{ json.decode(nodes.prepare.stdout) }}"
  - id: implement
    type: workflow
    ref: builtin://autopilot-agent
    args:
      tools: [shell]
      tool_params:
        shell:
          credentials: none
          writable_checkouts: ["{{ nodes.prep.p.worktree }}"]
`

// agentLikeWorkflow is autopilot-agent's call_llm, as far as tools go: the
// whole tool_params input is the node's tools block.
const agentLikeWorkflow = `
name: agent-like
entry: [turn]
inputs:
  tool_params: {type: object, default: {}}
nodes:
  - id: turn
    type: call_llm
    args:
      model: {tags: [flagship]}
      tools_config:
        filter: [shell]
        tools: "{{ inputs.tool_params }}"
`

func TestBoundWritableCheckouts_FromTheParentsTemplateToTheDispatchedCall(t *testing.T) {
	t.Parallel()
	const clone = "/var/cache/workspace/xdg/pr-fix/control-plane-2120-run-7/control-plane"

	// The parent evaluates the template against the step that prepared the clone.
	parent, err := wfyaml.ParseWorkflow([]byte(prFixLikeWorkflow))
	require.NoError(t, err)
	implement := parent.GetNodes()[2]
	resolved, err := runtime.EvaluateNodeConfig(implement,
		map[string]interface{}{"prep": map[string]interface{}{"p": map[string]interface{}{"worktree": clone}}},
		"wf-1", parent.GetName(), nil, nil, nil, nil)
	require.NoError(t, err)
	toolParams, ok := resolved.GetWorkflow().GetArgs()["tool_params"].AsInterface().(map[string]interface{})
	require.True(t, ok, "tool_params: %v", resolved.GetWorkflow().GetArgs()["tool_params"])
	assert.Equal(t, map[string]interface{}{"credentials": "none", "writable_checkouts": []interface{}{clone}}, toolParams["shell"])

	// The agent binds it on its shell, hidden from the model.
	scopes := resolveWithInputs(t, []byte(agentLikeWorkflow), map[string]interface{}{"tool_params": toolParams})
	shell := scopes.For(tools.ShellToolName)
	require.Contains(t, shell, "writable_checkouts")
	bound, problems := toolbindings.Apply([]tools.Tool{tools.NewShellTool(tools.ShellPlatformUnix)}, scopes)
	require.Empty(t, problems)
	_, offered := bound[0].ParamSchema().Properties.Get("writable_checkouts")
	assert.False(t, offered, "the model is not shown it")

	// execute_tools merges the bound value in and says which parameters are bound.
	caps := &tools.Capabilities{BoundParams: map[string]map[string]tools.BoundParam{tools.ShellToolName: {}}}
	for name, value := range shell {
		caps.BoundParams[tools.ShellToolName][name] = tools.BoundParam{Value: value}
	}
	tec := &toolExecutionContext{toolName: tools.ShellToolName, toolInput: `{"command":"git commit -qam fix"}`,
		chat: &db.Chat{ID: "c"}, project: &db.Project{ID: "p", UserID: "u"}, worktree: &rctx.WorktreeInfo{}}
	input, refusal := (&ExecuteToolsActivity{}).boundToolInput(context.Background(), caps, tec)
	require.Empty(t, refusal)
	assert.JSONEq(t, `{"command":"git commit -qam fix","credentials":"none","writable_checkouts":["`+clone+`"]}`, input)
	assert.Equal(t, []string{"credentials", "writable_checkouts"}, tec.boundParams)
	tec.dispatchInput = input
	assert.Equal(t, []string{"credentials", "writable_checkouts"}, tec.buildToolRequest().BoundParams)

	// A model that sets it to anything else is refused.
	tec = &toolExecutionContext{toolName: tools.ShellToolName,
		toolInput: `{"command":"true","writable_checkouts":["/home/workspace/projects/reliant-labs/control-plane"]}`}
	_, refusal = (&ExecuteToolsActivity{}).boundToolInput(context.Background(), caps, tec)
	assert.Contains(t, refusal, "writable_checkouts")
	assert.Empty(t, tec.boundParams)
}

// Existing YAML: a shell bound credentials: none and nothing else binds no
// writable_checkouts, and a model's value for it travels as unbound.
func TestBoundWritableCheckouts_UnboundAsBefore(t *testing.T) {
	t.Parallel()
	scopes := resolveWithInputs(t, []byte(agentLikeWorkflow), map[string]interface{}{
		"tool_params": map[string]interface{}{"shell": map[string]interface{}{"credentials": "none"}},
	})
	shell := scopes.For(tools.ShellToolName)
	assert.NotContains(t, shell, "writable_checkouts")
	caps := &tools.Capabilities{BoundParams: map[string]map[string]tools.BoundParam{
		tools.ShellToolName: {"credentials": {Value: shell["credentials"]}}}}
	tec := &toolExecutionContext{toolName: tools.ShellToolName, toolInput: `{"command":"true","writable_checkouts":["/x"]}`}
	_, refusal := (&ExecuteToolsActivity{}).boundToolInput(context.Background(), caps, tec)
	require.Empty(t, refusal)
	assert.Equal(t, []string{"credentials"}, tec.boundParams, "writable_checkouts is the model's, so not bound")
}
