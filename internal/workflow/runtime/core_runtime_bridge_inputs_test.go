package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestInlineWorkflowExecutor_BuildSubWorkflowInputs_UsesCoreInputPolicy(t *testing.T) {
	t.Run("inline policy shares parent map reference", func(t *testing.T) {
		parentInputs := map[string]interface{}{"model": "gpt-5", "mode": "auto"}
		executor := &InlineWorkflowExecutor{
			workflowInputs: parentInputs,
			invocationContract: &core.SubWorkflowContract{
				InputPolicy: core.InputPolicyInlineInheritParentInputs,
			},
		}

		result := executor.buildSubWorkflowInputs()
		if result["model"] != "gpt-5" {
			t.Fatalf("expected inherited model, got %#v", result)
		}
		if result["mode"] != "auto" {
			t.Fatalf("expected inherited mode, got %#v", result)
		}

		// Must be the SAME map reference (not a copy)
		result["model"] = "mutated"
		if parentInputs["model"] != "mutated" {
			t.Fatalf("expected inline-inherited result to share parent map reference, but mutation did not propagate")
		}
	})

	t.Run("ref policy uses args and defaults without parent inheritance", func(t *testing.T) {
		executor := &InlineWorkflowExecutor{
			subWorkflowInputs: map[string]interface{}{"task": "analyze"},
			subWorkflow: &reliantv1.Workflow{Inputs: map[string]*reliantv1.Input{
				"mode": {
					Type:   "string",
					Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{Default: stringPointer("manual")}},
				},
			}},
			invocationContract: &core.SubWorkflowContract{
				InputPolicy: core.InputPolicyRefPresetsArgsDefaults,
			},
		}

		result := executor.buildSubWorkflowInputs()
		if _, exists := result["model"]; exists {
			t.Fatalf("did not expect inherited parent model in ref policy: %#v", result)
		}
		if result["task"] != "analyze" {
			t.Fatalf("expected resolved arg task, got %#v", result)
		}
		if result["mode"] != "manual" {
			t.Fatalf("expected default mode, got %#v", result)
		}
	})

	t.Run("ref policy preset merge failures are non-fatal and still apply defaults", func(t *testing.T) {
		executor := &InlineWorkflowExecutor{
			workflowInputs:    map[string]interface{}{"parent_only": "secret"},
			subWorkflowInputs: map[string]interface{}{"task": "analyze"},
			logger:            &runtimeBridgeNoopLogger{},
			subWorkflow: &reliantv1.Workflow{Inputs: map[string]*reliantv1.Input{
				"mode": {
					Type:   "string",
					Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{Default: stringPointer("manual")}},
				},
			}},
			node: &reliantv1.Node{
				Id:   "wf_call",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Presets: map[string]string{DefaultPresetGroup: "unknown"}}},
			},
			projectPath: "",
			invocationContract: &core.SubWorkflowContract{
				InputPolicy: core.InputPolicyRefPresetsArgsDefaults,
			},
		}

		result := executor.buildSubWorkflowInputs()
		if _, exists := result["parent_only"]; exists {
			t.Fatalf("did not expect parent-only input inheritance in ref policy: %#v", result)
		}
		if result["task"] != "analyze" {
			t.Fatalf("expected resolved arg task, got %#v", result)
		}
		if result["mode"] != "manual" {
			t.Fatalf("expected defaults to survive preset merge failure, got %#v", result)
		}
	})
}

func stringPointer(value string) *string {
	return &value
}

type runtimeBridgeNoopLogger struct{}

func (l *runtimeBridgeNoopLogger) Debug(string, ...interface{}) {}
func (l *runtimeBridgeNoopLogger) Info(string, ...interface{})  {}
func (l *runtimeBridgeNoopLogger) Warn(string, ...interface{})  {}
func (l *runtimeBridgeNoopLogger) Error(string, ...interface{}) {}

func TestInlineLoopExecutor_BuildIterationInputs_UsesCoreInputPolicy(t *testing.T) {
	loopNode := &reliantv1.Node{
		Id:   "loop_node",
		Type: "loop",
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 1"},
			Args: map[string]*structpb.Value{
				"task": structpb.NewStringValue("loop-task"),
			},
		}},
	}

	t.Run("inline policy inherits parent iteration inputs", func(t *testing.T) {
		executor := &InlineLoopExecutor{
			loopID:         "loop_node",
			loopStep:       &core.TriggeredNode{Node: loopNode},
			iteration:      2,
			workflowInputs: map[string]interface{}{"model": "gpt-5", "mode": "auto"},
			invocationContract: &core.SubWorkflowContract{
				InputPolicy: core.InputPolicyInlineInheritParentInputs,
			},
		}

		iterInputs, err := executor.buildIterationInputs()
		if err != nil {
			t.Fatalf("buildIterationInputs returned error: %v", err)
		}
		if iterInputs["model"] != "gpt-5" {
			t.Fatalf("expected inherited model, got %#v", iterInputs)
		}
		iterCtx, ok := iterInputs["iter"].(map[string]interface{})
		if !ok || iterCtx["iteration"] != 2 {
			t.Fatalf("expected iter context for iteration 2, got %#v", iterInputs["iter"])
		}
	})

	t.Run("ref policy does not inherit parent-only inputs", func(t *testing.T) {
		executor := &InlineLoopExecutor{
			loopID:         "loop_node",
			loopStep:       &core.TriggeredNode{Node: loopNode},
			iteration:      0,
			workflowID:     "wf-1",
			workflowName:   "builtin://agent",
			workflowInputs: map[string]interface{}{"model": "gpt-5", "mode": "auto"},
			nodeOutputs:    map[string]interface{}{},
			subWorkflow:    &reliantv1.Workflow{},
			invocationContract: &core.SubWorkflowContract{
				InputPolicy: core.InputPolicyRefPresetsArgsDefaults,
			},
		}

		iterInputs, err := executor.buildIterationInputs()
		if err != nil {
			t.Fatalf("buildIterationInputs returned error: %v", err)
		}
		if _, exists := iterInputs["model"]; exists {
			t.Fatalf("did not expect inherited parent model in ref policy: %#v", iterInputs)
		}
		if iterInputs["task"] != "loop-task" {
			t.Fatalf("expected resolved arg task, got %#v", iterInputs)
		}
		if iterInputs["iter"] == nil || iterInputs["loop"] == nil {
			t.Fatalf("expected loop and iter context, got %#v", iterInputs)
		}
	})
}
