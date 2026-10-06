// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

func requestMachineCtx(noMachine bool) *rctx.ToolContext {
	ctx := context.Background()
	if noMachine {
		ctx = nomachine.With(ctx)
	}
	return rctx.NewToolContext(ctx, "chat-request-machine", "0", nil, nil)
}

// In a run with no machine the call returns at once and tells the model to
// stop: the offer the user sees is the rendered call, not anything this does.
func TestRequestMachine_TellsTheModelToStopAndWait(t *testing.T) {
	tool := &requestMachineTool{}
	resp, err := tool.Execute(requestMachineCtx(true), RequestMachineParams{Reason: "Running the tests needs a checkout."})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "Connect a machine")
	assert.Contains(t, resp.Content, "Stop here and wait")
}

func TestRequestMachine_RequiresAReason(t *testing.T) {
	tool := &requestMachineTool{}
	resp, err := tool.Execute(requestMachineCtx(true), RequestMachineParams{Reason: "  "})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
}

// On a machine there is nothing to request, and the call says so rather than
// putting a "Connect a machine" card in front of a user who has one.
func TestRequestMachine_RefusesOnAMachine(t *testing.T) {
	tool := &requestMachineTool{}
	resp, err := tool.Execute(requestMachineCtx(false), RequestMachineParams{Reason: "needs files"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "already has a machine")
}

// No filter grants it, however it is spelled: the no-machine narrowing in
// call_llm is the only thing that hands it over.
func TestRequestMachine_NoFilterGrantsIt(t *testing.T) {
	for _, filter := range [][]string{{ToolRequestMachine}, {"*"}, {"request_*"}} {
		assert.NotContains(t, ExpandToolFilter(filter, nil), ToolRequestMachine, "filter %v", filter)
	}
	assert.True(t, OnlyWithoutMachine(ToolRequestMachine))
	assert.False(t, NeedsMachine(ToolRequestMachine), "it runs on the server")
}

// load_tool neither advertises nor loads it, so a grant can never outlive the
// run's no-machine state (a chat connected to a machine keeps its loaded
// tools' scope).
func TestRequestMachine_IsNeverLoadable(t *testing.T) {
	for _, r := range SearchTools("machine", PermissionOrchestrator, nil) {
		assert.NotEqual(t, ToolRequestMachine, r.Name, "load_tool search must not surface it")
	}

	tool := &loadToolTool{}
	for _, noMachine := range []bool{false, true} {
		ctx := newLoadToolTestCtx(t, PermissionOrchestrator)
		if noMachine {
			ctx.Context = nomachine.With(ctx.Context)
		}
		resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolRequestMachine})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "noMachine=%v: load_tool must refuse it", noMachine)
		assert.NotContains(t, GetLoadedToolsStore().Get(Scope(GetChatID(ctx), ctx.Thread)), ToolRequestMachine)
	}
}
