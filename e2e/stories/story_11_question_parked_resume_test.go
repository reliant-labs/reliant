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
)

// nestedQuestionYAML: a top-level `type: workflow` node ("phase") whose inline
// sub-workflow (a sub-thread) contains an ask-loop — an LLM turn then an
// ask_question each iteration. Because the ask-loop is NESTED, the flat position
// checkpoint records only {phase, 0}: it cannot express the inner iteration, so
// coarse restart would re-run the whole sub-thread from scratch. Reset-and-replay
// rebuilds the inline sub-thread stack (iteration + the parked question channel),
// which is the only way to resume a dead question-parked run precisely.
const nestedQuestionYAML = `
name: resume-question-nested
description: Nested ask-loop for the question-parked reset-and-replay resume story.
entry: [phase]

inputs:
  model:
    type: model
    description: LLM model to use
    default:
      tags: ["flagship"]

outputs:
  response_text: "{{nodes.phase.response_text}}"

nodes:
  - id: phase
    type: workflow
    inline:
      name: inner
      entry: [attempt]
      outputs:
        response_text: "{{nodes.attempt.response_text}}"
      nodes:
        - id: attempt
          type: loop
          while: outputs.has_feedback == true
          inline:
            outputs:
              has_feedback: "{{nodes.gate.has_feedback}}"
              response_text: "{{nodes.work.response_text}}"
            entry: [work]
            nodes:
              - id: work
                type: call_llm
                save_message:
                  role: assistant
                  content: "{{output.message.text}}"
                args:
                  model: "{{inputs.model}}"
                  system_prompt: "NESTED-WORKER"
              - id: gate
                type: ask_question
                args:
                  metadata: '{"type":"ask_user","questions":[{"question":"Continue or give feedback?","options":[{"label":"Continue"},{"label":"Feedback"}]}]}'
            edges:
              - from: work
                default: [gate]
`

// Story 11 (question-parked resume): a run dies while parked on an unanswered
// ask_question INSIDE a nested sub-thread. The user sends a PLAIN message (not a
// form answer). It must resume PRECISELY — reset-and-replay + deliver the message
// as the (marked) question answer — so the nested inner iteration is preserved
// (NOT restarted at zero), and the delivered answer carries the resume marker.
func TestStory11_TerminateQuestionParkedResumesViaMarkedAnswer(t *testing.T) {
	t.Parallel()

	script := NewScriptedLLM(
		Turn{Text: "Nested iteration one."},
		Turn{Text: "Nested iteration two."},
	)

	h := newHarness(t, script)

	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       "resume-question-nested",
		Slug:       "resume-question-nested",
		Definition: nestedQuestionYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "seed nested-question workflow draft")

	created := h.CreateChat("resume-question-nested", "Do the nested work", map[string]any{})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. Inner iteration 0 runs (LLM turn 1) then parks on question q0.
	q0 := h.WaitPendingQuestion(chatID)

	// The FLAT checkpoint can only name the TOP-LEVEL node — it holds {phase, 0}
	// and has no field for the inner ask-loop iteration.
	cp, err := h.Stack.Repo.GetWorkflowCheckpoint(h.Ctx, workflowID)
	require.NoError(t, err)
	require.NotNil(t, cp)
	assert.Equal(t, "phase", cp.NodeID, "only the top-level sub-workflow node is flat-checkpointed")

	// 2. Answer q0 WITH feedback → the inner loop re-enters → inner iteration 1
	//    (LLM turn 2) → parks on question q1.
	h.ResolveQuestion(q0.ID, []string{"Feedback"}, "keep going")
	var q1 *db.Question
	h.eventually("second (inner iteration 1) pending question", func() (bool, string) {
		got, e := h.Stack.Repo.GetPendingQuestionByChatID(h.Ctx, chatID)
		if e != nil || got == nil || got.ID == q0.ID {
			return false, "still q0 / none"
		}
		q1 = got
		return true, ""
	})

	callsBeforeKill := len(h.LLM.StreamCalls())
	require.Equal(t, 2, callsBeforeKill, "two inner iterations ran before the kill")

	// 3. Kill the run while parked on q1 (wedge-recovery / operator terminate).
	require.NoError(t, h.Stack.Temporal.TerminateWorkflow(h.Ctx, workflowID, "", "e2e: kill while question-parked"),
		"terminate workflow")

	// 4. The user sends a PLAIN message. SendMessage maps TERMINATED -> failed,
	//    sees the pending question, and resumes via reset-and-replay + delivering
	//    the message as the marked answer to q1 (NOT a coarse loop-restart).
	h.LLM.Append(Turn{Text: "Nested iteration three."})
	modelParam, err := structpb.NewValue(map[string]any{"id": "mock"})
	require.NoError(t, err)
	_, err = h.ChatSvc.SendMessage(h.Ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "please wrap it up"},
		},
		WorkflowParams: map[string]*structpb.Value{"model": modelParam},
	}))
	require.NoError(t, err, "SendMessage (plain) after terminate")

	// 5. The marked answer (freetext) is feedback, so the inner loop re-enters
	//    once more → inner iteration 2 (LLM turn 3) → parks on a NEW question q2.
	var q2 *db.Question
	h.eventually("post-resume pending question (inner iteration 2)", func() (bool, string) {
		got, e := h.Stack.Repo.GetPendingQuestionByChatID(h.Ctx, chatID)
		if e != nil || got == nil || got.ID == q1.ID {
			return false, "still q1 / none"
		}
		q2 = got
		return true, ""
	})

	// THE PRECISION ASSERTION. Reset-and-replay replayed inner iterations 0 and 1
	// (their CallLLM activities were NOT re-executed) and continued from the
	// parked question, so exactly ONE more LLM turn ran. A coarse restart of the
	// nested sub-thread at iteration 0 would have re-called the LLM for iterations
	// 0, 1 and 2 (three more turns).
	calls := h.LLM.StreamCalls()
	require.Len(t, calls, callsBeforeKill+1,
		"reset-and-replay resumes at the parked inner iteration (0..1 replayed, not re-run)")

	// The delivered answer carried the resume marker, and it reached the resumed
	// LLM call's conversation history.
	resumed := calls[len(calls)-1]
	var sawMarker bool
	for i := range resumed.Messages {
		if strings.Contains(resumed.Messages[i].Content().Text,
			"<system> workflow was canceled or failed. user resumed with message</system>: please wrap it up") {
			sawMarker = true
		}
	}
	assert.True(t, sawMarker, "the resumed LLM must see the exact resume marker wrapping the user's message")

	// 6. Answer q2 with Continue (a normal live answer, unmarked) → the loop
	//    exits → the workflow completes cleanly.
	h.ResolveQuestion(q2.ID, []string{"Continue"}, "")
	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)
}
