package runtime

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestActualDynamicPrivateContinuation exercises the real engine and its actual
// newContinueAsNewError path. Pressure is a package-private existing test seam,
// not workflow inputs, YAML or an exported production override.
func TestActualDynamicPrivateContinuation(t *testing.T) {
	if testing.Short() {
		t.Skip("boots pinned Temporal private continuation proof; run without -short")
	}
	path := os.Getenv("TEMPORAL_CLI_PATH")
	if path == "" {
		t.Fatal("set verified TEMPORAL_CLI_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{ExistingPath: path, LogLevel: "never", ClientOptions: &client.Options{Namespace: "default"}})
	require.NoError(t, err)
	defer srv.Stop()
	queue := "private-can-" + uuid.NewString()
	root := "private-can-" + uuid.NewString()
	chat := "chat-" + uuid.NewString()
	var calls atomic.Int32
	var hooks atomic.Int32
	var pressure atomic.Bool
	forcePressure(t, chat, func() historyPressure {
		if pressure.Load() {
			return historyPressureSoft
		}
		return historyPressureNone
	})
	// Use existing real agent-loop graph. One initial model-boundary response
	// raises pressure; next run returns done and executes the private hook once.
	wf, err := wfyaml.ParseWorkflow([]byte(spawnBackgroundE2EYAML))
	require.NoError(t, err)
	graph, err := protojson.Marshal(wf)
	require.NoError(t, err)
	private := NewDynamicWorkflowWithHooks(WorkflowHooks{AfterGraph: func(ctx workflow.Context, done GraphCompletion) error { hooks.Add(1); return nil }})
	w := worker.New(srv.Client(), queue, worker.Options{})
	w.RegisterWorkflowWithOptions(private, workflow.RegisterOptions{Name: "PrivateRegisteredReview"})
	var loads atomic.Int32
	var initialRun string
	var loadMu sync.Mutex
	w.RegisterActivityWithOptions(func(ctx context.Context, _ map[string]string) (LoadedWorkflow, error) {
		loads.Add(1)
		loadMu.Lock()
		run := activity.GetInfo(ctx).WorkflowExecution.RunID
		if initialRun == "" {
			initialRun = run
		} else if run != initialRun {
			pressure.Store(false)
		}
		loadMu.Unlock()
		return LoadedWorkflow{WorkflowJSON: graph}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "Cleanup", "SaveMessage", "EmitThreadEvent", "WorkflowError"} {
		w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		}, activity.RegisterOptions{Name: name})
	}
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"valid": true}, nil
	}, activity.RegisterOptions{Name: "ValidateThreadOwnership"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) { return map[string]any{}, nil }, activity.RegisterOptions{Name: "LoadPresetParams"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"count": 0, "has_messages": false}, nil
	}, activity.RegisterOptions{Name: "DrainAgentMessages"})
	w.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]any, error) {
		if calls.Add(1) == 1 {
			pressure.Store(true)
			return map[string]any{"response_text": "working", "tool_calls": []any{map[string]any{"id": "noop", "name": "view", "input": "{}"}}}, nil
		}
		pressure.Store(false)
		return map[string]any{"response_text": "done", "tool_calls": nil}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	w.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]any, error) {
		return map[string]any{"tool_results": []any{}}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	require.NoError(t, w.Start())
	defer w.Stop()
	execution, err := srv.Client().ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: queue}, "PrivateRegisteredReview", spawnE2EWorkflowInput(chat))
	require.NoError(t, err)
	initialExecutionRun := execution.GetRunID()
	var result WorkflowResult
	err = execution.Get(ctx, &result)
	require.NoError(t, err)
	require.Equal(t, int32(1), hooks.Load())
	proof, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: srv.Client().WorkflowService(), Namespace: "default"}, runcontinuity.Execution{WorkflowID: root, RunID: initialExecutionRun})
	require.NoError(t, err)
	require.NotEmpty(t, proof.Edges)
}

func TestContinueAsNewMissingRegisteredTypeFailsClosed(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		workflow.GetInfo(ctx).WorkflowType.Name = ""
		return newContinueAsNewError(ctx, WorkflowInput{}, "loop", 1, nil, false)
	})
	require.ErrorContains(t, env.GetWorkflowError(), "missing registered workflow type")
	var can *workflow.ContinueAsNewError
	require.False(t, errors.As(env.GetWorkflowError(), &can))
}

func TestContinueAsNewRegisteredTypeVersionGate(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			fn := func(ctx workflow.Context) error {
				return newContinueAsNewError(ctx, WorkflowInput{}, "loop", 1, nil, false)
			}
			env.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "PrivateRegisteredReview"})
			if legacy {
				env.OnGetVersion("continue-as-new-preserve-registered-type", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
			}
			env.ExecuteWorkflow("PrivateRegisteredReview")
			var can *workflow.ContinueAsNewError
			require.True(t, errors.As(env.GetWorkflowError(), &can))
			expected := "PrivateRegisteredReview"
			if legacy {
				expected = "DynamicWorkflow"
			}
			require.Equal(t, expected, can.WorkflowType.Name)
		})
	}
}
