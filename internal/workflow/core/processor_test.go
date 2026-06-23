package core

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func TestWorkflowProcessorProcessRoutesToMatchingCase(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Entry: []string{"call_llm"},
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
			{Id: "done", Type: "save_message"},
			{Id: "tools", Type: "execute_tools"},
		},
		Edges: []*reliantv1.Edge{{
			From: "call_llm",
			Cases: []*reliantv1.EdgeCase{
				{Condition: "size(nodes.call_llm.tool_calls) > 0", To: []string{"tools"}},
				{To: []string{"done"}},
			},
		}},
	}

	processor, err := NewWorkflowProcessor(workflowDef)
	if err != nil {
		t.Fatalf("NewWorkflowProcessor error: %v", err)
	}

	nextState, triggeredNodes, err := processor.Process(WorkflowProcessorState{}, ProcessInput{
		Events: []*WorkflowEvent{{StepID: "call_llm", WorkflowID: "wf", WorkflowName: "wf"}},
		NodeOutputs: map[string]interface{}{
			"call_llm": map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"name": "bash"}}},
		},
		WorkflowInputs: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	_ = nextState

	if len(triggeredNodes) != 1 || triggeredNodes[0].Node.GetId() != "tools" {
		t.Fatalf("expected tools route, got %#v", triggeredNodes)
	}
}

func TestWorkflowProcessorProcessFallsBackToDefaultCase(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
			{Id: "done", Type: "save_message"},
		},
		Edges: []*reliantv1.Edge{{
			From: "call_llm",
			Cases: []*reliantv1.EdgeCase{
				{Condition: "size(nodes.call_llm.tool_calls) > 0", To: []string{"never"}},
				{To: []string{"done"}},
			},
		}},
	}

	processor, err := NewWorkflowProcessor(workflowDef)
	if err != nil {
		t.Fatalf("NewWorkflowProcessor error: %v", err)
	}

	_, triggeredNodes, err := processor.Process(WorkflowProcessorState{}, ProcessInput{
		Events:         []*WorkflowEvent{{StepID: "call_llm", WorkflowID: "wf", WorkflowName: "wf"}},
		NodeOutputs:    map[string]interface{}{"call_llm": map[string]interface{}{"tool_calls": []interface{}{}}},
		WorkflowInputs: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}

	if len(triggeredNodes) != 1 || triggeredNodes[0].Node.GetId() != "done" {
		t.Fatalf("expected done fallback route, got %#v", triggeredNodes)
	}
}

func TestWorkflowProcessorProcess_EdgeCELCompilationFailureIsPropagated(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
			{Id: "done", Type: "save_message"},
		},
		Edges: []*reliantv1.Edge{{
			From: "call_llm",
			Cases: []*reliantv1.EdgeCase{
				{Condition: "size(nodes.call_llm.tool_calls", To: []string{"done"}},
				{To: []string{"done"}},
			},
		}},
	}

	processor, err := NewWorkflowProcessor(workflowDef)
	if err != nil {
		t.Fatalf("NewWorkflowProcessor error: %v", err)
	}

	_, triggeredNodes, err := processor.Process(WorkflowProcessorState{}, ProcessInput{
		Events:         []*WorkflowEvent{{StepID: "call_llm", WorkflowID: "wf", WorkflowName: "wf"}},
		NodeOutputs:    map[string]interface{}{"call_llm": map[string]interface{}{"tool_calls": []interface{}{}}},
		WorkflowInputs: map[string]interface{}{},
	})
	if err == nil {
		t.Fatalf("expected CEL compilation error, got nil")
	}
	if len(triggeredNodes) != 0 {
		t.Fatalf("expected no triggered nodes when CEL compilation fails, got %#v", triggeredNodes)
	}
	if !strings.Contains(err.Error(), "evaluate edge case condition") {
		t.Fatalf("expected edge condition context in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "size(nodes.call_llm.tool_calls") {
		t.Fatalf("expected failing condition expression context in error, got: %v", err)
	}
}

func TestWorkflowProcessorProcess_DoesNotFallThroughWhenEarlierCaseExpressionFails(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
			{Id: "fallback", Type: "save_message"},
		},
		Edges: []*reliantv1.Edge{{
			From: "call_llm",
			Cases: []*reliantv1.EdgeCase{
				{Condition: "nodes.call_llm.tool_calls[", To: []string{"fallback"}},
				{To: []string{"fallback"}},
			},
		}},
	}

	processor, err := NewWorkflowProcessor(workflowDef)
	if err != nil {
		t.Fatalf("NewWorkflowProcessor error: %v", err)
	}

	_, triggeredNodes, err := processor.Process(WorkflowProcessorState{}, ProcessInput{
		Events:         []*WorkflowEvent{{StepID: "call_llm", WorkflowID: "wf", WorkflowName: "wf"}},
		NodeOutputs:    map[string]interface{}{"call_llm": map[string]interface{}{"tool_calls": []interface{}{}}},
		WorkflowInputs: map[string]interface{}{},
	})
	if err == nil {
		t.Fatalf("expected hard error from malformed CEL case, got nil")
	}
	if len(triggeredNodes) != 0 {
		t.Fatalf("expected no fallback routing when CEL case fails, got %#v", triggeredNodes)
	}
}
