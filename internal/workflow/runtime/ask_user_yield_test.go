// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// A thread blocked in ask_user yields to new input for it: a message (its
// doorbell) or an interrupt ends the wait, resolves the question unanswered,
// and tells the model why. Prod, chat adcee226 (2026-10-21): a spawn's
// question sat unanswered while the user's "continue" to it sat in its
// mailbox and seven "send now" interrupts did nothing.

const askUserYieldThread = "thread-ask-user-yield"

type askUserYieldRun struct {
	env        *testsuite.TestWorkflowEnvironment
	questionID string
	scheduled  []string
	resolved   []map[string]interface{}
	// resolveReply is what the fake QuestionResolve answers.
	resolveReply map[string]interface{}
}

func newAskUserYieldRun(t *testing.T) *askUserYieldRun {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	r := &askUserYieldRun{env: suite.NewTestWorkflowEnvironment(), resolveReply: map[string]interface{}{"success": true}}
	workflowBytes := askUserToolWorkflowBytes(t)

	r.env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		r.scheduled = append(r.scheduled, info.ActivityType.Name)
	})
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (map[string]interface{}, error) {
			var payload map[string]interface{}
			require.NoError(t, json.Unmarshal(input, &payload))
			assert.Equal(t, askUserYieldThread, payload["thread_id"])
			r.questionID = "question-yield"
			return map[string]interface{}{"question_id": r.questionID, "already_resolved": false}, nil
		},
		activity.RegisterOptions{Name: "QuestionCreate"},
	)
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			r.resolved = append(r.resolved, input)
			return r.resolveReply, nil
		},
		activity.RegisterOptions{Name: "QuestionResolve"},
	)
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)
	r.env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	return r
}

// at runs fn once the run is parked on its question.
func (r *askUserYieldRun) at(t *testing.T, after time.Duration, fn func()) {
	r.env.RegisterDelayedCallback(func() {
		require.NotEmpty(t, r.questionID, "the run must be parked on its question by now")
		fn()
	}, after)
}

func (r *askUserYieldRun) wake(thread string) {
	r.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonMailbox, FromUser: true})
}

func (r *askUserYieldRun) interrupt(thread string) {
	r.env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: thread})
}

func (r *askUserYieldRun) answer() {
	r.env.SignalWorkflow("signal.question."+r.questionID, map[string]interface{}{
		"action":        "reply",
		"response_data": `{"answers":[{"question":"What visual style?","selected":["Clinical Minimal"]}]}`,
	})
}

// run executes the chat's workflow and returns the ask_user tool result's
// content.
func (r *askUserYieldRun) run(t *testing.T) string {
	t.Helper()
	r.executeDynamic()
	require.True(t, r.env.IsWorkflowCompleted(), "the ask_user wait must end")
	require.NoError(t, r.env.GetWorkflowError())
	var result WorkflowResult
	require.NoError(t, r.env.GetWorkflowResult(&result))
	content, ok := result.Outputs["content"].(string)
	require.True(t, ok, "ask_user must return a tool result, got %#v", result.Outputs["content"])
	return content
}

func (r *askUserYieldRun) executeDynamic() {
	r.env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-ask-user-yield",
		WorkflowName: "unattended-ask-user",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-ask-user-yield",
			ChatID:       "chat-ask-user-yield",
			Thread:       askUserYieldThread,
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "unattended-ask-user",
		},
	})
}

// askUserWaitTestWorkflow is one ask_user call on askUserYieldThread, wired
// to the run's real doorbell and interrupt handlers, returning its tool
// result. It isolates the wait from the agent loop around it, which in the
// chat's workflow would go on to read the message.
func askUserWaitTestWorkflow(ctx workflow.Context) (map[string]interface{}, error) {
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}
	setupThreadWakeHandler(ctx, tracker, "wf-ask-user-yield")
	interrupts := NewThreadInterruptCoordinator(ctx, "wf-ask-user-yield")
	call := &reliantv1.ToolCallMsg{
		Id:    "call_1",
		Name:  "ask_user",
		Input: `{"blocking":true,"questions":[{"question":"What visual style?","options":[{"label":"Clinical Minimal"},{"label":"Bold"}]}]}`,
	}
	return executeAskUserInline(ctx, call, "chat-ask-user-yield", "wf-ask-user-yield", askUserYieldThread,
		"tools", "", 0, false,
		askUserThreadInput{tracker: tracker, interrupt: interrupts.ForThread(askUserYieldThread)},
		workflow.GetLogger(ctx)), nil
}

// wait runs askUserWaitTestWorkflow and returns the tool result's content.
func (r *askUserYieldRun) wait(t *testing.T) string {
	t.Helper()
	r.env.ExecuteWorkflow(askUserWaitTestWorkflow)
	require.True(t, r.env.IsWorkflowCompleted(), "the ask_user wait must end")
	require.NoError(t, r.env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, r.env.GetWorkflowResult(&result))
	assert.Equal(t, "call_1", result["tool_call_id"])
	content, ok := result["content"].(string)
	require.True(t, ok, "ask_user must return a tool result, got %#v", result)
	return content
}

func (r *askUserYieldRun) requireResolvedUnanswered(t *testing.T) {
	t.Helper()
	require.Len(t, r.resolved, 1, "the question must be resolved, so it stops showing as pending")
	assert.Equal(t, r.questionID, r.resolved[0]["question_id"])
	assert.Equal(t, "", r.resolved[0]["response_data"], "nobody answered it")
}

// (a) A message to the asking thread ends the wait.
func TestAskUser_MessageToTheThreadEndsTheWait(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.at(t, time.Minute, func() { r.wake(askUserYieldThread) })

	content := r.wait(t)

	assert.Equal(t, askUserYieldedToMessage, content,
		"the model must be told the user sent a message instead of answering")
	r.requireResolvedUnanswered(t)
}

// The same, through the chat's workflow: the doorbell SendAgentMessage rings
// ends the wait, and the run does not end with the message unread — the
// wake no turn has read yet earns one (late_wake.go), which here is a
// continue-as-new because this graph has no call_llm after its tools.
func TestAskUser_MessageToTheThreadReachesATurnInTheChatsRun(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.at(t, time.Minute, func() { r.wake(askUserYieldThread) })

	r.executeDynamic()

	require.True(t, r.env.IsWorkflowCompleted(), "the ask_user wait must end")
	var canErr *workflow.ContinueAsNewError
	require.ErrorAs(t, r.env.GetWorkflowError(), &canErr,
		"the message must get a turn, not be left unread when the run ends")
	r.requireResolvedUnanswered(t)
	assert.Contains(t, r.scheduled, "QuestionResolve")
}

// (b) An interrupt of the asking thread ends the wait.
func TestAskUser_InterruptOfTheThreadEndsTheWait(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.at(t, time.Minute, func() { r.interrupt(askUserYieldThread) })

	content := r.run(t)

	assert.Equal(t, askUserYieldedToInterrupt, content,
		"the model must be told the user interrupted the question")
	r.requireResolvedUnanswered(t)
}

// Input for another thread of the chat is not this thread's: every thread
// shares the run's doorbell and interrupt signals, and the payload is what
// names the recipient.
func TestAskUser_InputForAnotherThreadDoesNotEndTheWait(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.at(t, time.Minute, func() {
		r.wake("some-other-thread")
		r.interrupt("some-other-thread")
	})
	r.at(t, 2*time.Minute, r.answer)

	content := r.wait(t)

	assert.Contains(t, content, "Clinical Minimal", "the wait must have lasted until the answer")
	assert.Empty(t, r.resolved, "an answered question is resolved by the answer, not by the workflow")
}

// Another agent's message (spawn_send rings the same doorbell, without
// FromUser) is not the user's: the user's question stays theirs to answer.
func TestAskUser_AnAgentsMessageDoesNotEndTheWait(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.at(t, time.Minute, func() {
		r.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: askUserYieldThread, Reason: threadwake.ReasonMailbox})
	})
	r.at(t, 2*time.Minute, r.answer)

	content := r.wait(t)

	assert.Contains(t, content, "Clinical Minimal", "the wait must have lasted until the answer")
	assert.Empty(t, r.resolved)
}

// An answer that beat the yield to the question row is the answer: the
// QuestionResolve activity keeps it and reports it, and the model gets it.
func TestAskUser_AnswerThatBeatTheYieldIsKept(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.resolveReply = map[string]interface{}{
		"success":          true,
		"already_resolved": true,
		"response_data":    `{"answers":[{"question":"What visual style?","selected":["Bold"]}]}`,
	}
	r.at(t, time.Minute, func() { r.wake(askUserYieldThread) })

	content := r.wait(t)

	assert.Contains(t, content, "Bold", "the user's answer must win over the yield")
	assert.NotEqual(t, askUserYieldedToMessage, content)
}

// A wait parked before the yield shipped becomes interruptible once the new
// worker runs it. Replaying that run's history, every wake and interrupt it
// already recorded asks GetVersion for its own observation and gets
// DefaultVersion (there is no marker), so the replay keeps waiting through it
// exactly as the old code did. The next input, observed live, records a
// marker and ends the wait. Modelled here by pinning the first observation to
// DefaultVersion.
func TestAskUser_WaitParkedByAnOlderBuildYieldsToTheNextInput(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)
	r.env.OnGetVersion(askUserYieldVersionID("question-yield", 1), workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	r.at(t, time.Minute, func() { r.wake(askUserYieldThread) })
	r.at(t, 2*time.Minute, func() { r.interrupt(askUserYieldThread) })

	content := r.wait(t)

	assert.Equal(t, askUserYieldedToInterrupt, content,
		"the observation an old history recorded keeps waiting; the next one ends the wait")
	r.requireResolvedUnanswered(t)
}

// The 24h timeout and the answer path are unchanged.
func TestAskUser_UnansweredQuestionStillTimesOut(t *testing.T) {
	t.Parallel()
	r := newAskUserYieldRun(t)

	content := r.wait(t)

	assert.Equal(t, "The user did not respond in time.", content)
	require.Len(t, r.resolved, 1)
}
