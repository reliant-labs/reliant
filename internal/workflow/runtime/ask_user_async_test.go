// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// An async ask_user (input async: true) posts its question and returns at
// once: the agent's loop goes on with no wait on the answer, and the answer —
// delivered later by the question service as a message from the user — is
// read from the thread's mailbox at a later turn, like any message the user
// sends it. research/ASYNC_ASK_USER.md.

const asyncAskUserInput = `{"async":true,"questions":[{"question":"Which database?",` +
	`"options":[{"label":"Postgres (Recommended)","description":"d"},{"label":"SQLite","description":"d"}]}]}`

func asyncAskUserToolCall(id string) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": "ask_user", "input": asyncAskUserInput}
}

// questionActivities records QuestionCreate and QuestionResolve calls.
type questionActivities struct {
	mu       sync.Mutex
	created  []map[string]interface{}
	resolved []map[string]interface{}
}

func (q *questionActivities) register(env interface {
	RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions)
}) {
	env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (map[string]interface{}, error) {
			var payload map[string]interface{}
			if err := json.Unmarshal(input, &payload); err != nil {
				return nil, err
			}
			q.mu.Lock()
			q.created = append(q.created, payload)
			q.mu.Unlock()
			return map[string]interface{}{"question_id": "q-async", "already_resolved": false}, nil
		},
		activity.RegisterOptions{Name: "QuestionCreate"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			q.mu.Lock()
			q.resolved = append(q.resolved, input)
			q.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "QuestionResolve"},
	)
}

func TestAskUserAsync_ReturnsAtOnceAndTheAnswerArrivesAsAMessage(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(t, env)
	// Turn 1 asks without waiting; turn 2 carries on with no tool calls.
	e.script = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{asyncAskUserToolCall("call_async")}},
	}
	var questions questionActivities
	questions.register(env)

	// The user answers while the agent is on its next turn: the question
	// service queues the answer in the thread's mailbox and rings its
	// doorbell as the user's input, as SendAgentMessage does.
	const answer = "Answer to the question you asked without waiting (ask_user, question_id: q-async):\n\nQ: Which database?\nA: SQLite"
	e.midTurn = map[int]func(string){2: func(thread string) {
		e.mailbox.enqueue(thread, answer)
		e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonMailbox, FromUser: true})
	}}

	start := env.Now()
	e.execute("chat-async-ask")

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// Posted, async.
	require.Len(t, questions.created, 1)
	created := questions.created[0]
	assert.Equal(t, true, created["async"], "the question must be recorded as async")
	assert.Equal(t, "call_async", created["tool_call_id"])
	assert.Contains(t, created["metadata"], `"async":true`, "the metadata tells clients it is async")
	assert.Empty(t, questions.resolved, "nothing waits on an async question, so the workflow never resolves it")

	// Not waited on: the loop went on to its next turn at once, with no
	// timer (the blocking wait's is 24h, which the test clock would skip to).
	assert.Less(t, env.Now().Sub(start), time.Hour, "the loop must not wait on the question")
	require.GreaterOrEqual(t, e.parentTurns(), 2, "the loop must continue after the async ask")

	// The answer reached the thread as a message, and a turn read it.
	assert.True(t, e.readInThisRun(answer),
		"the answer must be drained from the mailbox into a turn's history; parent took %d turns", e.parentTurns())
	delivered, queued := e.mailbox.snapshot(e.parentThread)
	assert.Contains(t, delivered, answer)
	assert.Empty(t, queued)
}

// asyncAskUserWaitWorkflow is one async ask_user call, outside the agent loop.
func asyncAskUserWaitWorkflow(ctx workflow.Context) (map[string]interface{}, error) {
	tracker := &ChildWorkflowTracker{children: make(map[string]bool)}
	setupThreadWakeHandler(ctx, tracker, "wf-ask-user-async")
	interrupts := NewThreadInterruptCoordinator(ctx, "wf-ask-user-async")
	call := &reliantv1.ToolCallMsg{Id: "call_async", Name: "ask_user", Input: asyncAskUserInput}
	return executeAskUserInline(ctx, call, "chat-ask-user-async", "wf-ask-user-async", "thread-async",
		"tools", "", 0, false,
		askUserThreadInput{tracker: tracker, interrupt: interrupts.ForThread("thread-async")},
		workflow.GetLogger(ctx)), nil
}

func TestAskUserAsync_ToolResultTellsTheModelToCarryOn(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var questions questionActivities
	questions.register(env)

	env.ExecuteWorkflow(asyncAskUserWaitWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "call_async", result["tool_call_id"])
	assert.Equal(t, asyncAskUserResponse("q-async"), result["content"])
	assert.Nil(t, result["is_error"])
	assert.Empty(t, questions.resolved)
}

// A history recorded before the option existed, in which a model passed
// async: true anyway, replays the blocking wait it took then: the GetVersion
// it reaches has no marker, so it answers DefaultVersion.
func TestAskUserAsync_AnOlderHistoryReplaysTheBlockingWait(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.OnGetVersion(askUserAsyncVersionID("call_async"), workflow.DefaultVersion, askUserAsyncVersion).Return(workflow.DefaultVersion)
	var questions questionActivities
	questions.register(env)

	env.ExecuteWorkflow(asyncAskUserWaitWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "The user did not respond in time.", result["content"], "the blocking path waits until its timeout")
	require.Len(t, questions.created, 1)
	assert.Nil(t, questions.created[0]["async"], "the blocking path records a blocking question")
	assert.Len(t, questions.resolved, 1)
}

func TestAskUserInputAsync(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input map[string]interface{}
		want  bool
	}{
		{map[string]interface{}{"async": true}, true},
		{map[string]interface{}{"async": "true"}, true},
		{map[string]interface{}{"async": " TRUE "}, true},
		{map[string]interface{}{"async": false}, false},
		{map[string]interface{}{"async": "no"}, false},
		{map[string]interface{}{}, false},
		{nil, false},
	} {
		assert.Equal(t, tc.want, askUserInputAsync(tc.input), "%#v", tc.input)
	}
}
