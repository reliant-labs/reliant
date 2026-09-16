// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// The two loop families need OPPOSITE responses to the same stop_kind, and
// these tests pin both so a future "consistency" cleanup cannot quietly align
// them.
//
// Evaluated the way the runtime does it: InlineLoopExecutor.evaluateWhileCondition
// builds a LoopEvalContext from the iteration's MATERIALIZED outputs (every
// declared output is assigned — evaluateOutputsMap writes nil on failure), so
// every key a while-condition references is present.

func whileExprOf(t *testing.T, file, nodeID string) string {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, node := range wf.GetNodes() {
		if node.GetId() != nodeID {
			continue
		}
		args := node.GetLoop()
		if args == nil || args.GetWhile() == nil {
			t.Fatalf("%s: node %s has no while condition", file, nodeID)
		}
		return args.GetWhile().GetExpr()
	}
	t.Fatalf("%s: node %s not found", file, nodeID)
	return ""
}

func evalWhile(t *testing.T, expr string, outputs, inputs map[string]interface{}, iteration int) bool {
	t.Helper()
	got, err := wfcel.EvaluateBool(expr, &wfcel.LoopEvalContext{
		Iter:    &model.IterContext{Iteration: iteration},
		Outputs: outputs,
		Inputs:  inputs,
	})
	if err != nil {
		t.Fatalf("while condition failed to evaluate — the runtime treats this as "+
			"fatal (%q): %v", expr, err)
	}
	return got
}

// agentOutputs mirrors the full set agent.yaml declares.
func agentOutputs(toolCalls []interface{}, stopKind string) map[string]interface{} {
	return map[string]interface{}{
		"tool_calls":    toolCalls,
		"message":       map[string]interface{}{"role": "assistant", "text": ""},
		"response_text": "",
		"has_feedback":  false,
		"pending_inbox": false,
		"aborted":       false,
		"stop_kind":     stopKind,
	}
}

// agent.yaml is a TOOL-PRESENCE loop: it under-runs on truncation, because a
// cut-off turn yields zero tool calls and reads as "the model is done". It
// must CONTINUE so the work resumes.
func TestAgentLoopContinuesOnTruncation(t *testing.T) {
	t.Parallel()
	expr := whileExprOf(t, "agent.yaml", "agent_loop")
	inputs := map[string]interface{}{"mode": "auto", "ask": false}

	t.Run("exits on a clean finish with no tool calls", func(t *testing.T) {
		if evalWhile(t, expr, agentOutputs(nil, "complete"), inputs, 1) {
			t.Fatal("a completed turn with no tool calls must END the loop and yield " +
				"to the user; continuing here is what previously wedged threads")
		}
	})

	t.Run("continues on truncation", func(t *testing.T) {
		if !evalWhile(t, expr, agentOutputs(nil, "truncated"), inputs, 1) {
			t.Fatal("a TRUNCATED turn produced no tool calls only because it ran out " +
				"of output room — the work is unfinished and the loop must continue " +
				"(chat 2308c394: 868s and 64000 output tokens, reported as complete)")
		}
	})

	t.Run("continues on tool calls regardless", func(t *testing.T) {
		if !evalWhile(t, expr, agentOutputs([]interface{}{"call"}, "complete"), inputs, 1) {
			t.Fatal("tool calls must always continue the loop")
		}
	})

	// The null guard was removed from this expression; nil must still be safe.
	t.Run("nil tool_calls does not raise", func(t *testing.T) {
		if evalWhile(t, expr, agentOutputs(nil, "complete"), inputs, 1) {
			t.Fatal("nil tool_calls must read as empty, not raise and not continue")
		}
	})

	// An ABSENT stop_kind must not hold the loop open.
	//
	// This is the bug an earlier version of this change shipped: the term was
	// written `!= 'complete'`, which is also true when stop_kind is nil or "",
	// so any shape that does not populate it — replayed iterations, every
	// scenario fixture — looped forever. Measured at 1000 iterations against
	// the agent scenarios where 2 were expected.
	for _, missing := range []struct {
		name string
		val  interface{}
	}{
		{"nil", nil},
		{"empty string", ""},
	} {
		t.Run("absent stop_kind ("+missing.name+") does not loop forever", func(t *testing.T) {
			out := agentOutputs(nil, "complete")
			out["stop_kind"] = missing.val
			if evalWhile(t, expr, out, inputs, 1) {
				t.Fatal("an absent stop_kind must NOT continue the loop. Match positively " +
					"on the values that earn another turn; an unknown value means stop.")
			}
		})
	}
}

// structured-agent.yaml is a SENTINEL loop: it continues until the response
// tool is called, so a truncated turn produces no sentinel, the condition
// stays true, and it re-issues an identical doomed request. With max_turns
// defaulting to 0 that is unbounded. It must STOP.
func TestStructuredAgentStopsOnTruncation(t *testing.T) {
	t.Parallel()
	expr := whileExprOf(t, "structured-agent.yaml", "agent_loop")
	// max_turns 0 is the shipped default and means "no ceiling".
	inputs := map[string]interface{}{"max_turns": 0, "response_tool_name": "respond"}

	outputs := func(completed bool, stopKind string) map[string]interface{} {
		return map[string]interface{}{
			"response":      nil,
			"completed":     completed,
			"has_feedback":  false,
			"pending_inbox": false,
			"stop_kind":     stopKind,
		}
	}

	t.Run("continues while incomplete and healthy", func(t *testing.T) {
		if !evalWhile(t, expr, outputs(false, "complete"), inputs, 1) {
			t.Fatal("an incomplete turn that ended cleanly must keep working toward the sentinel")
		}
	})

	t.Run("stops on truncation even though incomplete", func(t *testing.T) {
		if evalWhile(t, expr, outputs(false, "truncated"), inputs, 1) {
			t.Fatal("a truncated turn must STOP this loop: the sentinel cannot appear, " +
				"so re-entering re-issues the same full-context request that just " +
				"truncated — and max_turns defaults to 0, so nothing bounds it")
		}
	})

	t.Run("stops on refusal even though incomplete", func(t *testing.T) {
		if evalWhile(t, expr, outputs(false, "refused"), inputs, 1) {
			t.Fatal("a refused turn must STOP: retrying an unchanged request is refused identically")
		}
	})

	t.Run("stops once the sentinel is reached", func(t *testing.T) {
		if evalWhile(t, expr, outputs(true, "complete"), inputs, 1) {
			t.Fatal("the response tool was called — the loop is done")
		}
	})

	// An ABSENT stop_kind must not stop the loop early. This is the mirror of
	// the agent.yaml hazard: here a positive `== 'complete'` match would exit
	// after one turn on any shape that does not populate the field, quietly
	// breaking a loop that used to work.
	for _, missing := range []struct {
		name string
		val  interface{}
	}{
		{"nil", nil},
		{"empty string", ""},
	} {
		t.Run("absent stop_kind ("+missing.name+") does not stop the loop early", func(t *testing.T) {
			out := outputs(false, "complete")
			out["stop_kind"] = missing.val
			if !evalWhile(t, expr, out, inputs, 1) {
				t.Fatal("an absent stop_kind must NOT end this loop: the sentinel has " +
					"not been reached, and the missing field says nothing about whether " +
					"progress is possible")
			}
		})
	}
}

// The polarity difference is intentional. If someone "fixes" the inconsistency
// by aligning them, one of the two bugs comes back.
func TestLoopFamiliesDisagreeOnTruncationDeliberately(t *testing.T) {
	t.Parallel()

	agentContinues := evalWhile(t,
		whileExprOf(t, "agent.yaml", "agent_loop"),
		agentOutputs(nil, "truncated"),
		map[string]interface{}{"mode": "auto", "ask": false}, 1)

	sentinelContinues := evalWhile(t,
		whileExprOf(t, "structured-agent.yaml", "agent_loop"),
		map[string]interface{}{
			"response": nil, "completed": false, "has_feedback": false,
			"pending_inbox": false, "stop_kind": "truncated",
		},
		map[string]interface{}{"max_turns": 0, "response_tool_name": "respond"}, 1)

	if agentContinues == sentinelContinues {
		t.Fatalf("both loop families responded identically to a truncated turn "+
			"(agent=%v sentinel=%v). They must differ: a tool-presence loop resumes "+
			"unfinished work, while a sentinel loop would re-issue the identical "+
			"request that just truncated, forever.",
			agentContinues, sentinelContinues)
	}
}
