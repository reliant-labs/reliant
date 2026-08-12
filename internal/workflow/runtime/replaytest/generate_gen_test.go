// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

// Fixture generators: each test drives one representative workflow shape
// end-to-end against a real (ephemeral) Temporal dev server through the
// production worker registration path, then exports the resulting history to
// fixtures/<name>.json. Run via `make replay-fixtures`.
//
// The scenarios deliberately mirror the e2e story suite's shapes
// (e2e/stories/story_*.go) so the pinned histories correspond to flows that
// are known-good end-to-end.

package replaytest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestGenerateFixture_AgentToolLoop pins the plain agent loop:
// CreateChat(builtin://agent) → CallLLM → ExecuteTools (real bash through the
// local daemon execution path) → CallLLM → end turn → completion.
func TestGenerateFixture_AgentToolLoop(t *testing.T) {
	script := NewScriptedLLM(
		Turn{
			Text: "Let me run that command.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, `{"command":"echo replay-fixture-agent"}`),
			},
		},
		Turn{Text: "All done — the command ran successfully."},
	)
	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Please echo something for me", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted(), "agent loop must not over-consume the script")

	h.ExportHistory(workflowID, "agent_tool_loop")
}

// TestGenerateFixture_StructuredAgentLoop pins the inline sub-workflow + loop
// shape (builtin://structured-agent): a loop node with an inline agent
// pipeline (call_llm → execute_tools edges), one regular-tool iteration and
// one response-tool iteration.
func TestGenerateFixture_StructuredAgentLoop(t *testing.T) {
	script := NewScriptedLLM(
		Turn{
			Text: "Let me check something first.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, `{"command":"echo replay-fixture-structured"}`),
			},
		},
		Turn{
			Text: "Done, submitting my response.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-resp-1", "submit_response", `{"choice":"complete","value":"all good"}`),
			},
		},
	)
	h := newHarness(t, script)

	created := h.CreateChat("builtin://structured-agent", "Do a check, then finish", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted())

	h.ExportHistory(workflowID, "structured_agent_loop")
}

// TestGenerateFixture_Spawn pins the spawn shape: a spawn tool call dispatches
// the child agent DETACHED — its own call_llm/execute_tools loop runs inside
// this same Temporal workflow execution (InlineWorkflowExecutor via
// dispatchSpawnBackground / runSpawnInlineChild — see workflow.go) via a
// tracked workflow.Go goroutine, while the spawn tool call itself settles
// immediately with a dispatch handle instead of waiting for the child.
//
// Because the parent no longer blocks on the child, its very next call_llm
// turn (the exit candidate, since it has no tool calls of its own) races the
// child's single turn — either may consume the scripted turn first depending
// on Temporal's coroutine scheduling. Whichever runs second still leaves the
// parent's loop blocked on InlineLoopExecutor.awaitLiveDetachedSpawns until
// the child's completion lands in the parent's mailbox, so the parent takes
// one more call_llm turn after that to react to it. Four scripted turns
// total: parent-turn-1 (spawn call) + {parent-exit-candidate, child's-turn}
// in either order + parent's final turn (after mailbox delivery).
func TestGenerateFixture_Spawn(t *testing.T) {
	script := NewScriptedLLM(
		// Turn 1: parent delegates to a sub-agent via the spawn tool.
		Turn{
			Text: "I'll delegate this to a sub-agent.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-spawn-1", "spawn", `{"preset":"implementer","prompt":"Echo something for the parent."}`),
			},
		},
		// Turns 2-3: race between the parent's exit-candidate turn and the
		// spawned child's only turn — neither has tool calls, so either
		// ordering ends its respective loop the same way.
		Turn{Text: "No tool calls needed right now."},
		Turn{Text: "Child agent here — done."},
		// Turn 4: parent's final turn, reacting to the mailbox-delivered
		// spawn result, no more tool calls.
		Turn{Text: "The sub-agent finished; all done."},
	)
	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Please delegate a small task to a sub-agent", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted(), "spawn must not over-consume the script")

	h.ExportHistory(workflowID, "spawn")
}

// replayRouterWorkflowYAML is a minimal pitch-deck-shaped workflow: an
// LLM-backed node router (like pitch-deck's `classify`) that dynamically
// dispatches to an inline builtin://agent sub-workflow node. It is stored as
// a user workflow draft so the production CreateChat validation + runtime
// ActivityLoadWorkflow draft-loading paths are the ones captured in history.
const replayRouterWorkflowYAML = `name: replay-router
apiVersion: "0.0.5"
description: |
  Minimal router-dispatch workflow used to pin replay compatibility of the
  node-router path (pitch-deck-like classify -> phase dispatch).

entry: [classify]

inputs:
  model:
    type: model
    default:
      tags: [flagship]
  mode:
    type: enum
    enum: [manual, auto]
    default: auto
  tools:
    type: tools
    default: ["tag:default"]

nodes:
  - id: classify
    type: router
    model:
      tags: [fast, smart]
    system_prompt: |
      Classify the user's request to determine which phase to run.
    nodes:
      - id: do_work
        description: "Run the working agent to handle the request."
      - id: skip_work
        description: "Nothing to do - acknowledge and stop."
    fallback: do_work

  - id: do_work
    type: workflow
    ref: builtin://agent
    args:
      tools: "{{inputs.tools}}"
      model: "{{inputs.model}}"
      mode: auto
    thread:
      mode: new
      inject:
        role: user
        content: "Do the work for this request."
    save_message:
      role: assistant
      content: |
        ## Work complete
        {{output.response_text}}

  - id: skip_work
    type: save_message
    args:
      role: assistant
      content: "Nothing to do."

outputs:
  selected: "{{nodes.classify.selected_node}}"
`

// TestGenerateFixture_RouterDispatch pins the router-dispatch shape: an LLM
// routing decision (CallLLM with the node_routing_decision response tool)
// followed by dynamic dispatch into an inline agent sub-workflow.
func TestGenerateFixture_RouterDispatch(t *testing.T) {
	script := NewScriptedLLM(
		// Turn 1: the router's routing decision (response tool).
		Turn{
			ToolCalls: []message.ToolCall{
				ToolCall("call-route-1", "node_routing_decision", `{"selected_node":"do_work","reasoning":"The user asked for work to be done."}`),
			},
		},
		// Turn 2: the dispatched agent's final answer (no tools → end turn).
		Turn{Text: "The work is done."},
	)
	h := newHarness(t, script)

	// Store the router workflow as a usable user draft (production custom
	// workflow path).
	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       "replay-router",
		Slug:       "replay-router",
		Definition: replayRouterWorkflowYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "create router workflow draft")

	created := h.CreateChat("replay-router", "Please do the work", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted())

	h.ExportHistory(workflowID, "router_dispatch")
}

// TestGenerateFixture_PauseResume pins the signal machinery: an ask_question
// block (signal.question.*), a user pause (signal.pause) delivered while
// blocked, question resolution with feedback (loop re-enters and parks on the
// epoch-broadcast pause Await), resume (signal.resume), a second turn, and a
// final Continue that completes the workflow.
func TestGenerateFixture_PauseResume(t *testing.T) {
	script := NewScriptedLLM(
		// Turn 1: no tool calls + ask=true → ask_question blocks the loop.
		Turn{Text: "Here is my first answer."},
		// Turn 2: runs after feedback + resume.
		Turn{Text: "Updated answer incorporating your feedback."},
	)
	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Answer, then wait for my feedback", map[string]any{
		"mode": "auto",
		"ask":  true,
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. Block on the pending question.
	q1 := h.WaitPendingQuestion(chatID)

	// 2. User pause while blocked (signal.pause; DB status → paused).
	require.NoError(t, h.Pause.PauseWorkflow(h.Ctx, workflowID, chatID, "replay fixture pause"))

	// 3. Resolve WITH feedback → has_feedback=true → the loop wants to
	// re-enter, hits the pause boundary, and parks on workflow.Await until
	// the resume epoch advances. The sleep gives the workflow time to
	// actually reach the Await so the fixture pins the blocked→resumed shape.
	h.ResolveQuestion(q1.ID, []string{"Provide feedback"}, "Please also mention the weather.")
	time.Sleep(2 * time.Second)

	// 4. Resume (signal.resume) → turn 2 plays → ask_question again.
	require.NoError(t, h.Pause.ResumeWorkflow(h.Ctx, workflowID, chatID))
	q2 := h.WaitPendingQuestion(chatID)
	require.NotEqual(t, q1.ID, q2.ID, "second ask_question must be a fresh question")

	// 5. Continue with no feedback → loop exits → completion.
	h.ResolveQuestion(q2.ID, []string{"Continue"}, "")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted())

	h.ExportHistory(workflowID, "pause_resume")
}

// TestGenerateFixture_Compaction pins the compaction edge: a scripted turn
// reports token usage far above a tiny compaction_threshold, so after
// execute_tools the loop routes into the compact node (summary via the
// scripted driver's canned compaction path), then the conversation continues
// in a fresh context window and completes.
func TestGenerateFixture_Compaction(t *testing.T) {
	script := NewScriptedLLM(
		Turn{
			Text: "Working on it.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, `{"command":"echo replay-fixture-compact"}`),
			},
			TokenCount: 5000,
		},
		Turn{Text: "All finished after compaction."},
	)
	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Do something token-heavy", map[string]any{
		"mode": "auto",
		// compaction_threshold rides on the model input object (see
		// agent.yaml: args.compaction_threshold ← inputs.model.compaction_threshold).
		"model": map[string]any{"id": "mock", "compaction_threshold": 100},
	})
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)
	assert.False(t, h.LLM.Exhausted())

	h.ExportHistory(workflowID, "compaction")
}
