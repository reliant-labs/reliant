// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/stopreason"
)

// The two loop families read the same stop_reason and need OPPOSITE responses
// to some of its values. These tests pin every value against both loops so a
// future "consistency" cleanup cannot quietly align them, and so a new value
// cannot be added without deciding what each loop does with it.
//
// Evaluated the way the runtime does it: InlineLoopExecutor.evaluateWhileCondition
// builds a LoopEvalContext from the iteration's MATERIALIZED outputs (every
// declared output is assigned), so every key a while-condition references is
// present.

// allStopReasons is the closed vocabulary. If stopreason grows, this list —
// and both tables below — must grow with it.
var allStopReasons = []string{
	stopreason.Interrupted, stopreason.ToolUse, stopreason.Refused, stopreason.Truncated,
	stopreason.Incomplete, stopreason.Error, stopreason.Done,
}

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
func agentOutputs(stopReason string) map[string]interface{} {
	return map[string]interface{}{
		"tool_calls":    []interface{}{},
		"message":       map[string]interface{}{"role": "assistant", "text": ""},
		"response_text": "",
		"has_feedback":  false,
		"pending_inbox": false,
		"stop_reason":   stopReason,
	}
}

// structuredOutputs mirrors the full set structured-agent.yaml declares.
func structuredOutputs(completed bool, stopReason string) map[string]interface{} {
	return map[string]interface{}{
		"response":      nil,
		"completed":     completed,
		"has_feedback":  false,
		"pending_inbox": false,
		"stop_reason":   stopReason,
	}
}

var (
	agentInputs = map[string]interface{}{"mode": "auto", "ask": false}
	// max_turns 0 is the shipped default and means "no ceiling".
	structuredInputs = map[string]interface{}{"max_turns": 0, "response_tool_name": "respond"}
)

// agent.yaml is a TOOL-PRESENCE loop: it continues exactly when the model has
// more to do, and yields to the user otherwise.
func TestAgentLoopStopReasonPolicy(t *testing.T) {
	t.Parallel()
	expr := whileExprOf(t, "agent.yaml", "agent_loop")

	want := map[string]struct {
		continues bool
		why       string
	}{
		stopreason.ToolUse:     {true, "the tools run and their results are new input"},
		stopreason.Incomplete:  {true, "the provider paused the turn and expects it handed back (chat b43b41fe announced its next step and then stopped)"},
		stopreason.Truncated:   {true, "the model ran out of room mid-thought (chat 2308c394: 868s, 64000 tokens, reported complete)"},
		stopreason.Interrupted: {true, "our stream was cut mid-flight (chat 7da3935c: killed mid-edit, reported as a success)"},
		stopreason.Done:        {false, "a finished turn must yield to the user; continuing is what once wedged threads"},
		stopreason.Refused:     {false, "an unchanged request is refused the same way"},
		stopreason.Error:       {false, "a provider failure is surfaced to the user, not retried in a loop"},
	}
	for _, reason := range allStopReasons {
		w, ok := want[reason]
		if !ok {
			t.Fatalf("stop_reason %q has no agent.yaml policy in this test — decide whether it continues", reason)
		}
		t.Run(reason, func(t *testing.T) {
			if got := evalWhile(t, expr, agentOutputs(reason), agentInputs, 1); got != w.continues {
				t.Fatalf("agent loop on stop_reason=%q: continues=%v, want %v — %s", reason, got, w.continues, w.why)
			}
		})
	}

	t.Run("pending inbox continues even after done", func(t *testing.T) {
		out := agentOutputs(stopreason.Done)
		out["pending_inbox"] = true
		if !evalWhile(t, expr, out, agentInputs, 1) {
			t.Fatal("a queued user message must earn another turn regardless of how the last one ended")
		}
	})

	t.Run("feedback continues even after done", func(t *testing.T) {
		out := agentOutputs(stopreason.Done)
		out["has_feedback"] = true
		if !evalWhile(t, expr, out, agentInputs, 1) {
			t.Fatal("user feedback must earn another turn regardless of how the last one ended")
		}
	})

	// An unrecognized value must not hold the loop open. The runtime never
	// emits one (the vocabulary is closed and absent values are filled), but
	// a positive match is what makes that true by construction.
	for _, odd := range []interface{}{"", nil, "some_future_value"} {
		t.Run("unrecognized stop_reason does not loop forever", func(t *testing.T) {
			out := agentOutputs(stopreason.Done)
			out["stop_reason"] = odd
			if evalWhile(t, expr, out, agentInputs, 1) {
				t.Fatalf("stop_reason=%v must NOT continue the loop — match positively on the values that earn another turn", odd)
			}
		})
	}
}

// structured-agent.yaml is a SENTINEL loop: it runs until the response tool
// is called. Its exit is `completed`, not stop_reason; stop_reason only stops
// it from spinning on the two outcomes where another identical request
// cannot make progress. Every other value must behave exactly as it did
// before stop_reason existed.
func TestStructuredAgentStopReasonPolicy(t *testing.T) {
	t.Parallel()
	expr := whileExprOf(t, "structured-agent.yaml", "agent_loop")

	stops := map[string]string{
		stopreason.Truncated: "the sentinel cannot appear, so re-entering re-issues the same request that just truncated — and max_turns defaults to unbounded",
		stopreason.Refused:   "retrying an unchanged request is refused identically",
	}
	for _, reason := range allStopReasons {
		t.Run(reason+" before the sentinel", func(t *testing.T) {
			got := evalWhile(t, expr, structuredOutputs(false, reason), structuredInputs, 1)
			if why, shouldStop := stops[reason]; shouldStop {
				if got {
					t.Fatalf("structured agent must STOP on stop_reason=%q — %s", reason, why)
				}
				return
			}
			if !got {
				t.Fatalf("structured agent must keep working toward the response tool on stop_reason=%q; "+
					"only the sentinel (or truncated/refused) ends it", reason)
			}
		})
		t.Run(reason+" after the sentinel", func(t *testing.T) {
			if evalWhile(t, expr, structuredOutputs(true, reason), structuredInputs, 1) {
				t.Fatalf("the response tool was called — the loop is done, whatever stop_reason=%q says", reason)
			}
		})
	}

	// Ending on the response tool is a tool_use turn whose payload validated.
	// A tool_use turn whose payload did NOT validate (schema_validation_failure)
	// must loop again — completed decides, not stop_reason.
	t.Run("tool_use without a valid response continues", func(t *testing.T) {
		if !evalWhile(t, expr, structuredOutputs(false, stopreason.ToolUse), structuredInputs, 1) {
			t.Fatal("a response-tool call that failed validation must earn another turn")
		}
	})

	t.Run("max_turns still bounds it", func(t *testing.T) {
		inputs := map[string]interface{}{"max_turns": 3, "response_tool_name": "respond"}
		if evalWhile(t, expr, structuredOutputs(false, stopreason.Done), inputs, 3) {
			t.Fatal("max_turns must end the loop")
		}
	})

	// An unrecognized value must not stop the loop early: the sentinel has not
	// been reached, and an unknown verdict says nothing about whether progress
	// is possible.
	for _, odd := range []interface{}{"", nil, "some_future_value"} {
		t.Run("unrecognized stop_reason does not stop the loop early", func(t *testing.T) {
			out := structuredOutputs(false, stopreason.Done)
			out["stop_reason"] = odd
			if !evalWhile(t, expr, out, structuredInputs, 1) {
				t.Fatalf("stop_reason=%v must NOT end the sentinel loop early", odd)
			}
		})
	}
}

// Every builtin loop whose body calls an LLM must decide "is the model done?"
// from stop_reason. A loop gated on tool_calls alone reads a truncated, cut, or
// paused turn as finished — the exact class of bug stop_reason exists to end —
// and a new builtin copying an old loop would silently reintroduce it.
func TestBuiltinLLMLoopsReadStopReason(t *testing.T) {
	t.Parallel()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		t.Fatalf("read builtin workflows: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		wf, err := v2.ParseWorkflowProtoBytes(data)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, node := range wf.GetNodes() {
			loop := node.GetLoop()
			if loop == nil || loop.GetWhile() == nil || loop.GetInline() == nil {
				continue
			}
			callsLLM := false
			for _, inner := range loop.GetInline().GetNodes() {
				if inner.GetType() == model.NodeTypeCallLLM {
					callsLLM = true
					break
				}
			}
			if !callsLLM {
				continue
			}
			checked++
			expr := loop.GetWhile().GetExpr()
			if !strings.Contains(expr, "outputs.stop_reason") {
				t.Errorf("%s: loop %q runs call_llm but its while-condition does not read stop_reason:\n  %s\n"+
					"Gate on stop_reason (internal/workflow/stopreason) — tool_calls alone cannot tell "+
					"a finished turn from a truncated, interrupted, or paused one.",
					entry.Name(), node.GetId(), expr)
			}
			if strings.Contains(expr, "size(outputs.tool_calls)") {
				t.Errorf("%s: loop %q still gates on size(outputs.tool_calls); stop_reason 'tool_use' already covers it:\n  %s",
					entry.Name(), node.GetId(), expr)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no builtin loop that runs call_llm — this guard is not testing anything")
	}
}

// The polarity difference is intentional. If someone "fixes" the inconsistency
// by aligning them, one of the two bugs comes back.
func TestLoopFamiliesDisagreeOnTruncationDeliberately(t *testing.T) {
	t.Parallel()

	agentContinues := evalWhile(t,
		whileExprOf(t, "agent.yaml", "agent_loop"),
		agentOutputs(stopreason.Truncated), agentInputs, 1)

	sentinelContinues := evalWhile(t,
		whileExprOf(t, "structured-agent.yaml", "agent_loop"),
		structuredOutputs(false, stopreason.Truncated), structuredInputs, 1)

	if agentContinues == sentinelContinues {
		t.Fatalf("both loop families responded identically to a truncated turn "+
			"(agent=%v sentinel=%v). They must differ: a tool-presence loop resumes "+
			"unfinished work, while a sentinel loop would re-issue the identical "+
			"request that just truncated, forever.",
			agentContinues, sentinelContinues)
	}
}
