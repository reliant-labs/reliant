// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"go.temporal.io/sdk/workflow"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// Continue-as-new with a background spawn blocked in a blocking ask_user.
//
// Chat b57e7021: a sub-agent asked a blocking question at loop iteration 54
// and waited on it. A spawn parks only at an iteration boundary, so it never
// parked; the soft handoff waited on it, and every spawn launched after the
// handoff was requested parked at iteration 0 and did nothing, until the hard
// backstop. Carrying it from its last boundary would not help either: the
// successor would re-run CallLLM for iteration 54, which fills the dangling
// ask_user as interrupted and has the model ask a NEW question.
//
// The question wait is a park point: the spawn parks inside it with the
// iteration's pending tool batch, the handoff carries it, and the successor
// re-enters the batch, not CallLLM. QuestionCreate's idempotency (same
// workflow, step, iteration and tool call) reattaches the SAME question, whose
// answer then resolves it.

// questionRows is an in-memory questions table keyed like QuestionCreate's
// idempotency check, shared by a predecessor and its successor.
type questionRows struct {
	mu       sync.Mutex
	byKey    map[string]string
	created  []string // question ids, one per row actually inserted
	creates  int      // QuestionCreate calls
	resolved []string
}

func newQuestionRows() *questionRows { return &questionRows{byKey: map[string]string{}} }

func (q *questionRows) register(env interface {
	RegisterActivityWithOptions(interface{}, activity.RegisterOptions)
}) {
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.creates++
		raw, _ := json.Marshal([]interface{}{in["workflow_id"], in["step_id"], in["loop_iteration"], in["tool_call_id"]})
		key := string(raw)
		id, ok := q.byKey[key]
		if !ok {
			id = "q-" + in["tool_call_id"].(string) + "-" + string(rune('0'+len(q.created)))
			q.byKey[key] = id
			q.created = append(q.created, id)
		}
		return map[string]interface{}{"question_id": id, "already_resolved": false}, nil
	}, activity.RegisterOptions{Name: "QuestionCreate"})
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		q.mu.Lock()
		q.resolved = append(q.resolved, in["question_id"].(string))
		q.mu.Unlock()
		return map[string]interface{}{"already_resolved": false}, nil
	}, activity.RegisterOptions{Name: "QuestionResolve"})
}

func blockingAskUserCall(id string) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"name":  "ask_user",
		"input": `{"blocking":true,"questions":[{"question":"Which way?","options":[{"label":"A"},{"label":"B"}]}]}`,
	}
}

// Spawn A blocks in ask_user; history crosses the soft threshold; the root
// spawns B, which parks at its first boundary. The handoff must not wait on A
// (the run would stall until the hard backstop): A parks inside its question
// wait, and the continuation carries A with its pending question and B at 0.
// The successor reattaches A to the SAME question, without a new CallLLM for
// that iteration, and the answer resolves it.
func TestContinueAsNew_SpawnBlockedInQuestionCarriesPendingQuestion(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) { runQuestionHandoff(t, legacy) })
	}
}

func runQuestionHandoff(t *testing.T, legacy bool) {
	const chatID = "chat-can-question"
	childA, childB := canChildThread("tc-a"), canChildThread("tc-b")
	parent := "thread-" + chatID
	rows := newQuestionRows()

	var suite1 temporaltest.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	e1 := newCANSpawnEnv(t, env1)
	rows.register(env1)
	e1.scripts[parent] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-a", "decide with the user")}},
		{toolCalls: []map[string]interface{}{spawnToolCall("tc-b", "later work")}},
		{toolCalls: nil}, // the root parks on its spawns
	}
	askCall := blockingAskUserCall("tc-ask")
	if legacy {
		askCall["input"] = strings.Replace(askCall["input"].(string), `"blocking":true,`, "", 1)
		env1.OnGetVersion(askUserAsyncVersionID("tc-ask"), workflow.DefaultVersion, askUserAsyncByDefault).Return(workflow.DefaultVersion)
	}
	e1.scripts[childA] = []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{noopToolCall("a-work")}},
		{toolCalls: []map[string]interface{}{askCall}},
	}
	e1.scripts[childB] = repeatTurns("b", 3)

	pressure := newPressureDial(historyPressureNone)
	forcePressure(t, chatID, pressure.get)
	e1.onLLM = func(thread string, n int) {
		// A's asking turn: the question is about to be created and waited on.
		if thread == childA && n == 2 {
			pressure.set(historyPressureSoft)
		}
	}

	env1.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	carried := carriedInput(t, env1)

	require.Len(t, rows.created, 1, "A asked exactly one question")
	questionID := rows.created[0]
	require.Empty(t, rows.resolved, "the handoff must not resolve the pending question")
	require.Equal(t, 2, e1.llmCalls[childA], "A is waiting on its question, not taking turns")
	require.Zero(t, e1.llmCalls[childB], "B parked at its first boundary")

	require.NotNil(t, carried.Resume)
	byThread := map[string]SpawnHandoff{}
	for _, h := range carried.Resume.Spawns {
		byThread[h.ChildThread] = h
	}
	require.Len(t, byThread, 2, "both spawns cross the boundary")
	a, b := byThread[childA], byThread[childB]
	require.Equal(t, "tc-a", a.ToolCallID)
	require.Equal(t, 1, a.LoopIteration, "A is carried at the iteration it asked in")
	require.NotNil(t, a.PendingToolBatch, "A carries its pending tool batch, question included")
	require.Equal(t, "tc-b", b.ToolCallID)
	require.Zero(t, b.LoopIteration)
	require.Nil(t, b.PendingToolBatch)

	// Successor: fresh history. A re-enters its question wait on the same
	// question; the user answers it; A reads the answer and finishes.
	pressure.set(historyPressureNone)
	var suite2 temporaltest.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	e2 := newCANSpawnEnv(t, env2)
	rows.register(env2)
	e2.scripts[childB] = repeatTurns("rb", 1)
	var llmCallsAtAnswer int
	env2.RegisterDelayedCallback(func() {
		e2.mu.Lock()
		llmCallsAtAnswer = e2.llmCalls[childA]
		e2.mu.Unlock()
		env2.SignalWorkflow("signal.question."+questionID, map[string]interface{}{
			"action": "reply", "response_data": "A",
		})
	}, time.Hour)

	env2.ExecuteWorkflow(DynamicWorkflow, carried)
	require.True(t, env2.IsWorkflowCompleted())
	require.NoError(t, env2.GetWorkflowError())

	require.Len(t, rows.created, 1, "the successor reattached the same question; no new one was asked")
	require.Zero(t, llmCallsAtAnswer, "A did not call the LLM for the asking iteration again")
	require.NotEmpty(t, e2.llmIterations[childA])
	require.Equal(t, 2, e2.llmIterations[childA][0], "A's first turn in the successor is the one after the answer")
	require.Empty(t, rows.resolved, "an answered question is not resolved unanswered")
	for _, tc := range []string{"tc-a", "tc-b"} {
		require.Equal(t, 1, e2.enqueuedFor(tc), "the parent receives exactly one completion for %s", tc)
		require.Contains(t, e2.toolStatusesFor(tc), "completed")
	}
}
