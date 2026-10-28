package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"
)

func cancelledQuestionWatcher(ctx workflow.Context) (bool, error) {
	tracker := &ChildWorkflowTracker{handoffRequested: true}
	record := &detachedSpawnRecord{}
	batch := &toolBatchPark{h: &toolBatchHandoff{tracker: tracker, rec: record}, nodeOutputs: map[string]interface{}{}}
	watchCtx, cancel := workflow.WithCancel(ctx)
	parked := awaitAskUserPark(watchCtx, "question-same-task", &askUserPark{batch: batch})
	// Answer selection cancels the watcher before its coroutine runs. Its
	// Await predicate is already true, which the SDK evaluates before Err().
	cancel()
	if err := workflow.Sleep(ctx, 1); err != nil {
		return false, err
	}
	return *parked || record.parked, nil
}

func TestAskUserCancelledWatcherCannotParkSettledBatch(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(cancelledQuestionWatcher)
	require.NoError(t, env.GetWorkflowError())
	var parked bool
	require.NoError(t, env.GetWorkflowResult(&parked))
	require.False(t, parked, "a cancelled watcher must not park an already answered batch even when handoff predicate is true")
}
