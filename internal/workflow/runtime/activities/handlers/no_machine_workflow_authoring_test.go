// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// workflowAuthoringTools are what an agent needs to build a workflow from the
// editor's chat: read the schema and the workflow, find an integration action,
// and write the result. All of them run on the server.
var workflowAuthoringTools = []string{
	tools.ToolGetSchema, tools.ToolGetWorkflow,
	tools.ToolSearchIntegrations, tools.ToolGetIntegrationSchema,
	tools.ToolCreateWorkflow, tools.ToolEditWorkflow, tools.ToolWriteWorkflow,
}

// The workflow editor's chat is an ordinary chat on the builtin agent
// (research/WORKFLOW_BUILDER_NORMAL_CHAT.md), and it starts with no machine
// when the user has no usable one (research/NO_MACHINE_CHATS.md §2.1). Building
// a workflow has to work there, because none of it touches the user's computer:
//
//   - the no-machine narrowing keeps the authoring tools in load_tool's reach
//     and in the list it advertises;
//   - load_tool(name="tag:workflow"), which the workflow-builder skill tells a
//     normal chat to call, grants them;
//   - the next turn offers them to the model.
//
// The launch half (validateNoMachine lets builtin://agent start with no
// machine) is TestLaunchNoMachineRecordsItOnTheChat in internal/launch.
func TestNoMachineChat_AgentCanLoadTheWorkflowAuthoringTools(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	// builtin/agent.yaml's call_llm in auto mode: preloaded inputs.tools
	// (default tag:coding:default), loadable "*", permission mutating.
	cfg := toolsConfig(tools.PermissionMutating, []string{"tag:coding:default"}, []string{"*"}, nil)

	turnN := f.turn(t, cfg, nil)
	recordedN := throughHistory(t, turnN.GetCapabilities())
	capsN := tools.CapabilitiesFromProto(recordedN)
	require.True(t, capsN.NoMachine, "precondition: the chat has no machine")
	require.True(t, capsN.Offers(tools.ToolLoadTool), "load_tool is how a normal chat reaches the workflow tools")
	for _, name := range workflowAuthoringTools {
		assert.True(t, capsN.CanLoad(name), "%s must be loadable with no machine: %s", name, capsN.LoadRefusal(name))
		assert.Contains(t, capsN.Deferred(), name, "load_tool must advertise %s", name)
	}

	executed := f.execute(t, serverToolExecutor(f.h.Repo()), recordedN,
		message.ToolCall{ID: "call-load", Name: tools.ToolLoadTool, Input: `{"name":"tag:workflow"}`})
	results := resultsByID(executed.GetToolResults())
	require.False(t, results["call-load"].GetIsError(), results["call-load"].GetContent())
	for _, name := range workflowAuthoringTools {
		assert.Contains(t, executed.GetGrantedTools(), name, "load_tool(tag:workflow) grants %s", name)
	}

	turnN1 := f.turn(t, cfg, executed.GetGrantedTools())
	capsN1 := tools.CapabilitiesFromProto(throughHistory(t, turnN1.GetCapabilities()))
	for _, name := range workflowAuthoringTools {
		assert.True(t, capsN1.Offers(name), "%s is offered on the next turn", name)
		assert.Contains(t, f.driver.capturedTools, name, "the model is handed %s", name)
	}
	for _, name := range f.driver.capturedTools {
		assert.False(t, tools.NeedsMachine(name), "a no-machine turn was handed %q, which needs a machine", name)
	}
}
