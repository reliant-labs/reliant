package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The spawned agent has a distinct thread, but runs inline in the ROOT
// Temporal execution. Its reset baseline must be available through the query
// registered by DynamicWorkflow, not a query against a child execution.
func TestThreadResetQuery_RootReturnsInlineSpawnBaseline(t *testing.T) {
	t.Parallel()
	const chatID = "chat-root-reset-query"
	const toolCallID = "tc-reset-query-child"
	parent := "thread-" + chatID
	child := canChildThread(toolCallID)
	require.NotEqual(t, parent, child)

	var suite temporaltest.WorkflowTestSuite
	rootEnv := suite.NewTestWorkflowEnvironment()
	harness := newCANSpawnEnv(t, rootEnv)
	harness.scripts[parent] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall(toolCallID, "resolve child baseline")}},
	}
	// The child's preset deliberately differs from the root's inputs. This
	// catches returning the root baseline (or an empty map) for a child ID.
	rootEnv.OnActivity("LoadPresetParams", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"mode": "auto", "model": "child-preset-model"}, nil)
	input := spawnE2EWorkflowInput(chatID)
	input.Inputs = map[string]interface{}{"mode": "manual", "model": "root-model"}
	rootEnv.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, rootEnv.IsWorkflowCompleted())
	require.NoError(t, rootEnv.GetWorkflowError())
	require.Contains(t, harness.createdRows, child)
	require.Positive(t, harness.llmCalls[child], "the real inline child must have executed")

	// QueryWorkflow targets this environment's ROOT execution. No synthetic
	// query handler or manually populated ChildWorkflowTracker is involved.
	value, err := rootEnv.QueryWorkflow("get_thread_reset_inputs", child)
	require.NoError(t, err)
	var baseline map[string]interface{}
	require.NoError(t, value.Get(&baseline))
	// Spawn inherits the parent's mode, while its preset supplies the model.
	require.Equal(t, "manual", baseline["mode"])
	require.Equal(t, "child-preset-model", baseline["model"])

	_, err = rootEnv.QueryWorkflow("get_thread_reset_inputs", "unknown-thread")
	require.Error(t, err, "an unknown thread must not silently return the root inputs")
	rootEnv.AssertExpectations(t)
}
