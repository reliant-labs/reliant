// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A definition whose load-time templates cannot resolve: `description` is
// resolved before the run starts (it is not a runtime-evaluated key) and
// reads a key `inputs.m` does not have.
const earlyFailureYAML = `
name: early-failure
description: "{{ inputs.m.missing.deeper }}"
inputs:
  m: {type: object, default: {}}
entry: [only]
nodes:
  - id: only
    type: compute
    outputs:
      n: "{{ 1 }}"
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: compute
      outputs:
        seen: "{{ failure.error }}"
`

// A run that fails resolving its templates, before any node starts, still
// runs its on_failure handler: that is the whole point of the handler, and the
// earliest failures are the ones nobody would otherwise be told about.
func TestOnFailure_RunsWhenTemplateResolutionFailsBeforeAnyNode(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	wf, err := wfyaml.ParseWorkflow([]byte(earlyFailureYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{YAML: []byte(earlyFailureYAML), WorkflowJSON: wfJSON, HasTemplates: true}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	for _, name := range []string{"WorkflowStatus", "Cleanup", "WorkflowError"} {
		env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true}, nil
		}, activity.RegisterOptions{Name: name})
	}

	in := resumeWorkflowInput("chat-early-failure", nil)
	in.WorkflowName = "early-failure"
	in.ExecContext.WorkflowName = "early-failure"
	env.ExecuteWorkflow(DynamicWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "the handler does not mask the failure")
	assert.Contains(t, env.GetWorkflowError().Error(), "failed to resolve workflow templates")

	val, err := env.QueryWorkflow(OnFailureQuery)
	require.NoError(t, err)
	var rec OnFailureRecord
	require.NoError(t, val.Get(&rec))
	assert.True(t, rec.Ran, "the on_failure handler runs for a run that failed before its first node")
	assert.Equal(t, FailureCodeNodeError, rec.ErrorCode)
	assert.Contains(t, rec.Error, "resolve workflow templates")
	assert.Empty(t, rec.NodeID, "the failure is outside any node")
}
