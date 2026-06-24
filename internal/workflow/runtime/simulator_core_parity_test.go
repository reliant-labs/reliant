package runtime

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

func TestWorkflowSimulator_WorkflowIdentityFromCoreContracts(t *testing.T) {
	rootWorkflow := &reliantv1.Workflow{
		Name: "builtin://agent",
		Nodes: []*reliantv1.Node{{
			Id:   "parent_ref",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref: celLiteralForSimulator("builtin://outer"),
			}},
		}},
	}

	workflowLoader := func(workflowRef string) (*reliantv1.Workflow, error) {
		if workflowRef != "builtin://outer" {
			return nil, fmt.Errorf("unexpected workflow ref: %s", workflowRef)
		}
		return &reliantv1.Workflow{
			Name: "outer",
			Nodes: []*reliantv1.Node{{
				Id:   "inline_inner",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Inline: &reliantv1.Workflow{Name: "inner"},
				}},
			}},
		}, nil
	}

	simulator := NewWorkflowSimulator(rootWorkflow, SimulatorConfig{WorkflowLoader: workflowLoader})

	if got := simulator.workflowIdentityForNodePath("parent_ref"); got != "builtin://outer" {
		t.Fatalf("parent ref workflow identity mismatch: got %q", got)
	}
	if got := simulator.workflowIdentityForNodePath("parent_ref.inline_inner"); got != "builtin://outer" {
		t.Fatalf("nested inline workflow identity mismatch: got %q", got)
	}
}

func TestWorkflowSimulator_AssembleSubWorkflowInputs_InlineInheritsParentInputs(t *testing.T) {
	rootWorkflow := &reliantv1.Workflow{
		Name: "builtin://agent",
		Nodes: []*reliantv1.Node{{
			Id:   "inline_node",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Inline: &reliantv1.Workflow{Name: "inner"},
			}},
		}},
	}

	simulator := NewWorkflowSimulator(rootWorkflow, SimulatorConfig{
		WorkflowInputs: map[string]interface{}{
			"model":       "gpt-5",
			"prompt":      "hello",
			"parent_only": "visible",
		},
	})

	node := rootWorkflow.GetNodes()[0]
	assembled := simulator.assembleSubWorkflowInputs("inline_node", node)

	if assembled["model"] != "gpt-5" || assembled["prompt"] != "hello" || assembled["parent_only"] != "visible" {
		t.Fatalf("inline input assembly should inherit parent inputs, got %#v", assembled)
	}
}

func TestWorkflowSimulator_AssembleSubWorkflowInputs_RefUsesArgsThenDefaults(t *testing.T) {
	rootWorkflow := &reliantv1.Workflow{
		Name: "builtin://agent",
		Nodes: []*reliantv1.Node{{
			Id:   "external_agent",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref: celLiteralForSimulator("builtin://one-ring"),
				Args: map[string]*structpb.Value{
					"model": structpb.NewStringValue("gpt-5"),
					"reviewer": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
						"temperature": structpb.NewNumberValue(0.8),
					}}),
				},
			}},
		}},
	}

	workflowLoader := func(workflowRef string) (*reliantv1.Workflow, error) {
		if workflowRef != "builtin://one-ring" {
			return nil, fmt.Errorf("unexpected workflow ref: %s", workflowRef)
		}
		return &reliantv1.Workflow{
			Name: "one-ring",
			Inputs: map[string]*reliantv1.Input{
				"model": stringInputWithDefaultForSimulator("claude-sonnet"),
				"reviewer": groupInputForSimulator(map[string]*reliantv1.Input{
					"temperature": numberInputWithDefaultForSimulator(0.2),
					"mode":        stringInputWithDefaultForSimulator("strict"),
				}),
			},
		}, nil
	}

	simulator := NewWorkflowSimulator(rootWorkflow, SimulatorConfig{
		WorkflowInputs: map[string]interface{}{"parent_only": "keep"},
		WorkflowLoader: workflowLoader,
	})

	node := rootWorkflow.GetNodes()[0]
	assembled := simulator.assembleSubWorkflowInputs("external_agent", node)

	if _, inherited := assembled["parent_only"]; inherited {
		t.Fatalf("ref input assembly should not inherit parent-only inputs: %#v", assembled)
	}
	if assembled["model"] != "gpt-5" {
		t.Fatalf("args should override defaults for model: %#v", assembled)
	}

	reviewer, ok := assembled["reviewer"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reviewer map in assembled inputs: %#v", assembled)
	}
	if reviewer["temperature"] != 0.8 {
		t.Fatalf("args should override reviewer.temperature default: %#v", reviewer)
	}
	if reviewer["mode"] != "strict" {
		t.Fatalf("defaults should provide missing reviewer.mode: %#v", reviewer)
	}
}

func TestWorkflowSimulator_Run_PropagatesStateMachineRoutingErrors(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name:  "sim-routing-error",
		Entry: []string{"call_llm"},
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

	simulator := NewWorkflowSimulator(workflowDef, SimulatorConfig{})
	err := simulator.Run(func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		if stepID == "call_llm" {
			return map[string]interface{}{"tool_calls": []interface{}{}}
		}
		return map[string]interface{}{}
	})
	if err == nil {
		t.Fatalf("expected simulator run to fail on malformed edge CEL")
	}
	if got := err.Error(); !strings.Contains(got, "find triggered steps in simulator") || !strings.Contains(got, "process workflow events") {
		t.Fatalf("expected simulator and processor error context chain, got: %v", err)
	}
}

func TestWorkflowSimulator_CompileFailureFallsBackDeterministicallyToRootIdentity(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name: "builtin://root-fallback",
		Nodes: []*reliantv1.Node{{
			Id:   "bad_ref",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref: celLiteralForSimulator("builtin://broken::inline"),
			}},
		}},
	}

	simulator := NewWorkflowSimulator(workflowDef, SimulatorConfig{})
	if simulator.compiledSemantics != nil {
		t.Fatalf("expected compile failure to leave compiled semantics nil")
	}

	firstIdentity := simulator.workflowIdentityForNodePath("bad_ref")
	secondIdentity := simulator.workflowIdentityForNodePath("bad_ref")
	if firstIdentity != "builtin://root-fallback" || secondIdentity != "builtin://root-fallback" {
		t.Fatalf("expected deterministic root identity fallback, got %q then %q", firstIdentity, secondIdentity)
	}
}

func TestWorkflowSimulator_Run_FailsOnMalformedInlineLoopCondition(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name:  "sim-inline-loop-condition-error",
		Entry: []string{"loop_node"},
		Nodes: []*reliantv1.Node{{
			Id:   "loop_node",
			Type: "loop",
			Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
				While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 1"},
				Inline: &reliantv1.Workflow{
					Entry: []string{"inner_run"},
					Nodes: []*reliantv1.Node{{
						Id:        "inner_run",
						Type:      "run",
						Condition: &reliantv1.DirectCelBool{Expr: "nodes.inner_run["},
					}},
				},
			}},
		}},
	}

	simulator := NewWorkflowSimulator(workflowDef, SimulatorConfig{})
	err := simulator.Run(func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"ok": true}
	})
	if err == nil {
		t.Fatalf("expected simulator run to fail on malformed inline loop condition")
	}
	if got := err.Error(); !strings.Contains(got, "node condition evaluation failed for loop_node.inner_run") {
		t.Fatalf("expected strict node condition failure for inline loop, got: %v", err)
	}
}

func TestWorkflowSimulator_Run_FailsOnMalformedLoopOutputExpression(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name:  "sim-loop-output-error",
		Entry: []string{"loop_node"},
		Nodes: []*reliantv1.Node{{
			Id:   "loop_node",
			Type: "loop",
			Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
				While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 1"},
				Inline: &reliantv1.Workflow{
					Entry: []string{"inner_run"},
					Nodes: []*reliantv1.Node{{
						Id:   "inner_run",
						Type: "run",
					}},
					Outputs: map[string]string{
						"bad": "{{nodes.inner_run[}}",
					},
				},
			}},
		}},
	}

	simulator := NewWorkflowSimulator(workflowDef, SimulatorConfig{})
	err := simulator.Run(func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"result": "ok"}
	})
	if err == nil {
		t.Fatalf("expected simulator run to fail on malformed loop output expression")
	}
	if got := err.Error(); !strings.Contains(got, "evaluate workflow outputs for loop loop_node iteration 0") {
		t.Fatalf("expected strict loop output evaluation failure, got: %v", err)
	}
}

func TestWorkflowSimulator_Run_FailsOnMalformedRefLoopWhileExpression(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name:  "sim-ref-loop-while-error",
		Entry: []string{"loop_node"},
		Nodes: []*reliantv1.Node{{
			Id:   "loop_node",
			Type: "loop",
			Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
				Ref:   celLiteralForSimulator("builtin://external-loop"),
				While: &reliantv1.DirectCelBool{Expr: "iter.iteration <"},
			}},
		}},
	}

	simulator := NewWorkflowSimulator(workflowDef, SimulatorConfig{})
	err := simulator.Run(func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"done": false}
	})
	if err == nil {
		t.Fatalf("expected simulator run to fail on malformed referenced loop while CEL")
	}
	if got := err.Error(); !strings.Contains(got, "evaluate while condition for loop loop_node") {
		t.Fatalf("expected strict while evaluation failure for referenced loop, got: %v", err)
	}
}

func celLiteralForSimulator(value string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: value}}
}

func stringInputWithDefaultForSimulator(defaultValue string) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{
			Default: &defaultValue,
		}},
	}
}

func numberInputWithDefaultForSimulator(defaultValue float64) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "number",
		Config: &reliantv1.Input_NumberInput{NumberInput: &reliantv1.NumberInputConfig{
			Default: &defaultValue,
		}},
	}
}

func groupInputForSimulator(inputs map[string]*reliantv1.Input) *reliantv1.Input {
	return &reliantv1.Input{
		Type:   "group",
		Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{Inputs: inputs}},
	}
}
