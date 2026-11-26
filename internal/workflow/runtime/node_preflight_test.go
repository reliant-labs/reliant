// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
)

// preflightRecorder is what a node's preflights did: the machine each check
// ran on, and the attempt each ExecuteRunStep dispatch was.
type preflightRecorder struct {
	checked     []string
	runAttempts map[string][]int32
}

// setupPreflight registers the MachinePreflight stub (a machine fails when
// unhealthy says so) and records every run step's attempt.
func setupPreflight(env *testsuite.TestWorkflowEnvironment, unhealthy func(id string) bool) *preflightRecorder {
	rec := &preflightRecorder{runAttempts: map[string][]int32{}}
	env.RegisterActivityWithOptions(func(_ context.Context, in MachinePreflightInput) (MachinePreflightOutput, error) {
		id, _ := in.DaemonSelector["id"].(string)
		rec.checked = append(rec.checked, id)
		if unhealthy(id) {
			return MachinePreflightOutput{Failed: true, Reason: string(machinepreflight.ReasonTmpUnusable),
				Message: "/tmp has mode 0755, not 1777", DaemonID: id}, nil
		}
		return MachinePreflightOutput{DaemonID: id}, nil
	}, activity.RegisterOptions{Name: MachinePreflightActivityName})
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "ExecuteRunStep" {
			rec.runAttempts[info.ActivityType.Name] = append(rec.runAttempts[info.ActivityType.Name], info.Attempt)
		}
	})
	return rec
}

// placeExcluding places each ask on the first member of members its Exclude
// leaves, exhausted when it leaves none.
func placeExcluding(members ...string) placeFunc {
	return func(in machinepool.PlaceRunInput, _ int) (machinepool.PlaceRunOutput, error) {
		for _, m := range members {
			if !containsString(in.Exclude, m) {
				return machinepool.PlaceRunOutput{DaemonID: m, PoolName: in.Pool}, nil
			}
		}
		return machinepool.PlaceRunOutput{Exhausted: true, PoolName: in.Pool,
			Reason: fmt.Sprintf("every member of pool %s (%d) failed this node's preflight", in.Pool, len(members))}, nil
	}
}

const preflightSeqYAML = `
name: place-test
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: build
    type: run
    command: make
    preflight: %s
edges:
  - from: plan
    default: build
`

func preflightWorkflow(t *testing.T, preflight string, selectors map[string]*reliantv1.DaemonSelectorProto) *reliantv1.Workflow {
	t.Helper()
	return placementWorkflow(t, fmt.Sprintf(preflightSeqYAML, preflight), selectors)
}

// A node whose machine fails its preflight moves to another member of its
// pool and runs there — dispatched once, as its first attempt: the failed
// machine spent none of the node's attempts.
func TestNodePreflight_MovesOffAnUnhealthyMachine(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		selectors map[string]*reliantv1.DaemonSelectorProto
		wantAsks  []machinepool.PlaceRunInput
		wantPlan  string
	}{
		{
			name:      "the node's own pool placement moves",
			selectors: map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}},
			wantAsks: []machinepool.PlaceRunInput{
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Scope: "build", Pool: "builders"},
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Scope: "build", Pool: "builders", Exclude: []string{"m-a"}},
			},
		},
		{
			name:      "a node on the run's pool member gets a placement of its own; the run stays",
			selectors: map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}},
			wantAsks: []machinepool.PlaceRunInput{
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Pool: "builders"},
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Scope: "build", Pool: "builders", Exclude: []string{"m-a"}},
			},
			wantPlan: "m-a",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			rec := setupPlacementEnv(t, env, preflightWorkflow(t, "true", tt.selectors), placeExcluding("m-a", "m-b"))
			pf := setupPreflight(env, func(id string) bool { return id == "m-a" })

			env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			assert.Equal(t, tt.wantAsks, rec.asks)
			assert.Equal(t, []string{"m-a", "m-b"}, pf.checked)
			require.Len(t, rec.selectors["build"], 1, "the node's work was dispatched once")
			assert.Equal(t, &types.DaemonSelector{ID: "m-b"}, rec.selectors["build"][0])
			assert.Equal(t, []int32{1}, pf.runAttempts["ExecuteRunStep"], "on its first attempt")
			if tt.wantPlan != "" {
				assert.Equal(t, &types.DaemonSelector{ID: tt.wantPlan}, rec.selectors["plan"][0], "the run's other nodes stay")
			}
		})
	}
}

// A healthy machine is checked and kept: one check, no move.
func TestNodePreflight_HealthyMachineIsKept(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env, preflightWorkflow(t, "{tools: [task]}", map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}}),
		placeExcluding("m-a", "m-b"))
	var specs []machinepreflight.Spec
	env.RegisterActivityWithOptions(func(_ context.Context, in MachinePreflightInput) (MachinePreflightOutput, error) {
		specs = append(specs, in.Spec)
		return MachinePreflightOutput{}, nil
	}, activity.RegisterOptions{Name: MachinePreflightActivityName})

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	require.NoError(t, env.GetWorkflowError())
	require.Len(t, specs, 1)
	assert.Equal(t, []string{"git", "task"}, specs[0].Tools, "the node's tools, beside git")
	assert.Equal(t, machinepreflight.DefaultMinFreeBytes, specs[0].MinFreeBytes)
	assert.Len(t, rec.asks, 1)
	assert.Equal(t, &types.DaemonSelector{ID: "m-a"}, rec.selectors["build"][0])
}

// A node no machine passes fails, bounded, naming every machine's reason —
// and never dispatches its work.
func TestNodePreflight_NoHealthyMachineIsBounded(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		preflight string
		members   []string
		selectors map[string]*reliantv1.DaemonSelectorProto
		wantCheck []string
		wantErr   string
	}{
		{
			name: "max_machines (default 3) ends a large pool", preflight: "true",
			members:   []string{"m-a", "m-b", "m-c", "m-d", "m-e"},
			selectors: map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}},
			wantCheck: []string{"m-a", "m-b", "m-c"},
			wantErr:   "it tried 3 machines (preflight.max_machines)",
		},
		{
			name: "every member of a smaller pool", preflight: "{max_machines: 5}",
			members:   []string{"m-a", "m-b"},
			selectors: map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}},
			wantCheck: []string{"m-a", "m-b"},
			wantErr:   "every member of pool builders (2) failed this node's preflight",
		},
		{
			name: "a single-member pool fails fast", preflight: "true",
			members:   []string{"m-a"},
			selectors: map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}},
			wantCheck: []string{"m-a"},
			wantErr:   "every member of pool builders (1) failed",
		},
		{
			name: "a machine that is not a pool member fails fast", preflight: "true",
			selectors: map[string]*reliantv1.DaemonSelectorProto{"build": {Id: "m-pinned"}},
			wantCheck: []string{"m-pinned"},
			wantErr:   "the node is not on a machine pool, so no other machine can take it",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			rec := setupPlacementEnv(t, env, preflightWorkflow(t, tt.preflight, tt.selectors), placeExcluding(tt.members...))
			pf := setupPreflight(env, func(string) bool { return true })

			env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

			require.True(t, env.IsWorkflowCompleted())
			err := env.GetWorkflowError()
			require.Error(t, err)
			assert.Contains(t, err.Error(), NoHealthyMachinePrefix+" for node build")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "/tmp has mode 0755")
			assert.Equal(t, tt.wantCheck, pf.checked)
			assert.Empty(t, rec.selectors["build"], "the node's work never dispatched")
		})
	}
}

// A node that sets no preflight schedules no check and moves nowhere.
func TestNodePreflight_UnsetChangesNothing(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env, preflightWorkflow(t, "false", map[string]*reliantv1.DaemonSelectorProto{"build": {Pool: "builders"}}),
		placeExcluding("m-a", "m-b"))
	pf := setupPreflight(env, func(string) bool { return true })

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Empty(t, pf.checked)
	assert.Equal(t, []machinepool.PlaceRunInput{{ChatID: "chat-place", WorkflowID: testWorkflowID, Scope: "build", Pool: "builders"}}, rec.asks)
	assert.Equal(t, &types.DaemonSelector{ID: "m-a"}, rec.selectors["build"][0])
}

// A workflow node (an agent, a lowered review) checks the machine its body
// runs on, and its body moves with it.
func TestNodePreflight_WorkflowNodeBodyMoves(t *testing.T) {
	t.Parallel()
	const yaml = `
name: place-test
entry: [fix]
nodes:
  - id: fix
    type: workflow
    preflight: true
    inline:
      name: body
      entry: [work]
      nodes:
        - id: work
          type: run
          command: make
`
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env, placementWorkflow(t, yaml, map[string]*reliantv1.DaemonSelectorProto{"fix": {Pool: "builders"}}),
		placeExcluding("m-a", "m-b"))
	pf := setupPreflight(env, func(id string) bool { return id == "m-a" })

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"m-a", "m-b"}, pf.checked)
	require.Len(t, rec.selectors["work"], 1)
	assert.Equal(t, &types.DaemonSelector{ID: "m-b"}, rec.selectors["work"][0])
	assert.Equal(t, []int32{1}, pf.runAttempts["ExecuteRunStep"])
}

// Review B1 on #3790: the pool a failed preflight may move a node within is
// found even when the routing value carries no PlacedPool.
func TestPreflightPool(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		own, sel *DaemonSelectorValue
		want     string
	}{
		{"the node's own pool", &DaemonSelectorValue{Pool: "builders"}, &DaemonSelectorValue{ID: "m-a", Placed: true}, "builders"},
		{"a placed machine's pool", nil, &DaemonSelectorValue{ID: "m-a", Placed: true, PlacedPool: "builders"}, "builders"},
		{"an inherited pool selector not yet placed", nil, &DaemonSelectorValue{Pool: "builders"}, "builders"},
		{"a placed machine that lost its pool, with no run placement to read", nil, &DaemonSelectorValue{ID: "m-a", Placed: true}, ""},
		{"a fixed machine", nil, &DaemonSelectorValue{ID: "m-a"}, ""},
		{"no machine", nil, nil, ""},
	} {
		if got := preflightPool(nil, testWorkflowID, tt.own, tt.sel); got != tt.want {
			t.Errorf("%s: pool = %q, want %q", tt.name, got, tt.want)
		}
	}
}
