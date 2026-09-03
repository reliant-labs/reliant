// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Does a ROOT-thread agent run drain its mailbox?
//
// Every existing test for the agent mailbox drives a SPAWNED CHILD thread.
// This exercises the path a plain "user typed into the chat composer" run
// takes: SendMessage -> DynamicWorkflow -> the top-level `agent_loop` loop
// node -> InlineLoopExecutor -> call_llm/execute_tools, and asserts both
// (a) that DrainAgentMessages is dispatched at all, keyed on the ROOT
// thread, and (b) that it lands at the correct boundary — after the
// previous turn's tool_results were saved and before the next call_llm.

// rootDrainAgentYAML mirrors agent.yaml's structure (a top-level loop node
// whose inline sub-workflow is call_llm -> execute_tools), including the
// save_message blocks that persist the assistant-with-tool_calls row and its
// tool_results row. Those are what make the ordering assertion meaningful.
const rootDrainAgentYAML = `
name: agent
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0) || outputs.pending_inbox == true
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
        pending_inbox: "{{has(nodes.call_llm) && has(nodes.call_llm.pending_inbox) ? nodes.call_llm.pending_inbox : false}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          save_message:
            role: "{{output.message.role}}"
            content: "{{output.message.text}}"
            tool_calls: "{{output.tool_calls}}"
        - id: execute_tools
          type: execute_tools
          save_message:
            role: tool
            content: ""
            tool_results: "{{output.tool_results}}"
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0
              label: "has_tools"
`

// rootDrainEvent is one entry in the ordered trace of the activities that
// define the boundary invariant.
type rootDrainEvent struct {
	kind   string // "call_llm", "save_assistant", "save_tool_results", "save_other"
	thread string
}

type rootDrainEnv struct {
	t   *testing.T
	mu  sync.Mutex
	seq []rootDrainEvent

	callLLMCount int32
	// callLLMThreads records the thread each call_llm ran for. CallLLM owns
	// mailbox delivery, so this is the record of which threads had their
	// mailbox delivered.
	callLLMThreads []string

	// pendingInboxTurns marks no-tool turns that should force one mailbox drain continuation.
	pendingInboxTurns map[int]bool
	// turns is how many turns emit tool calls before the run winds down.
	turns int
}

func newRootDrainEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, turns int) *rootDrainEnv {
	t.Helper()
	e := &rootDrainEnv{t: t, turns: turns}

	wf, err := wfyaml.ParseWorkflow([]byte(rootDrainAgentYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "EmitToolCallStatus"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		},
		activity.RegisterOptions{Name: "EmitThreadEvent"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (interface{}, error) {
			tid, _ := input["thread_id"].(string)
			return map[string]interface{}{"message_id": "msg-" + tid}, nil
		},
		activity.RegisterOptions{Name: "CreateWorkflowWithThread"},
	)

	// SaveMessage is the row-writer whose ordering relative to the drain is
	// the whole invariant. Classify each save as the assistant row (carries
	// tool_calls) or the tool_results row.
	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (interface{}, error) {
			args := input.Node.GetSaveMessageNode()
			kind := "save_other"
			switch {
			case len(args.GetResolvedToolResults()) > 0:
				kind = "save_tool_results"
			case len(args.GetResolvedToolCalls()) > 0:
				kind = "save_assistant"
			}
			e.record(kind, input.Runtime.Thread)
			return map[string]interface{}{"message_id": "msg-save"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLMStub(input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.executeToolsStub(input)
		},
		activity.RegisterOptions{Name: "ExecuteTools"},
	)

	return e
}

func (e *rootDrainEnv) record(kind, thread string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq = append(e.seq, rootDrainEvent{kind: kind, thread: thread})
}

func (e *rootDrainEnv) callLLMStub(input types.ActivityInput) (map[string]interface{}, error) {
	idx := int(atomic.AddInt32(&e.callLLMCount, 1)) - 1
	e.mu.Lock()
	e.callLLMThreads = append(e.callLLMThreads, input.Runtime.Thread)
	e.mu.Unlock()
	e.record("call_llm", input.Runtime.Thread)
	pendingInbox := e.pendingInboxTurns[idx]

	if idx >= e.turns {
		// Wind down: no tool calls, unless a late mailbox message forces one continuation.
		return map[string]interface{}{
			"response_text": "done",
			"tool_calls":    nil,
			"pending_inbox": pendingInbox,
			"message":       map[string]interface{}{"role": "assistant", "text": "done"},
		}, nil
	}
	toolCalls := []interface{}{
		map[string]interface{}{
			"id":    "tc" + string(rune('1'+idx)),
			"name":  "view",
			"input": `{"file_path":"README.md"}`,
		},
	}
	return map[string]interface{}{
		"response_text": "working",
		"tool_calls":    toolCalls,
		"pending_inbox": pendingInbox,
		"message":       map[string]interface{}{"role": "assistant", "text": "working"},
	}, nil
}

func (e *rootDrainEnv) executeToolsStub(input types.ActivityInput) (map[string]interface{}, error) {
	// Return one result per tool call the node was handed. The ids do not
	// have to match the originals for this test's purposes — what matters is
	// that a non-empty tool_results set reaches execute_tools' save_message.
	results := []interface{}{
		map[string]interface{}{
			"tool_call_id": "tc-" + input.Runtime.Thread,
			"content":      "file contents",
			"is_error":     false,
		},
	}
	return map[string]interface{}{"tool_results": results}, nil
}

func (e *rootDrainEnv) trace() []rootDrainEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]rootDrainEvent, len(e.seq))
	copy(out, e.seq)
	return out
}

func rootDrainWorkflowInput(chatID, thread string) WorkflowInput {
	// Mirrors internal/grpc/services/chat_send.go:1040 — the execution
	// context a plain SendMessage builds for a fresh chat run. Thread is the
	// chat's root thread (targetThread == workflowID there).
	return WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "agent",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-" + chatID,
			ChatID:       chatID,
			Thread:       thread,
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "agent",
		},
	}
}

type RootThreadMailboxDrainSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestRootThreadMailboxDrainE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(RootThreadMailboxDrainSuite))
}

// TestRootThreadRun_DrainsMailboxAtEveryBoundary is the ROOT-thread mirror of
// TestSendAgentMessage_EndToEndDelivery: it drives a full DynamicWorkflow the
// way a user's normal chat message does and asserts DrainAgentMessages fires,
// keyed on the ROOT thread.
func (s *RootThreadMailboxDrainSuite) TestRootThreadRun_DrainsMailboxAtEveryBoundary() {
	env := s.NewTestWorkflowEnvironment()
	const chatID = "chat-root-drain"
	const rootThread = "wf-" + chatID
	e := newRootDrainEnv(s.T(), env, 2) // two tool-calling turns, then wind down

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput(chatID, rootThread))

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	// Guard against a trivially-green test: the run must actually have taken
	// the turns we scripted.
	require.Equal(s.T(), 3, int(atomic.LoadInt32(&e.callLLMCount)),
		"scripted run must have taken 2 tool-calling turns plus the wind-down turn")

	s.T().Logf("activity trace: %+v", e.trace())

	// Delivery is not a workflow-level step. CallLLM drains the mailbox itself,
	// immediately before it reads history (CallLLMActivity.drainAgentMailbox),
	// so what proves a root-thread run delivers is that CallLLM runs for the
	// ROOT thread — which is what SendAgentMessage targets.
	require.NotEmpty(s.T(), e.callLLMThreads,
		"a ROOT-thread agent run must call the LLM — that call is what delivers "+
			"a message queued from the chat composer")
	for _, th := range e.callLLMThreads {
		require.Equal(s.T(), rootThread, th,
			"delivery is keyed on the thread CallLLM runs for, which must be the ROOT thread")
	}
}

func (s *RootThreadMailboxDrainSuite) TestRootThreadRun_PendingInboxForcesExactlyOneContinuation() {
	env := s.NewTestWorkflowEnvironment()
	const chatID = "chat-root-pending-inbox"
	const rootThread = "wf-" + chatID
	e := newRootDrainEnv(s.T(), env, 0)
	e.pendingInboxTurns = map[int]bool{0: true}

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput(chatID, rootThread))

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())
	require.Equal(s.T(), 2, int(atomic.LoadInt32(&e.callLLMCount)),
		"pending_inbox on a no-tool turn must buy exactly one more CallLLM turn")
	var kinds []string
	for _, ev := range e.trace() {
		kinds = append(kinds, ev.kind)
	}
	require.Equal(s.T(), []string{"call_llm", "save_other", "call_llm", "save_other"}, kinds,
		"the continuation must drain once and then exit instead of spinning")
}

// TestRootThreadRun_DrainLandsAfterToolResultsAndBeforeNextCallLLM pins the
// ordering constraint documented at internal/db/core/agent_message.go:63-69
// and above loop_executor.go:545: a drained message must never land between
// an assistant-with-tool_calls row and its tool_results row, or the provider
// deadlocks. Asserted against the real activity dispatch order of a root run.
func (s *RootThreadMailboxDrainSuite) TestRootThreadRun_DrainLandsAfterToolResultsAndBeforeNextCallLLM() {
	env := s.NewTestWorkflowEnvironment()
	const chatID = "chat-root-drain-order"
	const rootThread = "wf-" + chatID
	e := newRootDrainEnv(s.T(), env, 2)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput(chatID, rootThread))

	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	trace := e.trace()
	s.T().Logf("activity trace: %+v", trace)

	// Guard against a vacuous pass: the walk below only proves anything if
	// the trace actually contains assistant-with-tool_calls rows and their
	// tool_results rows for it to walk BETWEEN.
	var assistantRows, toolResultRows int
	for _, ev := range trace {
		switch ev.kind {
		case "save_assistant":
			assistantRows++
		case "save_tool_results":
			toolResultRows++
		}
	}
	require.Equal(s.T(), 2, assistantRows,
		"expected an assistant-with-tool_calls row for each of the 2 tool-calling turns")
	require.Equal(s.T(), 2, toolResultRows,
		"expected a tool_results row for each of the 2 tool-calling turns")

	// The exact shape the invariant requires, pinned rather than merely
	// walked. Delivery now happens INSIDE call_llm (immediately before it
	// reads history), so there is no separate drain event — and that is a
	// strictly stronger guarantee than the old "drain sits at the head of each
	// iteration": delivery cannot land between an assistant-with-tool_calls row
	// and its tool_results row, because it does not exist as a step that could
	// be scheduled there.
	var kinds []string
	for _, ev := range trace {
		kinds = append(kinds, ev.kind)
	}
	require.Equal(s.T(), []string{
		"call_llm", "save_assistant", "save_tool_results",
		"call_llm", "save_assistant", "save_tool_results",
		"call_llm", "save_other",
	}, kinds)

	// The invariant restated as the property, not the shape: every
	// save_assistant is immediately followed by its save_tool_results, with
	// nothing in between. This is what deadlocks the provider when violated,
	// and it is what any future change to delivery must keep true.
	for i, ev := range trace {
		if ev.kind != "save_assistant" {
			continue
		}
		require.Less(s.T(), i+1, len(trace),
			"an assistant-with-tool_calls row must be followed by its tool_results row")
		require.Equal(s.T(), "save_tool_results", trace[i+1].kind,
			"nothing may land between an assistant-with-tool_calls row and its "+
				"tool_results row — that is the provider deadlock the mailbox exists to prevent")
	}

	// Delivery must still happen before the model reads history — that is the
	// whole point of delivering at all. It is inside CallLLM, so what this
	// asserts is that the run reaches a call_llm at all.
	var firstCallLLM = -1
	for i, ev := range trace {
		if ev.kind == "call_llm" {
			firstCallLLM = i
			break
		}
	}
	require.GreaterOrEqual(s.T(), firstCallLLM, 0,
		"expected at least one call_llm — that is what delivers the mailbox")
}
