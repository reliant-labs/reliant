package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestInlineWorkflowExecutor_BuildSubWorkflowInputs_UsesCoreInputPolicy(t *testing.T) {
	t.Parallel()
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

	// A preset that cannot be loaded must fail the call, not silently fall back
	// to the workflow's defaults. Falling back ran the agent under a different
	// model, tool set and prompt than the caller asked for while reporting
	// success.
	//
	// Loading a preset runs the LoadPresetParams activity, so this runs where
	// production does: inside a workflow, with the loader failing the way the
	// real one fails for a preset that does not exist. The project path is
	// empty on purpose — that alone no longer refuses the load
	// (presetsNeedNoPathChangeID), so the failure here is the preset's own.
	t.Run("ref policy preset merge failure is fatal", func(t *testing.T) {
		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			// Non-retryable so the activity's retry ladder does not wait out
			// backoff timers; whether the error retries is not what this pins.
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("preset not found: %v", input["preset_name"]), "PresetNotFound", nil)
		}, activity.RegisterOptions{Name: "LoadPresetParams"})

		var err error
		env.ExecuteWorkflow(func(ctx workflow.Context) error {
			executor := &InlineWorkflowExecutor{
				ctx:               ctx,
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
			_, _, err = executor.buildSubWorkflowInputsWithOwnership()
			return nil
		})

		if !env.IsWorkflowCompleted() {
			t.Fatal("the workflow running the preset load did not complete")
		}
		if err == nil {
			t.Fatal("expected an error when the preset cannot be loaded, got nil")
		}
		if !strings.Contains(err.Error(), "load presets") || !strings.Contains(err.Error(), "preset not found: unknown") {
			t.Fatalf("expected a preset-load error naming the missing preset, got %v", err)
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
	t.Parallel()
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
