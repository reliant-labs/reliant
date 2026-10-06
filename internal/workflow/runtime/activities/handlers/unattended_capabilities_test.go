// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// An unattended run — a trigger fired it, or it is a sub-agent of one — is not
// handed the tools that change workflows, start standing work, or change
// something through an integration, unless its step names the tool. These
// drive the real call_llm, load_tool and execute_tools, with the set crossing
// history between them (capabilities_flow_test.go).

// turnAs runs one call_llm turn with rtx as the workflow built it.
func (f *noMachineFixture) turnAs(t *testing.T, cfg *reliantv1.ToolsConfig, rtx RuntimeContext) *reliantv1.CallLLMOutput {
	t.Helper()
	rtx.ChatID, rtx.Thread = f.chat.ID, f.chat.ID
	callLLM := NewCallLLMActivity(f.h.Repo(), nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: f.h.Repo()}),
		&staticConfigProvider{}, f.callLLM.driverResolver, f.callLLM.mcpBinder)
	var output CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(callLLM.Execute, ActivityInput{
		Runtime: rtx,
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:       &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: cfg,
		}}},
	}, &output))
	return &output
}

// The builtin agent's shape: a starting bundle by tag, and "*" loadable. The
// workflow tools come in through tag:workflow and http__request (a mutating
// integration action that needs no connection) through tag:integration —
// neither is a name.
func taggedAgentConfig() *reliantv1.ToolsConfig {
	return toolsConfig(tools.PermissionMutating,
		[]string{tools.ToolView, "tag:workflow", "tag:integration"}, []string{"*"}, nil)
}

var (
	editWorkflowCall = message.ToolCall{ID: "call-edit-workflow", Name: tools.ToolEditWorkflow,
		Input: `{"id":"wf","old_string":"a","new_string":"b"}`}
	httpRequestCall = message.ToolCall{ID: "call-http", Name: "http__request",
		Input: `{"url":"https://example.com","method":"POST"}`}
)

// A webhook-fired run is not offered edit_workflow, cannot load it, and is
// refused when it calls it anyway; the same for a mutating integration action.
func TestUnattendedRun_IsNotOfferedLoadedOrRunAuthoringTools(t *testing.T) {
	f := setupNoMachineFixture(t, false)

	recorded := throughHistory(t, f.turnAs(t, taggedAgentConfig(), RuntimeContext{Unattended: true}).GetCapabilities())
	assert.True(t, recorded.GetUnattended(), "the set records that nobody is attending")
	for _, name := range []string{tools.ToolEditWorkflow, tools.ToolCreateWorkflow, tools.ToolWriteWorkflow, "http__request"} {
		assert.NotContains(t, f.driver.capturedTools, name, "%s must not be in the tool array", name)
		assert.NotContains(t, recorded.GetOffered(), name)
	}
	assert.Contains(t, recorded.GetOffered(), tools.ToolListWorkflows, "reading workflows is still offered")
	assert.Contains(t, recorded.GetOffered(), tools.ToolGetWorkflow)

	loaded := f.execute(t, serverToolExecutor(f.h.Repo()), recorded,
		message.ToolCall{ID: "call-load", Name: tools.ToolLoadTool, Input: `{"name":"edit_workflow"}`})
	require.Len(t, loaded.GetToolResults(), 1)
	assert.True(t, loaded.GetToolResults()[0].GetIsError(), "load_tool must refuse it")
	assert.Contains(t, loaded.GetToolResults()[0].GetContent(), "unattended runs can't create or change workflows")
	assert.Empty(t, loaded.GetGrantedTools(), "nothing is granted")

	mock := newMockToolExecutor()
	results := resultsByID(f.execute(t, mock, recorded, editWorkflowCall, httpRequestCall).GetToolResults())
	for id, reason := range map[string]string{
		editWorkflowCall.ID: "unattended runs can't create or change workflows",
		httpRequestCall.ID:  "unattended runs can't take integration actions that change something outside Reliant",
	} {
		require.Contains(t, results, id)
		assert.True(t, results[id].GetIsError(), "%s must be refused", id)
		assert.Contains(t, results[id].GetContent(), reason)
		assert.Equal(t, 0, mock.GetExecutionCount(id), "%s must not run", id)

		row, err := f.h.Repo().GetToolCall(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, core.ToolCallStatusFailed, row.Status)
		require.NotNil(t, row.ErrorMessage)
		assert.Contains(t, *row.ErrorMessage, reason, "the FAILED row records why")
	}
}

// A person's turn in that same chat is attended: the same declaration offers
// the tools, and they run.
func TestPersonsTurnInAnAutomationsChat_IsOfferedAuthoringTools(t *testing.T) {
	f := setupNoMachineFixture(t, false)

	recorded := throughHistory(t, f.turnAs(t, taggedAgentConfig(), RuntimeContext{}).GetCapabilities())
	assert.False(t, recorded.GetUnattended())
	assert.Contains(t, f.driver.capturedTools, tools.ToolEditWorkflow)
	assert.Contains(t, f.driver.capturedTools, "http__request")

	mock := newMockToolExecutor()
	results := resultsByID(f.execute(t, mock, recorded, editWorkflowCall, httpRequestCall).GetToolResults())
	for _, id := range []string{editWorkflowCall.ID, httpRequestCall.ID} {
		assert.False(t, results[id].GetIsError(), "%s: %s", id, results[id].GetContent())
		assert.Equal(t, 1, mock.GetExecutionCount(id))
	}
}

// A step that names the tool keeps it when nobody is attending: its author
// decided the step does that work. Its tagged neighbours stay withheld.
func TestUnattendedRun_ExplicitlyDeclaredToolStillRuns(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating,
		[]string{tools.ToolView, tools.ToolEditWorkflow, "http__request", "tag:workflow"}, nil, nil)

	recorded := throughHistory(t, f.turnAs(t, cfg, RuntimeContext{Unattended: true}).GetCapabilities())
	assert.Equal(t, []string{tools.ToolEditWorkflow, "http__request"}, recorded.GetUnattendedOptIn())
	assert.Contains(t, f.driver.capturedTools, tools.ToolEditWorkflow)
	assert.NotContains(t, f.driver.capturedTools, tools.ToolWriteWorkflow, "reached only through the tag")

	mock := newMockToolExecutor()
	results := resultsByID(f.execute(t, mock, recorded, editWorkflowCall, httpRequestCall).GetToolResults())
	for _, id := range []string{editWorkflowCall.ID, httpRequestCall.ID} {
		assert.False(t, results[id].GetIsError(), "%s: %s", id, results[id].GetContent())
		assert.Equal(t, 1, mock.GetExecutionCount(id))
	}
}

// A sub-agent's turn of an unattended run carries the fact the runtime
// propagated (runtime.TestUnattendedRunsSubAgentInheritsItsCallLLM), and is
// withheld exactly as its parent is.
func TestUnattendedSubAgent_IsWithheldAuthoringTools(t *testing.T) {
	f := setupNoMachineFixture(t, false)

	recorded := throughHistory(t, f.turnAs(t, taggedAgentConfig(),
		RuntimeContext{Unattended: true, SpawnDepth: 1, ParentPermission: tools.PermissionMutating}).GetCapabilities())
	assert.NotContains(t, f.driver.capturedTools, tools.ToolEditWorkflow)

	mock := newMockToolExecutor()
	results := resultsByID(f.execute(t, mock, recorded, editWorkflowCall).GetToolResults())
	assert.True(t, results[editWorkflowCall.ID].GetIsError())
	assert.Contains(t, results[editWorkflowCall.ID].GetContent(), "unattended runs can't")
	assert.Equal(t, 0, mock.GetExecutionCount(editWorkflowCall.ID))
}
