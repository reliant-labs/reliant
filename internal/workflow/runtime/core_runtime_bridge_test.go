package runtime

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
)

func TestCompileRuntimeSemantics_SubWorkflowContracts(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name: "root",
		Nodes: []*reliantv1.Node{
			{
				Id:   "inline_node",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Inline: &reliantv1.Workflow{Name: "inline-child"},
				}},
			},
			{
				Id:   "ref_node",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://child"}},
				}},
			},
			{
				Id:   "wrapper",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Inline: &reliantv1.Workflow{
						Name: "nested-wrapper",
						Nodes: []*reliantv1.Node{{
							Id:   "nested_ref",
							Type: "workflow",
							Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
								Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://nested-child"}},
							}},
						}},
					},
				}},
			},
		},
	}

	semantics, err := CompileRuntimeSemantics(workflowDef, "builtin://agent")
	if err != nil {
		t.Fatalf("CompileRuntimeSemantics returned error: %v", err)
	}

	inlineContract, ok := semantics.ContractForNode("inline_node")
	if !ok {
		t.Fatalf("missing inline contract")
	}
	if inlineContract.WorkflowIdentity != "builtin://agent" {
		t.Fatalf("inline workflow identity mismatch: got %q", inlineContract.WorkflowIdentity)
	}
	if inlineContract.InputPolicy != core.InputPolicyInlineInheritParentInputs {
		t.Fatalf("inline input policy mismatch: got %q", inlineContract.InputPolicy)
	}

	refContract, ok := semantics.ContractForNode("ref_node")
	if !ok {
		t.Fatalf("missing ref contract")
	}
	if refContract.WorkflowIdentity != "builtin://child" {
		t.Fatalf("ref workflow identity mismatch: got %q", refContract.WorkflowIdentity)
	}
	if refContract.InputPolicy != core.InputPolicyRefPresetsArgsDefaults {
		t.Fatalf("ref input policy mismatch: got %q", refContract.InputPolicy)
	}

	if _, hasWrapper := semantics.ContractForNode("wrapper"); !hasWrapper {
		t.Fatalf("expected top-level wrapper contract")
	}
	if _, hasNested := semantics.ContractForNode("wrapper/nested_ref"); hasNested {
		t.Fatalf("expected nested node path contracts to be filtered from runtime map")
	}
}

func TestSimplifiedStateMachine_FindTriggeredNodes_PropagatesCoreProcessorErrors(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
			{Id: "done", Type: "save_message"},
		},
		Edges: []*reliantv1.Edge{{
			From: "call_llm",
			Cases: []*reliantv1.EdgeCase{
				{Condition: "nodes.call_llm.tool_calls[", To: []string{"done"}},
			},
		}},
	}

	stateMachine := NewSimplifiedStateMachine("wf", workflowDef)
	_, err := stateMachine.FindTriggeredNodes(
		[]*core.WorkflowEvent{{WorkflowID: "wf", WorkflowName: "wf", StepID: "call_llm"}},
		map[string]interface{}{"call_llm": map[string]interface{}{"tool_calls": []interface{}{}}},
		map[string]interface{}{},
	)
	if err == nil {
		t.Fatalf("expected routing error from malformed CEL condition, got nil")
	}
	if !strings.Contains(err.Error(), "process workflow events") {
		t.Fatalf("expected FindTriggeredNodes context wrapper, got: %v", err)
	}
	if !strings.Contains(err.Error(), "evaluate edge case condition") {
		t.Fatalf("expected processor edge-condition context, got: %v", err)
	}
}

func TestCompileRuntimeSemantics_CompileErrorsIncludeBridgeContext(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name: "broken",
		Nodes: []*reliantv1.Node{{
			Id:   "bad_ref",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://broken::inline"}},
			}},
		}},
	}

	_, err := CompileRuntimeSemantics(workflowDef, "builtin://root")
	if err == nil {
		t.Fatalf("expected CompileRuntimeSemantics to fail for non-canonical ref")
	}
	if !strings.Contains(err.Error(), "compile core semantics") {
		t.Fatalf("expected bridge compile context, got: %v", err)
	}
	if !strings.Contains(err.Error(), "non-canonical workflow ref") {
		t.Fatalf("expected compile root cause context, got: %v", err)
	}
}
