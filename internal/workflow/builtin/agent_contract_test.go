// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// =============================================================================
// AGENT.YAML CONTRACT EXPRESSION VERIFICATION
// =============================================================================
//
// These tests load the actual agent.yaml, extract its CEL expressions, and
// verify they match the expected constants below.
// If someone changes the YAML expressions, these tests fail.

const (
	// stop_reason is the one verdict on the turn that just ran; the loop
	// continues on the four values where the model has more to do. Each is a
	// production regression this loop once exited through:
	//
	//   tool_use     the model asked for tools
	//   incomplete   the provider paused the turn (chat b43b41fe: announced
	//                its next step, no tool call, exited as if finished)
	//   truncated    output cap hit mid-thought (chat 2308c394: 868s, 64000
	//                output tokens, reported complete)
	//   interrupted  stream cut mid-flight (chat 7da3935c, thread 5e3fe370:
	//                killed mid-edit, reported to its parent as a success)
	//
	// has_feedback / pending_inbox are user input, not a verdict on the turn:
	// pending_inbox keeps the loop alive for one more turn when a message
	// landed in the mailbox while the last response was streaming (call_llm
	// delivers BEFORE reading history, so a mid-turn arrival misses that turn).
	//
	// structured-agent.yaml STOPS on truncated. That is not an
	// inconsistency: it is a sentinel loop, where re-entering on a truncated
	// turn re-issues an identical doomed request, while here re-entering
	// resumes genuinely unfinished work.
	//
	// The ask_user edge excludes incomplete: a paused turn is not finished,
	// so there is nothing to ask the user yet.
	agentWhileExpr             = `outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted'] || outputs.has_feedback == true || outputs.pending_inbox == true`
	edgeCallLLMToApproval      = `size(nodes.call_llm.tool_calls) > 0 && inputs.mode == 'manual'`
	edgeCallLLMToExecuteTools  = `size(nodes.call_llm.tool_calls) > 0 && inputs.mode != 'manual'`
	edgeCallLLMToAskQuestion   = `size(nodes.call_llm.tool_calls) == 0 && nodes.call_llm.stop_reason != 'incomplete' && inputs.ask`
	edgeApprovalToExecuteTools = `nodes.approval.status == 'approved'`
	edgeExecuteToolsToCompact  = `nodes.execute_tools.thread_token_count > nodes.call_llm.compaction_threshold`
)

func TestContractExpressionsMatchAgentYAML(t *testing.T) {
	t.Parallel()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		t.Fatalf("failed to read agent.yaml: %v", err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		t.Fatalf("failed to parse agent.yaml: %v", err)
	}

	// Find agent_loop node
	var loopWhileExpr string
	type edgeInfo struct {
		label     string
		condition string
	}
	var inlineEdges []edgeInfo

	for _, node := range wf.GetNodes() {
		if node.GetId() != "agent_loop" {
			continue
		}
		loopArgs := node.GetLoop()
		if loopArgs == nil {
			t.Fatalf("agent_loop has no loop args")
		}

		if loopArgs.GetWhile() != nil {
			loopWhileExpr = loopArgs.GetWhile().GetExpr()
		}

		inline := loopArgs.GetInline()
		if inline == nil {
			t.Fatal("agent_loop has no inline workflow")
		}

		for _, edge := range inline.GetEdges() {
			for _, c := range edge.GetCases() {
				if c.GetLabel() != "" && c.GetCondition() != "" {
					inlineEdges = append(inlineEdges, edgeInfo{
						label:     c.GetLabel(),
						condition: c.GetCondition(),
					})
				}
			}
		}
		break
	}

	if loopWhileExpr == "" {
		t.Fatal("agent_loop node not found or has no while expression")
	}

	// Verify while expression
	if loopWhileExpr != agentWhileExpr {
		t.Errorf("while expression mismatch:\n  yaml:     %q\n  expected: %q\nUpdate the constants in v3/agent_contract_test.go and builtin/agent_contract_test.go", loopWhileExpr, agentWhileExpr)
	}

	// Build edge expression map
	edgeExprs := map[string]string{}
	for _, edge := range inlineEdges {
		edgeExprs[edge.label] = edge.condition
	}

	edgeChecks := map[string]string{
		"require_approval": edgeCallLLMToApproval,
		"auto_approve":     edgeCallLLMToExecuteTools,
		"ask_user":         edgeCallLLMToAskQuestion,
		"approved":         edgeApprovalToExecuteTools,
		"compact":          edgeExecuteToolsToCompact,
	}

	for label, expectedExpr := range edgeChecks {
		actual, ok := edgeExprs[label]
		if !ok {
			found := make([]string, 0, len(edgeExprs))
			for k := range edgeExprs {
				found = append(found, k)
			}
			t.Errorf("edge label %q not found in agent.yaml inline edges (found: %v)", label, found)
			continue
		}
		if actual != expectedExpr {
			t.Errorf("edge %q expression mismatch:\n  yaml:     %q\n  expected: %q\nUpdate the constants in v3/agent_contract_test.go and builtin/agent_contract_test.go", label, actual, expectedExpr)
		}
	}
}

// TestContractAgentYAMLExpressionsEvaluate loads the actual expressions from
// agent.yaml and runs them through the evaluator with representative contexts.
// This ensures that if the YAML expressions change, they remain evaluable
// (no syntax or type errors).
func TestContractAgentYAMLExpressionsEvaluate(t *testing.T) {
	t.Parallel()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		t.Fatalf("failed to read agent.yaml: %v", err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		t.Fatalf("failed to parse agent.yaml: %v", err)
	}

	// Find agent_loop node and extract expressions
	var loopWhileExpr string
	type edgeEntry struct {
		label     string
		condition string
	}
	var inlineEdgeExprs []edgeEntry

	for _, node := range wf.GetNodes() {
		if node.GetId() != "agent_loop" {
			continue
		}
		loopArgs := node.GetLoop()
		if loopArgs == nil {
			t.Fatal("agent_loop has no loop args")
		}

		if loopArgs.GetWhile() != nil {
			loopWhileExpr = loopArgs.GetWhile().GetExpr()
		}

		inline := loopArgs.GetInline()
		if inline == nil {
			t.Fatal("no inline workflow")
		}

		for _, edge := range inline.GetEdges() {
			for _, c := range edge.GetCases() {
				if c.GetCondition() != "" {
					inlineEdgeExprs = append(inlineEdgeExprs, edgeEntry{
						label:     c.GetLabel(),
						condition: c.GetCondition(),
					})
				}
			}
		}
		break
	}

	if loopWhileExpr == "" {
		t.Fatal("agent_loop node not found")
	}

	makeToolCalls := func(n int) []interface{} {
		calls := make([]interface{}, n)
		for i := range calls {
			calls[i] = map[string]interface{}{
				"id":   "call_123",
				"type": "function",
				"name": "read_file",
			}
		}
		return calls
	}

	t.Run("while_expression_from_yaml", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Iter: &model.IterContext{Iteration: 5},
			Outputs: map[string]interface{}{
				"tool_calls":    makeToolCalls(1),
				"has_feedback":  false,
				"pending_inbox": false,
				"stop_reason":   "tool_use",
			},
			Inputs: map[string]interface{}{
				"max_turns": 200,
			},
		}

		result, err := wfcel.EvaluateBool(loopWhileExpr, ctx)
		if err != nil {
			t.Fatalf("while expression failed to evaluate: %v", err)
		}
		if !result {
			t.Error("expected while=true for iteration 5 with tool calls")
		}
	})

	t.Run("edge_expressions_from_yaml", func(t *testing.T) {
		for _, e := range inlineEdgeExprs {
			t.Run("edge_"+e.label, func(t *testing.T) {
				ctx := &wfcel.EdgeEvalContext{
					Nodes: map[string]interface{}{
						"call_llm": map[string]interface{}{
							"tool_calls":           makeToolCalls(1),
							"compaction_threshold": 185000,
							"stop_reason":          "tool_use",
						},
						"approval": map[string]interface{}{
							"status": "approved",
						},
						"execute_tools": map[string]interface{}{
							"thread_token_count": 200000,
						},
					},
					Inputs: map[string]interface{}{
						"mode": "auto",
						"ask":  true,
						"model": map[string]interface{}{
							"tags":                 []string{"flagship"},
							"compaction_threshold": 185000,
						},
					},
				}

				_, err := wfcel.EvaluateBool(e.condition, ctx)
				if err != nil {
					t.Fatalf("edge %q expression %q failed to evaluate: %v", e.label, e.condition, err)
				}
			})
		}
	})
}
