// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// Does a re-dispatched step re-use its assistant message id, or mint a new one?
//
// This decides whether an interrupted CallLLM can persist its partial the way
// execute_tools persists tool state. execute_tools survives cancellation by
// writing rows keyed on the TOOL CALL ID — an identity that comes from the
// LLM's output and is therefore stable across a re-dispatch — through a
// detached context (detachedForTerminalWrite, execute_tools.go). The write is
// an upsert, so a re-dispatch converges on one row.
//
// call_llm has no such key. Its row identity is the pre-allocated message id
// from preallocateAssistantMessageID (stream_finalized.go), and
// threads.SaveMessage does NOT key idempotency on it: checkExistingMessage
// looks up by (chat, workflow, ActivityID) only, and CreateMessage is a plain
// INSERT with no ON CONFLICT. So if the id is fresh per dispatch, a detached
// partial-persist writes a SECOND assistant message on every re-dispatch.
//
// The file header of stream_finalized.go says the SideEffect means "retries
// re-stream under the same id". That is true for a Temporal ACTIVITY RETRY
// (same command, replayed SideEffect). The open question — and the one that
// matters for interrupt — is a workflow-level RE-DISPATCH: StepExecutor.Start
// called again after a CanceledError, which issues a NEW SideEffect command.
func TestPreallocatedMessageID_FreshPerDispatch(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	// Mint an id twice in the same execution, exactly as two successive
	// StepExecutor.Start calls for the same logical step would.
	env.ExecuteWorkflow(func(ctx workflow.Context) ([]string, error) {
		first := preallocateAssistantMessageID(ctx, "chat-1", "thread-1")
		second := preallocateAssistantMessageID(ctx, "chat-1", "thread-1")
		return []string{first, second}, nil
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var ids []string
	require.NoError(t, env.GetWorkflowResult(&ids))
	require.Len(t, ids, 2)
	require.NotEmpty(t, ids[0], "new executions run at version 1 and get an id")

	// The load-bearing assertion. If these differ, the pre-allocated id cannot
	// fence a detached partial-persist in call_llm, and doing it anyway
	// duplicates the assistant message on every interrupt.
	require.NotEqual(t, ids[0], ids[1],
		"each dispatch mints its own id: SideEffect is positional in the command "+
			"stream, not memoized on (chat, thread)")
}
