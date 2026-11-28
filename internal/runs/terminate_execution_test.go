// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// chainTemporal is Temporal as TerminateExecution must see it: the executions
// of one workflow id by run id, one of them current. Every call addressed to a
// run id acts on that execution alone and is refused once it has closed, as
// Temporal refuses a cancel or terminate of a closed run; one addressed to no
// run id acts on whichever is current, as Temporal's does.
type chainTemporal struct {
	current string
	status  map[string]enumspb.WorkflowExecutionStatus

	cancelled, terminated []string
	// closesOnCancel: the run acknowledges a cancel by closing.
	closesOnCancel bool
	// onCancel runs as a cancel arrives, before it is applied; afterCancel
	// once it has been.
	onCancel, afterCancel func()
}

func (c *chainTemporal) DescribeWorkflowExecution(_ context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	runID = c.resolve(runID)
	status, ok := c.status[runID]
	if !ok {
		return nil, errors.New("workflow not found")
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: runID}, Status: status,
	}}, nil
}

// resolve is the execution a call addressed to runID acts on.
func (c *chainTemporal) resolve(runID string) string {
	if runID == "" {
		return c.current
	}
	return runID
}

func (c *chainTemporal) CancelWorkflow(_ context.Context, _, runID string) error {
	c.cancelled = append(c.cancelled, runID)
	if c.onCancel != nil {
		c.onCancel()
	}
	runID = c.resolve(runID)
	if c.status[runID] != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return errors.New("workflow execution already completed")
	}
	if c.closesOnCancel {
		c.status[runID] = enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
	}
	if c.afterCancel != nil {
		c.afterCancel()
	}
	return nil
}

func (c *chainTemporal) QueryWorkflow(_ context.Context, _, runID, _ string, _ ...interface{}) (converter.EncodedValue, error) {
	runID = c.resolve(runID)
	if c.status[runID] != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return nil, errors.New("workflow execution already completed")
	}
	return stopValue{model.RunStopState{}}, nil
}

func (c *chainTemporal) TerminateWorkflow(_ context.Context, _, runID, _ string, _ ...interface{}) error {
	c.terminated = append(c.terminated, runID)
	runID = c.resolve(runID)
	if c.status[runID] != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return errors.New("workflow execution already completed")
	}
	c.status[runID] = enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED
	return nil
}

// The automation's run is "run-auto"; a person's later turn is "run-human".
func executionFixture(t *testing.T) (*fakeRepo, *chainTemporal, *Service) {
	t.Helper()
	repo, _, pause, _ := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	repo.pendingQuestions = []*db.Question{{ID: "q-1", ChatID: "chat-1", WorkflowID: "wf-1", ThreadID: "chat-1", StepID: "ask", Status: db.QuestionStatusPending}}
	temporal := &chainTemporal{current: "run-auto", status: map[string]enumspb.WorkflowExecutionStatus{"run-auto": enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}
	svc := New(Deps{Repo: repo, Chats: repo, Resolver: repo, Workflows: repo, Questions: repo, Temporal: temporal, Pause: pause, Waits: repo})
	clock := &fakeClock{t: clockEpoch}
	svc.cancelGrace, svc.cancelSettle, svc.cancelPoll = testGrace, testSettle, testPoll
	svc.now, svc.sleep = clock.now, clock.sleep
	return repo, temporal, svc
}

// startHumanTurn makes a person's turn the workflow id's current, running chain.
func (c *chainTemporal) startHumanTurn() {
	c.status["run-human"] = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	c.current = "run-human"
}

// The exact execution is cancelled, addressed by its run id, and the run's
// row and markers are settled as Terminate settles them.
func TestTerminateExecution_CancelsTheExactRun(t *testing.T) {
	repo, temporal, svc := executionFixture(t)
	temporal.closesOnCancel = true

	require.NoError(t, svc.TerminateExecution(context.Background(), "chat-1", "wf-1", "run-auto"))

	assert.Equal(t, []string{"run-auto"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated, "it acknowledged the cancel; nothing is terminated")
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints)
	assert.Equal(t, []string{"q-1"}, repo.resolvedQuestionIDs)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"], "the row its closed execution left live is settled cancelled")
	assert.Equal(t, []string{"chat-1|"}, repo.clearedUsageLimit)
}

// A run that is no longer the chat's current execution is refused outright:
// nothing is asked of Temporal and nothing is written.
func TestTerminateExecution_RefusesARunThatIsNoLongerCurrent(t *testing.T) {
	repo, temporal, svc := executionFixture(t)
	temporal.status["run-auto"] = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	temporal.startHumanTurn()

	err := svc.TerminateExecution(context.Background(), "chat-1", "wf-1", "run-auto")

	require.ErrorIs(t, err, ErrExecutionNotCurrent)
	assert.Empty(t, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
	assert.Equal(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, temporal.status["run-human"], "the person's run is untouched")
	assert.Empty(t, repo.deletedCheckpoints)
	assert.Empty(t, repo.resolvedQuestionIDs)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.casCalls)
}

// A different chat workflow is refused too: the caller decided on another.
func TestTerminateExecution_RefusesAnotherWorkflowID(t *testing.T) {
	_, temporal, svc := executionFixture(t)
	require.ErrorIs(t, svc.TerminateExecution(context.Background(), "chat-1", "wf-other", "run-auto"), ErrExecutionNotCurrent)
	assert.Empty(t, temporal.cancelled)
}

// The chain is replaced after the revalidation and before the cancel lands
// (the run completed and a person's turn started). The cancel and the
// terminate are addressed to the run id, so Temporal refuses both: the
// person's run keeps running, an error is returned, and nothing is written.
func TestTerminateExecution_ChainReplacedBeforeTheCancelLandsIsNeverStopped(t *testing.T) {
	repo, temporal, svc := executionFixture(t)
	temporal.onCancel = func() {
		temporal.status["run-auto"] = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
		temporal.startHumanTurn()
	}

	err := svc.TerminateExecution(context.Background(), "chat-1", "wf-1", "run-auto")

	require.Error(t, err)
	assert.Equal(t, []string{"run-auto"}, temporal.cancelled)
	assert.Equal(t, []string{"run-auto"}, temporal.terminated)
	assert.Equal(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, temporal.status["run-human"], "the person's run survives")
	assert.Empty(t, repo.deletedCheckpoints)
	assert.Empty(t, repo.resolvedQuestionIDs)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.casCalls)
	assert.Empty(t, repo.clearedUsageLimit)
}

// The cancelled run closes and a person's turn starts before the settle: the
// workflow's row and the chat's markers are that turn's now, so nothing of the
// cancel's is written over them.
func TestTerminateExecution_LeavesALaterChainsRowAlone(t *testing.T) {
	repo, temporal, svc := executionFixture(t)
	temporal.closesOnCancel = true
	temporal.afterCancel = temporal.startHumanTurn

	require.NoError(t, svc.TerminateExecution(context.Background(), "chat-1", "wf-1", "run-auto"))

	assert.Equal(t, []string{"run-auto"}, temporal.cancelled)
	assert.Empty(t, temporal.terminated)
	assert.Equal(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, temporal.status["run-human"])
	assert.Empty(t, repo.updatedStatuses, "the later turn's row is not settled as the cancelled run's")
	assert.Empty(t, repo.casCalls)
	assert.Zero(t, repo.cascadedReason)
	assert.Empty(t, repo.clearedUsageLimit)
}
