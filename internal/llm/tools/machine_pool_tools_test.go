package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func pipelinePool() MachinePoolSummary {
	return MachinePoolSummary{ID: "pool-pipeline", Spec: MachinePoolSpec{Name: "pipeline", Selector: map[string]string{"pool": "pipeline"}, MaxRunsPerMember: 8, MemberIdleTimeout: "10m"},
		Members: []MachinePoolMember{{ID: "default-id", Name: "default", State: "connected"}}}
}

// label changes which pools a machine is in, so it runs only with an
// approval for exactly the resulting label set, and the card says which
// pools it joins and leaves.
func TestManageMachineLabelNeedsACardForTheExactResult(t *testing.T) {
	f := &fakeMachines{labels: map[string]map[string]string{"cicd-id": {"team": "infra", "old": "x"}}, pools: []MachinePoolSummary{pipelinePool()}}
	tool := newTestMachineTool(f)
	call := ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"cicd-id","action":"label","labels":{"pool":"pipeline"},"remove":["old"]}`}
	preview, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
	require.NoError(t, err)
	require.True(t, ask)
	require.Contains(t, preview.Title, "Set the labels of machine cicd (cicd-id) to: pool=pipeline, team=infra (now: old=x, team=infra)")
	require.Contains(t, preview.Title, "It joins machine pool(s): pipeline; it leaves: none")
	require.Equal(t, labelApprovalHash("cicd-id", map[string]string{"pool": "pipeline", "team": "infra"}), preview.Hash)

	// No approval, or one for another label set, runs nothing.
	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	other := labelApprovalHash("cicd-id", map[string]string{"pool": "gate", "team": "infra"})
	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": other}}), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Empty(t, f.calls)

	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}}), call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []machineCall{{op: "label pool=pipeline, team=infra", user: "owner", id: "cicd-id"}}, f.calls)

	// Applied: the same call is now a no-op, with no card.
	_, ask, err = tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), call, false)
	require.NoError(t, err)
	require.False(t, ask)
}

// Labels the registry would refuse are refused before a card is raised, and
// labelling is never unattended.
func TestManageMachineLabelRefusals(t *testing.T) {
	f := &fakeMachines{}
	tool := newTestMachineTool(f)
	for _, input := range []string{
		`{"machine_id":"cicd-id","action":"label"}`,
		`{"machine_id":"cicd-id","action":"label","labels":{"Pool":"x"}}`,
		`{"machine_id":"cicd-id","action":"label","labels":{"pool":"a"},"remove":["pool"]}`,
		`{"machine_id":"cicd-id","action":"label","labels":{"pool":"a"},"size":"xl"}`,
		`{"machine_id":"cicd-id","action":"start","labels":{"pool":"a"}}`,
		`{"machine_id":"someone-elses","action":"label","labels":{"pool":"a"}}`,
	} {
		_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolManageMachine, Input: input}, false)
		require.Error(t, err, input)
	}
	unattended := automationpolicy.Execution{Unattended: true, Approvals: map[string]string{"call": "anything"}}
	call := ToolCall{ID: "call", Name: ToolManageMachine, Input: `{"machine_id":"cicd-id","action":"label","labels":{"pool":"pipeline"}}`}
	_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(unattended), call, true)
	require.ErrorContains(t, err, "unattended runs cannot label machines")
	resp, err := tool.Run(machineToolCtx(unattended), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Empty(t, f.calls)
}

func newTestPoolTool(f *fakeMachines) Tool {
	own := "default-id"
	return NewManageMachinePoolTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ActiveDaemonID: &own}}, f)
}

// put creates or changes a pool: the card shows the full resulting
// definition and its members, and the approval binds to exactly that
// definition and to the pool as it was when the card was raised.
func TestManageMachinePoolPutNeedsACardForTheExactDefinition(t *testing.T) {
	f := &fakeMachines{labels: map[string]map[string]string{"cicd-id": {"pool": "gate"}}, pools: []MachinePoolSummary{pipelinePool()}}
	tool := newTestPoolTool(f)

	create := ToolCall{ID: "call", Name: ToolManageMachinePool, Input: `{"action":"put","name":"gate","max_runs_per_member":1}`}
	preview, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), create, false)
	require.NoError(t, err)
	require.True(t, ask)
	require.Contains(t, preview.Title, "Create machine pool gate: selector pool=gate; max_runs_per_member 1")
	require.Contains(t, preview.Title, "member_idle_timeout 10m")
	require.Contains(t, preview.Title, "Members: cicd (cicd-id)")

	change := ToolCall{ID: "call", Name: ToolManageMachinePool, Input: `{"action":"put","name":"pipeline","max_runs_per_member":24}`}
	card, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), change, false)
	require.NoError(t, err)
	require.True(t, ask)
	require.Contains(t, card.Title, "Change machine pool pipeline (id pool-pipeline): max_runs_per_member 8 -> 24.")
	require.Contains(t, card.Title, "selector pool=pipeline", "an omitted field keeps its current value")
	require.NotEqual(t, preview.Hash, card.Hash)

	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}}), change)
	require.NoError(t, err)
	require.True(t, resp.IsError, "another call's approval does not carry over")
	// The pool changed after the card: its approval no longer applies.
	moved := pipelinePool()
	moved.Spec.MemberIdleTimeout = "20m"
	f.pools = []MachinePoolSummary{moved}
	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": card.Hash}}), change)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Empty(t, f.calls)

	f.pools = []MachinePoolSummary{pipelinePool()}
	resp, err = tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": card.Hash}}), change)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, f.calls, 1)
	require.Contains(t, f.calls[0].op, "put pipeline: selector pool=pipeline; max_runs_per_member 24")

	// A put that changes nothing needs no card.
	_, ask, err = tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}),
		ToolCall{ID: "call", Name: ToolManageMachinePool, Input: `{"action":"put","name":"pipeline","max_runs_per_member":8}`}, false)
	require.NoError(t, err)
	require.False(t, ask)
}

func TestManageMachinePoolDeleteAndRefusals(t *testing.T) {
	f := &fakeMachines{pools: []MachinePoolSummary{pipelinePool()}}
	tool := newTestPoolTool(f)
	del := ToolCall{ID: "call", Name: ToolManageMachinePool, Input: `{"action":"delete","name":"pipeline"}`}
	card, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), del, false)
	require.NoError(t, err)
	require.True(t, ask)
	require.Contains(t, card.Title, "Delete machine pool pipeline (id pool-pipeline)?")
	resp, err := tool.Run(machineToolCtx(automationpolicy.Execution{Approvals: map[string]string{"call": card.Hash}}), del)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []machineCall{{op: "delete pipeline", user: "owner"}}, f.calls)

	for _, input := range []string{
		`{"action":"delete","name":"nope"}`,
		`{"action":"delete","name":"pipeline","max_runs_per_member":1}`,
		`{"action":"put","name":"Bad Name"}`,
		`{"action":"put","name":"p","selector":{"Pool":"x"}}`,
		`{"action":"put","name":"p","member_idle_timeout":"1s"}`,
		`{"action":"put","name":"p","min_members":3,"max_members":1}`,
		`{"action":"put","name":"p","member_size":"huge"}`,
		`{"action":"rename","name":"p"}`,
		`{"action":"put","name":"p","unknown":1}`,
	} {
		_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolManageMachinePool, Input: input}, false)
		require.Error(t, err, input)
	}

	unattended := automationpolicy.Execution{Unattended: true, Approvals: map[string]string{"call": card.Hash}}
	_, _, err = tool.(AutomationApprovalTool).PrepareAutomation(machineToolCtx(unattended), del, true)
	require.ErrorContains(t, err, "unattended runs cannot define or delete machine pools")
	f.calls = nil
	resp, err = tool.Run(machineToolCtx(unattended), del)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Empty(t, f.calls)
}

func TestMachinePoolToolRegistered(t *testing.T) {
	factory := NewToolsFactory(nil)
	require.NotNil(t, factory.GetToolByName(ToolManageMachinePool, nil))
	require.False(t, NeedsMachine(ToolManageMachinePool))
	require.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolManageMachinePool))
	require.NotEmpty(t, UnattendedWithholding(ToolManageMachinePool))
}

// list_machines shows each machine's labels and pools, and every pool's
// definition and members.
func TestListMachinesShowsLabelsAndPools(t *testing.T) {
	own := "default-id"
	f := &fakeMachines{labels: map[string]map[string]string{"default-id": {"pool": "pipeline"}}, pools: []MachinePoolSummary{pipelinePool()}}
	resp, err := NewListMachinesTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ActiveDaemonID: &own}}, f).
		Run(machineToolCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolListMachines, Input: `{}`})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "labels: pool=pipeline; pools: none")
	require.Contains(t, resp.Content, "- pipeline (id pool-pipeline): selector pool=pipeline; max_runs_per_member 8")
	require.Contains(t, resp.Content, "members: default (id default-id, connected, 0 live run(s))")
}
