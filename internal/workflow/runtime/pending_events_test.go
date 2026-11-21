// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A node skipped by its condition routes its successors at once, while a
// parallel branch's long step still runs. Before routePendingFirst, the skip
// was routed only once some running step completed: the PR pipeline's
// heartbeat (a step that runs beside the whole attempt) held every pr-fix run
// at its first skipped node (2026-11-20).
const skipBesideLongBranchYAML = `
name: resume-test
entry: [start]
nodes:
  - id: start
    type: call_llm
  - id: beat
    type: call_llm
  - id: work
    type: call_llm
  - id: maybe
    type: call_llm
    condition: "false"
  - id: after
    type: call_llm
edges:
  - from: start
    default: [work, beat]
  - from: work
    default: maybe
  - from: maybe
    default: after
`

func TestDynamicWorkflow_SkippedNodeRoutesWithoutWaitingForAParallelBranch(t *testing.T) {
	t.Parallel()
	wf, err := wfyaml.ParseWorkflow([]byte(skipBesideLongBranchYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	for _, name := range []string{"WorkflowStatus", "Cleanup"} {
		env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		}, activity.RegisterOptions{Name: name})
	}
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})

	// beat stands for the heartbeat: it returns when `after` has run, or
	// after a bound. Which came first is the verdict.
	var mu sync.Mutex
	var order []string
	afterRan := make(chan struct{})
	env.RegisterActivityWithOptions(func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		id := input.Node.GetId()
		switch id {
		case "beat":
			select {
			case <-afterRan:
			case <-time.After(3 * time.Second):
			}
			id = "beat-done"
		case "after":
			defer close(afterRan)
		}
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
		return map[string]interface{}{"response_text": "ok"}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-skip-beside-branch", nil))
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	idx := func(id string) int {
		for i, o := range order {
			if o == id {
				return i
			}
		}
		t.Fatalf("%s never ran: %v", id, order)
		return -1
	}
	require.Less(t, idx("after"), idx("beat-done"),
		"the skipped node's successor waited for the parallel branch to finish: %v", order)
}
