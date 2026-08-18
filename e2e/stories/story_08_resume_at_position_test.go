// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// resumeTwoPhaseYAML is a two-phase workflow modeled on the pitch-deck
// incident: a phase-one planning node followed by a phase-two work loop.
// The distinctive system prompts (PHASE-ONE-PLANNER / PHASE-TWO-WORKER) let
// the scripted LLM prove which node issued each request.
const resumeTwoPhaseYAML = `
name: resume-two-phase
description: Two-phase workflow for the resume-at-position story.
entry: [plan_step]

inputs:
  model:
    type: model
    description: LLM model to use
    default:
      tags: ["flagship"]

outputs:
  response_text: "{{nodes.work_loop.response_text}}"

nodes:
  - id: plan_step
    type: call_llm
    # The plan is persisted as a user-role note so the work loop's history
    # still ends with a user turn (an assistant-tail history is rejected by
    # call_llm). Multi-phase production workflows pass phase outputs via node
    # outputs / forked threads; thread mechanics are not what this story tests.
    save_message:
      role: user
      content: "PLAN NOTES: {{output.message.text}}"
    args:
      model: "{{inputs.model}}"
      system_prompt: "PHASE-ONE-PLANNER"

  - id: work_loop
    type: loop
    while: outputs.tool_calls != null && size(outputs.tool_calls) > 0
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
        response_text: "{{nodes.call_llm.response_text}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          save_message:
            role: "{{output.message.role}}"
            content: "{{output.message.text}}"
            tool_calls: "{{output.tool_calls}}"
          args:
            model: "{{inputs.model}}"
            system_prompt: "PHASE-TWO-WORKER"
            tools_config:
              filter: ["tag:default"]
              permission: "mutating"
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
              label: "run_tools"

edges:
  - from: plan_step
    default: work_loop
`

// Story 08: a run is killed mid-loop (Temporal terminate — the same shape as
// a wedge-recovery terminate or an operator kill) and the user's next message
// must RESUME AT POSITION, not restart the graph.
//
// This is the pitch-deck incident test: the prior run finished phase one
// (plan_step) and was interrupted inside phase two (work_loop, iteration 1).
// Because the terminated execution still has replayable history, SendMessage
// takes the RESET-AND-REPLAY path (not the coarse fresh-restart-with-
// checkpoint): the run resets to the last decision point and replays, re-
// entering work_loop directly at iteration 1 — the scripted LLM proves no
// re-planning happened (no PHASE-ONE-PLANNER request after resume). Reset-and-
// replay re-executes the interrupted activity FRESH (the mid-flight `sleep`
// tool re-runs to a real result), rather than leaving a dangling tool call to
// be stubbed.
func TestStory08_TerminateMidLoopResumesAtPosition(t *testing.T) {
	// Not t.Parallel(): reset-and-replay re-runs the interrupted `sleep` tool on
	// the resumed run, so this story is CPU/worker-heavy. Running it in the serial
	// phase (before the parallel batch) keeps the shared dev-server/worker from
	// saturating and tripping the harness completion wait under load.

	script := NewScriptedLLM(
		// Phase one: planning turn, no tools -> plan_step completes.
		Turn{Text: "Plan ready: do the work."},
		// Phase two, iteration 0: quick tool call.
		Turn{
			Text:      "Working on step one.",
			ToolCalls: []message.ToolCall{ToolCall("call-echo", tools.ShellToolName, `{"command":"echo step-one"}`)},
		},
		// Phase two, iteration 1: slow tool call. The story kills the run
		// while this command sleeps, so the interruption lands mid-iteration
		// with the tool_use persisted but no tool_result.
		Turn{
			Text:      "Working on step two.",
			ToolCalls: []message.ToolCall{ToolCall("call-sleep", tools.ShellToolName, `{"command":"sleep 3"}`)},
		},
	)

	h := newHarness(t, script)

	// Seed the custom workflow as a user draft; CreateChat and the engine's
	// ActivityLoadWorkflow both resolve non-builtin refs by (userID, slug).
	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       "resume-two-phase",
		Slug:       "resume-two-phase",
		Definition: resumeTwoPhaseYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "seed workflow draft")

	created := h.CreateChat("resume-two-phase", "Build the thing", map[string]any{})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. Wait until the run is provably mid phase-two iteration 1:
	//    - the position checkpoint records {work_loop, 1} (written at
	//      iteration start), and
	//    - iteration 1's assistant tool_use (call-sleep) is persisted, meaning
	//      the LLM turn completed and execute_tools is now sleeping.
	h.eventually("run to reach work_loop iteration 1 with the slow tool_use persisted", func() (bool, string) {
		cp, err := h.Stack.Repo.GetWorkflowCheckpoint(h.Ctx, workflowID)
		if err != nil || cp == nil {
			return false, "no checkpoint yet"
		}
		if cp.NodeID != "work_loop" || cp.LoopIteration < 1 {
			return false, "checkpoint=" + cp.NodeID
		}
		for _, m := range h.Messages(chatID, workflowID) {
			for _, b := range m.Blocks {
				if b.ToolCallID != nil && *b.ToolCallID == "call-sleep" {
					return true, ""
				}
			}
		}
		return false, "call-sleep tool_use not persisted yet"
	})

	cp, err := h.Stack.Repo.GetWorkflowCheckpoint(h.Ctx, workflowID)
	require.NoError(t, err)
	require.NotNil(t, cp, "position checkpoint must exist mid-run")
	require.Equal(t, "work_loop", cp.NodeID)
	require.EqualValues(t, 1, cp.LoopIteration)

	callsBeforeKill := len(h.LLM.StreamCalls())
	require.Equal(t, 3, callsBeforeKill, "plan turn + two work iterations before the kill")

	// 2. Kill the run mid-loop, the way the wedge recovery (or an operator)
	//    terminates a broken execution. The DB status stays 'running' until
	//    the next SendMessage reconciles it against Temporal.
	require.NoError(t, h.Stack.Temporal.TerminateWorkflow(h.Ctx, workflowID, "", "e2e: kill mid-loop"),
		"terminate workflow")

	// 3. Script the post-resume turn, then send a message. SendMessage maps
	//    Temporal TERMINATED -> failed and starts a NEW run in resume mode.
	//    The model param rides along (as the web client re-sends params) so
	//    the fresh run's input validation resolves the mock model.
	h.LLM.Append(Turn{Text: "Resumed and finishing up."})
	modelParam, err := structpb.NewValue(map[string]any{"id": "mock"})
	require.NoError(t, err)
	_, err = h.ChatSvc.SendMessage(h.Ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "continue"},
		},
		WorkflowParams: map[string]*structpb.Value{"model": modelParam},
	}))
	require.NoError(t, err, "SendMessage after terminate")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	// 4. Resume proof: exactly ONE more LLM turn ran, and it came from the
	//    phase-two worker — the phase-one planner was never consulted again.
	calls := h.LLM.StreamCalls()
	require.Len(t, calls, callsBeforeKill+1,
		"the resumed run re-enters work_loop at iteration 1 and finishes in one turn")
	assert.False(t, h.LLM.Exhausted())

	resumedCall := calls[len(calls)-1]
	resumedPrompts := strings.Join(resumedCall.Prompts, "\n")
	assert.Contains(t, resumedPrompts, "PHASE-TWO-WORKER",
		"resumed run must enter the work loop directly")
	assert.NotContains(t, resumedPrompts, "PHASE-ONE-PLANNER",
		"resumed run must NOT re-run the planning phase (no re-planning / re-classification)")

	// 5. Thread continuity: the resumed LLM call sees the prior run's plan
	//    output and the user's resume message.
	var sawPlan, sawContinue, sawSleepResult bool
	for _, m := range resumedCall.Messages {
		for _, tc := range m.TextContents() {
			if strings.Contains(tc.Text, "Plan ready: do the work.") {
				sawPlan = true
			}
			if strings.Contains(tc.Text, "continue") {
				sawContinue = true
			}
		}
		// 6. Reset-and-replay re-executes the interrupted activity FRESH. The
		//    kill landed while call-sleep was mid-flight (tool_use persisted, no
		//    result); the resumed run re-runs execute_tools rather than leaving a
		//    dangling tool call, so by the first resumed LLM call the tool has a
		//    real (non-error) result — not an "interrupted / outcome unknown"
		//    stub.
		for _, tr := range m.ToolResults() {
			if tr.ToolCallID == "call-sleep" {
				sawSleepResult = true
				assert.False(t, tr.IsError,
					"reset-and-replay re-runs the interrupted tool fresh, so its result is a real success, not an interruption stub")
			}
		}
	}
	assert.True(t, sawPlan, "resumed history must carry the prior run's plan message")
	assert.True(t, sawContinue, "resumed history must end with the user's resume message")
	assert.True(t, sawSleepResult, "the interrupted tool call must be resolved by re-running it fresh on the resumed run")

	// 7. Completion clears the position checkpoint — the NEXT message starts
	//    a fresh run at graph entry.
	cp, err = h.Stack.Repo.GetWorkflowCheckpoint(h.Ctx, workflowID)
	require.NoError(t, err)
	assert.Nil(t, cp, "completed runs must clear their checkpoint")

	// 8. The final assistant message is persisted and the chat is healthy.
	var sawFinal bool
	for _, m := range h.Messages(chatID, workflowID) {
		if strings.Contains(TextOf(m), "Resumed and finishing up.") {
			sawFinal = true
		}
	}
	assert.True(t, sawFinal, "post-resume assistant message must be persisted")
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)
}
