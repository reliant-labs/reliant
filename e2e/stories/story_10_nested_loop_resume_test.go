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

// nestedLoopYAML is a GENERIC nested loop: a top-level `outer` loop whose inline
// body is an `inner` work loop. This is the engine-level shape that get-it-right
// (a review loop nested under a parent) shares, but stripped of get-it-right's
// specifics — the fix is engine-level and applies to ANY nested loop.
//
// The distinction that matters: the FLAT position checkpoint records only
// TOP-LEVEL loop iterations, so it can name {outer, 0} but has no field for the
// INNER loop's iteration. The coarse fresh-restart-with-checkpoint therefore
// cannot resume the inner loop precisely — it would re-enter `outer` and restart
// `inner` from iteration 0. Reset-and-replay rebuilds the whole goroutine stack
// (the inner loop runs INLINE in the same Temporal execution), so it resumes at
// the inner iteration. This story pins that difference end-to-end.
const nestedLoopYAML = `
name: resume-nested-loop
description: Generic nested loop for the reset-and-replay resume story.
entry: [outer]

inputs:
  model:
    type: model
    description: LLM model to use
    default:
      tags: ["flagship"]

outputs:
  response_text: "{{nodes.outer.response_text}}"

nodes:
  - id: outer
    type: loop
    while: iter.iteration < 1
    inline:
      outputs:
        response_text: "{{nodes.inner.response_text}}"
      entry: [inner]
      nodes:
        - id: inner
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
                  system_prompt: "NESTED-WORKER"
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
`

// Story 10 (Finding-1 regression): a run is killed mid-INNER-loop and the user's
// next message must RESUME AT THE INNER ITERATION via reset-and-replay — NOT
// restart the inner loop at zero. The flat checkpoint can only record the
// top-level {outer, 0}, so this precision comes entirely from replay rebuilding
// the nested stack, which is what the coarse restart cannot do.
func TestStory10_TerminateMidNestedLoopResumesAtInnerIteration(t *testing.T) {
	// Not t.Parallel(): reset-and-replay re-runs the interrupted `sleep` tool on
	// the resumed run, so this story is CPU/worker-heavy. Running it in the serial
	// phase (before the parallel batch) keeps the shared dev-server/worker from
	// saturating and tripping the harness completion wait under load.

	script := NewScriptedLLM(
		// inner iteration 0: quick tool call.
		Turn{
			Text:      "Inner step one.",
			ToolCalls: []message.ToolCall{ToolCall("call-echo", tools.ShellToolName, `{"command":"echo inner-one"}`)},
		},
		// inner iteration 1: slow tool call — the story kills the run while this
		// command sleeps, so the interruption lands mid inner-iteration 1.
		Turn{
			Text:      "Inner step two.",
			ToolCalls: []message.ToolCall{ToolCall("call-sleep", tools.ShellToolName, `{"command":"sleep 3"}`)},
		},
	)

	h := newHarness(t, script)

	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       "resume-nested-loop",
		Slug:       "resume-nested-loop",
		Definition: nestedLoopYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "seed nested-loop workflow draft")

	created := h.CreateChat("resume-nested-loop", "Do the nested work", map[string]any{})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. Wait until the run is provably mid INNER-loop iteration 1: inner
	//    iteration 1's assistant tool_use (call-sleep) is persisted, meaning that
	//    LLM turn completed and execute_tools is now sleeping.
	h.eventually("run to reach inner-loop iteration 1 with the slow tool_use persisted", func() (bool, string) {
		for _, m := range h.Messages(chatID, workflowID) {
			for _, b := range m.Blocks {
				if b.ToolCallID != nil && *b.ToolCallID == "call-sleep" {
					return true, ""
				}
			}
		}
		return false, "call-sleep tool_use not persisted yet"
	})

	// The FLAT checkpoint can only name the TOP-LEVEL loop: it records
	// {outer, 0} and has no field for the inner iteration. This is exactly why
	// the coarse restart cannot resume the inner loop and reset-and-replay must.
	cp, err := h.Stack.Repo.GetWorkflowCheckpoint(h.Ctx, workflowID)
	require.NoError(t, err)
	require.NotNil(t, cp, "position checkpoint must exist mid-run")
	assert.Equal(t, "outer", cp.NodeID, "the flat checkpoint records only the TOP-LEVEL loop node")
	assert.EqualValues(t, 0, cp.LoopIteration, "the inner loop iteration is invisible to the flat checkpoint")

	callsBeforeKill := len(h.LLM.StreamCalls())
	require.Equal(t, 2, callsBeforeKill, "two inner iterations ran before the kill")

	// 2. Kill the run mid inner-loop (wedge-recovery / operator terminate shape).
	require.NoError(t, h.Stack.Temporal.TerminateWorkflow(h.Ctx, workflowID, "", "e2e: kill mid nested loop"),
		"terminate workflow")

	// 3. Script the post-resume turn, then send a message. SendMessage maps
	//    Temporal TERMINATED -> failed and, because replayable history exists,
	//    takes the RESET-AND-REPLAY path.
	h.LLM.Append(Turn{Text: "Nested work finished."})
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

	// 4. THE FINDING-1 ASSERTION. Reset-and-replay replays inner iterations 0 and
	//    1 from history (their CallLLM activities are NOT re-executed) and
	//    continues from the interrupted iteration, so EXACTLY ONE more LLM turn
	//    runs. Had the run coarse-restarted the inner loop at iteration 0, the
	//    LLM would have been re-called for iterations 0, 1 and 2 (three more
	//    turns). One more turn is the proof it resumed at the inner iteration.
	calls := h.LLM.StreamCalls()
	require.Len(t, calls, callsBeforeKill+1,
		"reset-and-replay resumes at the INNER iteration (iterations 0..1 replayed, not re-run); a nested-loop-at-zero restart would re-call the LLM three times")
	assert.False(t, h.LLM.Exhausted())

	resumedCall := calls[len(calls)-1]
	resumedPrompts := strings.Join(resumedCall.Prompts, "\n")
	assert.Contains(t, resumedPrompts, "NESTED-WORKER",
		"resumed run re-enters the nested work loop directly")

	// 5. Thread continuity + fresh re-run of the interrupted tool: the resumed
	//    call sees the user's message, and the mid-flight call-sleep was re-run
	//    to a real (non-error) result rather than left dangling.
	var sawContinue, sawSleepResult bool
	for _, m := range resumedCall.Messages {
		for _, tc := range m.TextContents() {
			if strings.Contains(tc.Text, "continue") {
				sawContinue = true
			}
		}
		for _, tr := range m.ToolResults() {
			if tr.ToolCallID == "call-sleep" {
				sawSleepResult = true
				assert.False(t, tr.IsError,
					"reset-and-replay re-runs the interrupted tool fresh")
			}
		}
	}
	assert.True(t, sawContinue, "resumed history must carry the user's resume message")
	assert.True(t, sawSleepResult, "the interrupted inner-loop tool call must be resolved by re-running it fresh")

	// 6. The run completed cleanly and the final assistant message is persisted.
	var sawFinal bool
	for _, m := range h.Messages(chatID, workflowID) {
		if strings.Contains(TextOf(m), "Nested work finished.") {
			sawFinal = true
		}
	}
	assert.True(t, sawFinal, "post-resume assistant message must be persisted")
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)
}
