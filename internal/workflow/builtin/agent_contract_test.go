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
	// The pending_inbox term keeps the loop alive for one more turn when a
	// message landed in the mailbox while the last response was streaming.
	// Without it the loop exits holding an undelivered message (call_llm
	// delivers BEFORE reading history, so a mid-turn arrival misses that turn).
	//
	// The aborted term keeps it alive when the last turn was cut short rather
	// than finished. A cancelled stream returns zero tool calls, which is
	// indistinguishable here from "the model is done" — without this the loop
	// exits mid-task and the thread is reported as complete (chat 7da3935c,
	// thread 5e3fe370, killed mid-edit and reported to its parent as a
	// success). Unlike pending_inbox it cannot wedge: it is recomputed from
	// each turn's own stream, so a healthy turn clears it.
	agentWhileExpr             = `(outputs.tool_calls != null && size(outputs.tool_calls) > 0) || outputs.has_feedback == true || outputs.pending_inbox == true || outputs.aborted == true`
	edgeCallLLMToApproval      = `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0 && inputs.mode == 'manual'`
	edgeCallLLMToExecuteTools  = `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0 && inputs.mode != 'manual'`
	edgeCallLLMToAskQuestion   = `(nodes.call_llm.tool_calls == null || size(nodes.call_llm.tool_calls) == 0) && inputs.ask`
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
				"tool_calls":   makeToolCalls(1),
				"has_feedback": false,
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
