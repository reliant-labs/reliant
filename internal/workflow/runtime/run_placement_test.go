// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

// placementRecorder is what a placement run did: every PlaceRunOnMachinePool
// ask, the selector each call_llm and run step was dispatched with, and the
// error cards the run posted.
type placementRecorder struct {
	asks      []machinepool.PlaceRunInput
	selectors map[string][]*types.DaemonSelector
	errors    []map[string]interface{}
}

// testWorkflowID is the Temporal test environment's workflow id, which the
// run takes over the input's.
const testWorkflowID = "default-test-workflow-id"

// placeFunc answers one PlaceRunOnMachinePool ask; n is the ask's 0-based
// index in the run.
type placeFunc func(in machinepool.PlaceRunInput, n int) (machinepool.PlaceRunOutput, error)

// placeOn places every ask on the member its pool (or "labels") maps to.
func placeOn(members map[string]string) placeFunc {
	return func(in machinepool.PlaceRunInput, _ int) (machinepool.PlaceRunOutput, error) {
		key := in.Pool
		if key == "" {
			key = "labels"
		}
		return machinepool.PlaceRunOutput{DaemonID: members[key], PoolName: in.Pool}, nil
	}
}

func setupPlacementEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, wf *reliantv1.Workflow, place placeFunc) *placementRecorder {
	t.Helper()
	rec := &placementRecorder{selectors: map[string][]*types.DaemonSelector{}}
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	for _, name := range []string{"WorkflowStatus", "Cleanup"} {
		env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		}, activity.RegisterOptions{Name: name})
	}
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		rec.errors = append(rec.errors, in)
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "WorkflowError"})
	// Stands in for handlers.FailStepActivity, which fails non-retryably
	// with the step's error.
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		msg, _ := in["error"].(string)
		return nil, temporal.NewNonRetryableApplicationError(msg, "", nil)
	}, activity.RegisterOptions{Name: "FailStep"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		rec.selectors[in.Node.GetId()] = append(rec.selectors[in.Node.GetId()], in.Runtime.DaemonSelector)
		return map[string]interface{}{"response_text": "ok"}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		var sel *types.DaemonSelector
		if raw, ok := in["daemon_selector"]; ok {
			b, err := json.Marshal(raw)
			if err != nil {
				return nil, err
			}
			sel = &types.DaemonSelector{}
			if err := json.Unmarshal(b, sel); err != nil {
				return nil, err
			}
		}
		id, _ := in["step_id"].(string)
		rec.selectors[id] = append(rec.selectors[id], sel)
		return map[string]interface{}{"exit_code": 0, "stdout": "ok"}, nil
	}, activity.RegisterOptions{Name: "ExecuteRunStep"})
	env.RegisterActivityWithOptions(func(_ context.Context, in machinepool.PlaceRunInput) (machinepool.PlaceRunOutput, error) {
		n := len(rec.asks)
		rec.asks = append(rec.asks, in)
		return place(in, n)
	}, activity.RegisterOptions{Name: machinepool.PlaceRunActivityName})
	return rec
}

func placementInput(execCtx *ExecutionContext) WorkflowInput {
	if execCtx == nil {
		execCtx = &ExecutionContext{}
	}
	execCtx.ChatID = "chat-place"
	execCtx.Thread = "thread-place"
	execCtx.WorkflowName = "place-test"
	return WorkflowInput{
		ChatID:       "chat-place",
		WorkflowName: "place-test",
		Inputs:       map[string]interface{}{},
		ExecContext:  execCtx,
	}
}

// placementWorkflow parses yamlStr and sets the given selectors on the
// workflow ("" key) and on nodes (by id, loop bodies included), so the test
// does not depend on how YAML spells a pool.
func placementWorkflow(t *testing.T, yamlStr string, selectors map[string]*reliantv1.DaemonSelectorProto) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
	require.NoError(t, err)
	literal := func(ds *reliantv1.DaemonSelectorProto) *reliantv1.CelDaemonSelector {
		return &reliantv1.CelDaemonSelector{Value: &reliantv1.CelDaemonSelector_Literal{Literal: ds}}
	}
	if ds, ok := selectors[""]; ok {
		wf.Daemon = literal(ds)
	}
	var walk func(nodes []*reliantv1.Node)
	walk = func(nodes []*reliantv1.Node) {
		for _, n := range nodes {
			if ds, ok := selectors[n.GetId()]; ok {
				n.Daemon = literal(ds)
			}
			walk(n.GetLoop().GetInline().GetNodes())
		}
	}
	walk(wf.GetNodes())
	return wf
}

const placementSeqYAML = `
name: place-test
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: work
    type: call_llm
  - id: build
    type: run
    command: make
edges:
  - from: plan
    default: work
  - from: work
    default: build
`

func TestDynamicWorkflow_PlacesDaemonSelectors(t *testing.T) {
	t.Parallel()
	pin := func(id string) *types.DaemonSelector { return &types.DaemonSelector{ID: id} }
	tests := []struct {
		name      string
		selectors map[string]*reliantv1.DaemonSelectorProto
		wantAsks  []machinepool.PlaceRunInput
		want      map[string]*types.DaemonSelector
	}{
		{
			name:      "workflow-level pool places once and every step routes to the member",
			selectors: map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}},
			wantAsks:  []machinepool.PlaceRunInput{{ChatID: "chat-place", WorkflowID: testWorkflowID, Pool: "builders"}},
			want:      map[string]*types.DaemonSelector{"plan": pin("m-builders"), "work": pin("m-builders"), "build": pin("m-builders")},
		},
		{
			name:      "workflow-level labels-only selector places on the labels",
			selectors: map[string]*reliantv1.DaemonSelectorProto{"": {Labels: map[string]string{"gpu": "a100"}}},
			wantAsks:  []machinepool.PlaceRunInput{{ChatID: "chat-place", WorkflowID: testWorkflowID, Labels: map[string]string{"gpu": "a100"}}},
			want:      map[string]*types.DaemonSelector{"plan": pin("m-labels"), "work": pin("m-labels"), "build": pin("m-labels")},
		},
		{
			name: "a node-level pool overrides the workflow's for that node only",
			selectors: map[string]*reliantv1.DaemonSelectorProto{
				"":     {Pool: "builders"},
				"work": {Pool: "gpus"},
			},
			wantAsks: []machinepool.PlaceRunInput{
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Pool: "builders"},
				{ChatID: "chat-place", WorkflowID: testWorkflowID, Scope: "work", Pool: "gpus"},
			},
			want: map[string]*types.DaemonSelector{"plan": pin("m-builders"), "work": pin("m-gpus"), "build": pin("m-builders")},
		},
		{
			name: "name, id and type pins (labels beside them included) are unchanged and never placed",
			selectors: map[string]*reliantv1.DaemonSelectorProto{
				"":      {Name: "ci-box"},
				"work":  {Id: "d-123"},
				"build": {Type: "cloud", Labels: map[string]string{"os": "linux"}},
			},
			want: map[string]*types.DaemonSelector{
				"plan":  {Name: "ci-box"},
				"work":  {ID: "d-123"},
				"build": {Type: "cloud", Labels: map[string]string{"os": "linux"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			rec := setupPlacementEnv(t, env, placementWorkflow(t, placementSeqYAML, tt.selectors),
				placeOn(map[string]string{"builders": "m-builders", "gpus": "m-gpus", "labels": "m-labels"}))

			env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			assert.Equal(t, tt.wantAsks, rec.asks)
			for node, want := range tt.want {
				require.Len(t, rec.selectors[node], 1, "node %s ran once", node)
				assert.Equal(t, want, rec.selectors[node][0], "node %s", node)
			}
		})
	}
}

// A pool with no free member answers Waiting; the run sleeps and asks again
// until one takes it, then runs on it.
func TestDynamicWorkflow_PlacementWaitsForAMember(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	start := env.Now()
	rec := setupPlacementEnv(t, env,
		placementWorkflow(t, placementSeqYAML, map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}}),
		func(in machinepool.PlaceRunInput, n int) (machinepool.PlaceRunOutput, error) {
			if n < 2 {
				return machinepool.PlaceRunOutput{Waiting: true, Reason: "waiting: every member is busy"}, nil
			}
			return machinepool.PlaceRunOutput{DaemonID: "m1"}, nil
		})

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Len(t, rec.asks, 3)
	assert.GreaterOrEqual(t, env.Now().Sub(start), placeRunWaitFirst*3, "it waited 15s, then 30s, between asks")
	assert.Equal(t, &types.DaemonSelector{ID: "m1"}, rec.selectors["plan"][0])
}

// A run whose pool never frees a member gives up after the cap with the
// pool's last reason, rather than holding a workflow forever.
func TestDynamicWorkflow_PlacementGivesUpAfterTheCap(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	start := env.Now()
	rec := setupPlacementEnv(t, env,
		placementWorkflow(t, placementSeqYAML, map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}}),
		func(machinepool.PlaceRunInput, int) (machinepool.PlaceRunOutput, error) {
			return machinepool.PlaceRunOutput{Waiting: true, Reason: "waiting: every member is busy"}, nil
		})

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	err := env.GetWorkflowError()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "every member is busy")
	assert.GreaterOrEqual(t, env.Now().Sub(start), placeRunWaitCap)
	assert.Less(t, len(rec.asks), 400, "a day of waiting stays a bounded number of asks (got %d)", len(rec.asks))
	assert.Empty(t, rec.selectors, "no step ran")
}

// An unknown pool fails the run at once (the error type is non-retryable),
// with the activity's message on the run's error card.
func TestDynamicWorkflow_PlacementUnknownPoolFailsTheRun(t *testing.T) {
	t.Parallel()
	const msg = `no machine pool named "nope"`
	for _, tt := range []struct {
		name      string
		selectors map[string]*reliantv1.DaemonSelectorProto
	}{
		{"workflow level", map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "nope"}}},
		{"node level", map[string]*reliantv1.DaemonSelectorProto{"work": {Pool: "nope"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			rec := setupPlacementEnv(t, env, placementWorkflow(t, placementSeqYAML, tt.selectors),
				func(machinepool.PlaceRunInput, int) (machinepool.PlaceRunOutput, error) {
					return machinepool.PlaceRunOutput{}, temporal.NewNonRetryableApplicationError(msg, machinepool.PoolNotFoundErrorType, nil)
				})

			env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

			err := env.GetWorkflowError()
			require.Error(t, err)
			assert.Contains(t, err.Error(), msg)
			assert.Len(t, rec.asks, 1, "a missing pool is not retried")
			assert.Empty(t, rec.selectors["work"], "the placed step never dispatched")
			if tt.name == "workflow level" {
				require.NotEmpty(t, rec.errors)
				assert.Equal(t, "daemon_resolution_error", rec.errors[0]["error_type"])
				assert.Equal(t, msg, rec.errors[0]["error_message"])
			}
		})
	}
}

// A continue-as-new successor carries its predecessor's placement in the
// execution context, routes to the same machine and never asks again.
func TestDynamicWorkflow_PlacementCrossesContinueAsNew(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env,
		placementWorkflow(t, placementSeqYAML, map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}}),
		placeOn(map[string]string{"builders": "m-other"}))

	// What the predecessor handed over: its context, through JSON as
	// NewContinueAsNewError serializes it.
	raw, err := json.Marshal(&ExecutionContext{WorkflowPlacement: &RunPlacement{DaemonID: "m-placed"}})
	require.NoError(t, err)
	var carried ExecutionContext
	require.NoError(t, json.Unmarshal(raw, &carried))

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(&carried))

	require.NoError(t, env.GetWorkflowError())
	assert.Empty(t, rec.asks, "the successor does not place again")
	assert.Equal(t, &types.DaemonSelector{ID: "m-placed"}, rec.selectors["plan"][0])
	assert.Equal(t, &types.DaemonSelector{ID: "m-placed"}, rec.selectors["build"][0])
}

// A node inside a loop is placed once, at its path without the iteration;
// every iteration runs on that member.
func TestDynamicWorkflow_LoopNodePlacedOnce(t *testing.T) {
	t.Parallel()
	const loopYAML = `
name: place-test
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 3
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env,
		placementWorkflow(t, loopYAML, map[string]*reliantv1.DaemonSelectorProto{"work": {Pool: "gpus"}}),
		placeOn(map[string]string{"gpus": "m-gpus"}))

	env.ExecuteWorkflow(DynamicWorkflow, placementInput(nil))

	require.NoError(t, env.GetWorkflowError())
	require.Len(t, rec.asks, 1, "placed once for every iteration")
	assert.Equal(t, "agent_loop.work", rec.asks[0].Scope)
	require.Len(t, rec.selectors["work"], 3)
	for i, sel := range rec.selectors["work"] {
		assert.Equal(t, &types.DaemonSelector{ID: "m-gpus"}, sel, "iteration %d", i)
	}
}

// A placement that pins nothing (a scenario's stub) leaves the selector
// unpinned: the workflow's falls back to the chat's pin, a node's to none.
func TestDynamicWorkflow_EmptyPlacementLeavesTheRunUnpinned(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	rec := setupPlacementEnv(t, env,
		placementWorkflow(t, placementSeqYAML, map[string]*reliantv1.DaemonSelectorProto{"": {Pool: "builders"}, "work": {Pool: "gpus"}}),
		func(machinepool.PlaceRunInput, int) (machinepool.PlaceRunOutput, error) {
			return machinepool.PlaceRunOutput{}, nil
		})
	in := placementInput(nil)
	in.Inputs["session_daemon_id"] = "chat-pin"

	env.ExecuteWorkflow(DynamicWorkflow, in)

	require.NoError(t, env.GetWorkflowError())
	assert.Len(t, rec.asks, 2)
	assert.Equal(t, &types.DaemonSelector{ID: "chat-pin", Session: true}, rec.selectors["plan"][0])
	assert.Nil(t, rec.selectors["work"][0])
}

func TestDaemonSelectorNeedsPlacement(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"gpu": "a100"}
	for _, tt := range []struct {
		ds   *DaemonSelectorValue
		want bool
	}{
		{nil, false},
		{&DaemonSelectorValue{}, false},
		{&DaemonSelectorValue{Pool: "builders"}, true},
		{&DaemonSelectorValue{Labels: labels}, true},
		{&DaemonSelectorValue{Labels: labels, Name: "box"}, false},
		{&DaemonSelectorValue{Labels: labels, ID: "d1"}, false},
		{&DaemonSelectorValue{Labels: labels, Type: "cloud"}, false},
		{&DaemonSelectorValue{ID: "d1", Session: true}, false},
	} {
		assert.Equal(t, tt.want, tt.ds.NeedsPlacement(), "%+v", tt.ds)
	}
}

func TestDaemonSelectorCarriesPool(t *testing.T) {
	t.Parallel()
	assert.Equal(t, &DaemonSelectorValue{Pool: "builders"},
		ResolveDaemonSelectorProto(&reliantv1.DaemonSelectorProto{Pool: "builders"}))
	ds, err := daemonSelectorFromCELResult(map[string]interface{}{"pool": "builders"})
	require.NoError(t, err)
	assert.Equal(t, &DaemonSelectorValue{Pool: "builders"}, ds)
}

// The placement's ladder is reported as the infrastructure one.
func TestPlaceRunIsAnInfrastructureActivity(t *testing.T) {
	t.Parallel()
	info := activity.Info{ActivityType: activity.Type{Name: machinepool.PlaceRunActivityName}, StartToCloseTimeout: placeRunStartToClose}
	assert.Equal(t, infrastructureActivityMaxAttempts, resolveMaxAttempts(info))
}
