package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

type machineCall struct {
	op, user, id, size string
	unattended         bool
}

type fakeMachines struct {
	calls      []machineCall
	activation string
}

func (f *fakeMachines) ListMachines(context.Context, string) ([]MachineSummary, error) {
	return []MachineSummary{
		{ID: "cicd-id", Name: "cicd", Type: "managed", State: "suspended", Size: "xl"},
		{ID: "default-id", Name: "default", Type: "managed", State: "ready", Size: "large"},
	}, nil
}
func (f *fakeMachines) StartMachine(_ context.Context, user, id string, unattended bool) error {
	f.calls = append(f.calls, machineCall{op: "start", user: user, id: id, unattended: unattended})
	return nil
}
func (f *fakeMachines) StopMachine(_ context.Context, user, id string) error {
	f.calls = append(f.calls, machineCall{op: "stop", user: user, id: id})
	return nil
}
func (f *fakeMachines) ResizeMachine(_ context.Context, user, id, size string) error {
	f.calls = append(f.calls, machineCall{op: "resize", user: user, id: id, size: size})
	return nil
}
func (f *fakeMachines) ActivationMachine(context.Context, string) (string, error) {
	return f.activation, nil
}

func machineToolCtx(exec automationpolicy.Execution) *rctx.ToolContext {
	ctx := rctx.NewToolContext(context.Background(), "chat", "thread", nil, nil)
	ctx.Context = automationpolicy.WithExecution(ctx.Context, exec)
	return ctx
}

func newTestMachineTool(f *fakeMachines) Tool {
	own := "default-id"
	return NewManageMachineTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ActiveDaemonID: &own}}, f)
}

// start and resize spend money: they run only with an approval for exactly
// that action, machine and size.
func TestManageMachineCostlyActionsNeedTheirOwnApproval(t *testing.T) {
	for _, tc := range []struct{ input, action, size, title string }{
		{`{"machine_id":"cicd-id","action":"start"}`, "start", "", "Start machine cicd (cicd-id, size xl)"},
		{`{"machine_id":"cicd-id","action":"resize","size":"2xl"}`, "resize", "2xl", "from xl to 2xl"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			f := &fakeMachines{}
			tool := newTestMachineTool(f)
			call := ToolCall{ID: "call", Name: ToolManageMachine, Input: tc.input}
			preview, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
			require.NoError(t, err)
			require.True(t, ask)
			require.Contains(t, preview.Title, tc.title)
			require.Equal(t, machineApprovalHash(tc.action, "cicd-id", tc.size), preview.Hash)

			resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{}), call)
			require.NoError(t, err)
			require.True(t, resp.IsError)
			resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": machineApprovalHash(tc.action, "default-id", tc.size)}}), call)
			require.NoError(t, err)
			require.True(t, resp.IsError, "an approval for another machine does not carry over")
			require.Empty(t, f.calls)

			resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}}), call)
			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, []machineCall{{op: tc.action, user: "owner", id: "cicd-id", size: tc.size}}, f.calls)
		})
	}
}

func TestManageMachinePreviewRefusesAMachineTheUserDoesNotHave(t *testing.T) {
	tool := newTestMachineTool(&fakeMachines{})
	call := ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"someone-elses","action":"start"}`}
	_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
	require.ErrorContains(t, err, "not one of the user's machines")
}

// stop saves money: no card, but never the machine the run executes on.
func TestManageMachineStopNeedsNoCardButSparesTheRunsOwnMachine(t *testing.T) {
	f := &fakeMachines{}
	tool := newTestMachineTool(f)
	call := ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"cicd-id","action":"stop"}`}
	_, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
	require.NoError(t, err)
	require.False(t, ask)
	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{}), call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"default-id","action":"stop"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "would stop this run")
	require.Equal(t, []machineCall{{op: "stop", user: "owner", id: "cicd-id"}}, f.calls)
}

// Unattended: only a start of the machine the run's activation targets.
func TestManageMachineUnattendedStartsOnlyTheActivationsMachine(t *testing.T) {
	f := &fakeMachines{activation: "cicd-id"}
	tool := newTestMachineTool(f)
	unattended := automationpolicy.Execution{Unattended: true, Approvals: map[string]string{"call": "anything"}}
	for _, input := range []string{
		`{"machine_id":"default-id","action":"start"}`,
		`{"machine_id":"cicd-id","action":"stop"}`,
		`{"machine_id":"cicd-id","action":"resize","size":"2xl"}`,
	} {
		call := ToolCall{ID: "call", Name: ToolManageMachine, Input: input}
		_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(unattended), call, true)
		require.Error(t, err, input)
		resp, err := tool.Run(machineToolCtx(unattended), call)
		require.NoError(t, err)
		require.True(t, resp.IsError, input)
	}
	require.Empty(t, f.calls)

	call := ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"cicd-id","action":"start"}`}
	_, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(unattended), call, true)
	require.NoError(t, err)
	require.False(t, ask)
	resp, err := tool.Run(machineToolCtx(unattended), call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []machineCall{{op: "start", user: "owner", id: "cicd-id", unattended: true}}, f.calls)

	f.activation = ""
	_, _, err = tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(unattended), call, true)
	require.Error(t, err, "a run no activation launched may start nothing")
}

func TestManageMachineRejectsMalformedCalls(t *testing.T) {
	tool := newTestMachineTool(&fakeMachines{})
	for _, input := range []string{
		`{"machine_id":"cicd-id","action":"resize"}`,
		`{"machine_id":"cicd-id","action":"resize","size":"huge"}`,
		`{"machine_id":"cicd-id","action":"start","size":"xl"}`,
		`{"machine_id":"","action":"start"}`,
		`{"machine_id":"cicd-id","action":"delete"}`,
	} {
		_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolManageMachine, Input: input}, false)
		require.Error(t, err, input)
	}
}

func TestMachineToolsRegistered(t *testing.T) {
	factory := NewToolsFactory(nil)
	for _, name := range []string{ToolListMachines, ToolManageMachine} {
		require.NotNil(t, factory.GetToolByName(name, nil))
		require.False(t, NeedsMachine(name), "acts through the control plane, not the run's machine")
	}
	require.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolManageMachine))
	require.NotEmpty(t, UnattendedWithholding(ToolManageMachine))
	require.True(t, (&Capabilities{Unattended: true, UnattendedOptIn: []string{ToolManageMachine}}).withheldUnattended(ToolManageMachine) == "",
		"a step that names manage_machine hands it to its unattended runs")
}

func TestListMachinesMarksTheRunsOwnMachine(t *testing.T) {
	own := "default-id"
	tool := NewListMachinesTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ActiveDaemonID: &own}}, &fakeMachines{})
	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolListMachines, Input: `{}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "cicd (id cicd-id), managed, suspended, xl")
	require.Contains(t, resp.Content, "default (id default-id), managed, ready, large — this run executes on it")
}
