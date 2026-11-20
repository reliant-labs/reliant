// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TL-31: `workflow status` showed a failed call_llm as FAIL with no reason;
// only `workflow follow` had the error.
func TestStepFailures_KeepsTheNewestFailurePerNode(t *testing.T) {
	no, yes := false, true
	execs := []*reliantv1.StepExecution{ // newest first, as ListStepExecutions sends them
		{WorkflowId: "wf", StepId: "call_llm", NodePath: "review.agent_loop.call_llm", Success: &no, ErrorMessage: "provider returned 529: overloaded", CreatedAt: "t3"},
		{WorkflowId: "wf", StepId: "call_llm", NodePath: "review.agent_loop.call_llm", Success: &no, ErrorMessage: "an older failure", CreatedAt: "t2"},
		{WorkflowId: "wf", StepId: "call_llm", NodePath: "draft.agent_loop.call_llm", Success: &yes, CreatedAt: "t1"},
		{WorkflowId: "wf", StepId: "gate", NodePath: "gate", Success: &no, CreatedAt: "t0"}, // failed with no error recorded
	}
	got := stepFailures(execs)
	require.Len(t, got, 1)
	assert.Equal(t, "review.agent_loop.call_llm", got[0].NodePath)
	assert.Equal(t, "provider returned 529: overloaded", got[0].Error)

	var out bytes.Buffer
	printStatusReport(&out, statusReport{ExecutionID: "c", Status: "failed", Failures: got})
	assert.Contains(t, out.String(), "FAILED STEPS")
	assert.Contains(t, out.String(), "review.agent_loop.call_llm: provider returned 529: overloaded")
}

func TestHasFailedStep(t *testing.T) {
	assert.False(t, hasFailedStep([]statusStepGroup{{Steps: []statusStep{{Result: "ok"}}}}))
	assert.True(t, hasFailedStep([]statusStepGroup{{Steps: []statusStep{{Result: "ok", Failed: 2}}}}))
	assert.True(t, hasFailedStep([]statusStepGroup{{Steps: []statusStep{{Result: "FAIL"}}}}))
}

// TL-11: the first `workflow run --project-path` in a new folder was refused
// until the machine had synced the project's workflows.
func TestRetryUntilWorkflowsSync(t *testing.T) {
	prev := workflowSyncPoll
	workflowSyncPoll = time.Millisecond
	t.Cleanup(func() { workflowSyncPoll = prev })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	pending := errors.New("starting workflow: failed_precondition: " + workflowSyncPending + "; connect a machine")
	calls := 0
	got, err := retryUntilWorkflowsSync(cmd, time.Minute, func() (string, error) {
		calls++
		if calls < 3 {
			return "", pending
		}
		return "chat-1", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "chat-1", got)
	assert.Contains(t, stderr.String(), "Waiting up to 1m0s")

	// It gives up at the deadline with the last refusal, and never retries
	// another error.
	_, err = retryUntilWorkflowsSync(cmd, 0, func() (string, error) { return "", pending })
	assert.True(t, awaitingWorkflowSync(err))
	calls = 0
	_, err = retryUntilWorkflowsSync(cmd, time.Minute, func() (string, error) { calls++; return "", errors.New("permission denied") })
	assert.EqualError(t, err, "permission denied")
	assert.Equal(t, 1, calls)
}
