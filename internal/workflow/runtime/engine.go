// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	yaml "gopkg.in/yaml.v3"
)

// ============================================================================
// MODE HELPERS - derive mode-based flags from workflow inputs
// ============================================================================

// getModeFromInputs extracts the mode from workflow inputs, defaulting to "manual"
func getModeFromInputs(inputs map[string]interface{}) string {
	if inputs == nil {
		return "manual"
	}
	if mode, ok := inputs["mode"].(string); ok {
		return mode
	}
	return "manual"
}

// ============================================================================
// CORE TYPES
// ============================================================================

// DefaultPresetGroup is the reserved group name for ungrouped/top-level inputs.
const DefaultPresetGroup = "default"

// isActivityType returns true if the node type represents a known Temporal activity.
// Returns true only for known node types that are not structural.
// Unknown types (including the string "null") return false and fall through
// to the UnknownStepType error path in StepExecutor.Start.
func isActivityType(nodeType string) bool {
	return nodeType != "" && model.IsActivityNode(nodeType)
}

// nodeTypeToActivityName converts a snake_case node type to a PascalCase Temporal activity name.
// Examples: "call_llm" -> "CallLLM", "save_message" -> "SaveMessage"
func nodeTypeToActivityName(nodeType string) string {
	if nodeType == "" {
		return ""
	}
	return "" + snakeToPascal(nodeType)
}

// knownAcronyms maps common acronyms to their uppercase form.
// Used by snakeToPascal to properly capitalize acronyms like "llm" -> "LLM".
var knownAcronyms = map[string]string{
	"llm": "LLM",
	"api": "API",
	"url": "URL",
	"id":  "ID",
	"mcp": "MCP",
}

// snakeToPascal converts snake_case to PascalCase.
// Handles common acronyms like "llm" -> "LLM".
// Examples: "call_llm" -> "CallLLM", "save_message" -> "SaveMessage"
func snakeToPascal(s string) string {
	if s == "" {
		return ""
	}

	parts := strings.Split(s, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		if acronym, ok := knownAcronyms[strings.ToLower(part)]; ok {
			parts[i] = acronym
		} else {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}

// ============================================================================
// CEL COMPATIBILITY WRAPPERS
// ============================================================================

// CelCustomFunctions returns all custom CEL functions used in workflow evaluation.
func CelCustomFunctions() []cel.EnvOption {
	return wfcel.CustomFunctions()
}

// CelParseJsonFunction returns the parseJson CEL function.
func CelParseJsonFunction() cel.EnvOption {
	return wfcel.CelParseJsonFunction()
}

// CelToJsonFunction returns the toJson CEL function.
func CelToJsonFunction() cel.EnvOption {
	return wfcel.CelToJsonFunction()
}

// CelNowFunction returns the now CEL function.
func CelNowFunction() cel.EnvOption {
	return wfcel.CelNowFunction()
}

// CelCoalesceFunction returns the coalesce CEL function.
func CelCoalesceFunction() cel.EnvOption {
	return wfcel.CelCoalesceFunction()
}

// LoopScope carries the loop namespaces a node condition is evaluated against when
// the node runs inside a loop iteration: `iter` (which iteration this is) and
// `outputs` (the PREVIOUS iteration's declared loop outputs). Nil outside a loop.
//
// Without it a node's `condition` could see strictly less than the very same node's
// config templates, which EvaluateNodeConfig already resolves against iter/outputs.
// That asymmetry failed SILENTLY rather than loudly: EdgeEvalContext.Namespaces()
// declares `iter` and `outputs` unconditionally, so `outputs.foo` in a condition
// compiled fine and then evaluated against an empty map — every has() guard false,
// every branch taken as if the first iteration. A condition is the one place a
// workflow can say "skip this node THIS time round", so it is exactly the place
// that needs to know which time round it is.
type LoopScope struct {
	Iter    *model.IterContext
	Outputs map[string]interface{}
}

func (s *LoopScope) iter() *model.IterContext {
	if s == nil {
		return nil
	}
	return s.Iter
}

func (s *LoopScope) outputs() map[string]interface{} {
	if s == nil {
		return nil
	}
	return s.Outputs
}

// evaluateNodeCondition evaluates a node's condition field to determine if it should execute.
// Returns (shouldExecute bool, error).
func evaluateNodeCondition(
	node *reliantv1.Node,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
	workflowContext map[string]interface{},
	scope *LoopScope,
) (bool, error) {
	conditionExpr := model.ConditionExpr(node)
	if conditionExpr == "" {
		return true, nil
	}
	if conditionExpr == "true" {
		return true, nil
	}
	if conditionExpr == "false" {
		return false, nil
	}

	ctx := &wfcel.EdgeEvalContext{
		Nodes:    nodeOutputs,
		Inputs:   workflowInputs,
		Workflow: workflowContextToTyped(workflowContext),
		Iter:     scope.iter(),
		Outputs:  scope.outputs(),
	}
	return wfcel.EvaluateBool(conditionExpr, ctx)
}

// skipNodeIfConditionFalse evaluates a node's condition and, if false, records
// the skip (via SkippedStep activity) and returns a completion event so downstream
// edges can route. Returns (skipped=true, event, nil) when the node was skipped,
// or (skipped=false, nil, nil) when the node should execute normally.
//
// This is the single implementation of condition-based skip logic used by
// workflow.go, inline_workflow_executor.go, and loop_executor.go.
func skipNodeIfConditionFalse(
	ctx workflow.Context,
	node *reliantv1.Node,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
	workflowID string,
	chatID string,
	workflowName string,
	logger log.Logger,
	scope *LoopScope,
	nodePathPrefix string,
) (skipped bool, skipEvent *core.WorkflowEvent, err error) {
	condExpr := model.ConditionExpr(node)
	if condExpr == "" {
		return false, nil, nil
	}

	workflowContext := buildWorkflowContext(workflowID, workflowName, chatID, workflowInputs)
	shouldExecute, err := evaluateNodeCondition(node, nodeOutputs, workflowInputs, workflowContext, scope)
	if err != nil {
		return false, nil, fmt.Errorf("node condition evaluation failed for %s: %w", node.GetId(), err)
	}

	if shouldExecute {
		return false, nil, nil
	}

	logger.Info("Node skipped due to condition",
		"stepID", node.GetId(),
		"condition", condExpr,
	)

	// Execute SkippedStep activity for UI visibility
	skipCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	var skipResult map[string]interface{}
	_ = workflow.ExecuteActivity(skipCtx, model.ActivitySkippedStep, map[string]interface{}{
		"workflow_id": workflowID,
		"chat_id":     chatID,
		"step_id":     node.GetId(),
		// node_path is the node's fully-qualified graph position, which is what
		// identifies a skipped node inside a nested sub-workflow. step_id stays
		// the bare id it has always been.
		"node_path": joinNodePath(nodePathPrefix, node.GetId()),
		"condition": condExpr,
	}).Get(ctx, &skipResult)

	var skippedOutput map[string]interface{}
	if node.GetType() == model.NodeTypeRun {
		skippedOutput = model.SkippedRunOutputMap()
	} else {
		skippedOutput = model.SkippedOutputMap()
	}
	nodeOutputs[node.GetId()] = skippedOutput

	evt := &core.WorkflowEvent{
		ID:           fmt.Sprintf("skipped-%s-%d", node.GetId(), workflow.Now(ctx).UnixNano()),
		WorkflowID:   workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		StepID:       node.GetId(),
		Data:         skippedOutput,
	}
	return true, evt, nil
}

func normalizeEdgeFrom(from string) string {
	return from
}

// ============================================================================
// WORKFLOW PROCESSOR ADAPTER
// ============================================================================

// SimplifiedStateMachine is a runtime adapter around the core workflow processor.
type SimplifiedStateMachine struct {
	processor *core.WorkflowProcessor
	state     core.WorkflowProcessorState
}

// NewSimplifiedStateMachine creates a new state machine for a workflow.
func NewSimplifiedStateMachine(_ string, workflowDef *reliantv1.Workflow) *SimplifiedStateMachine {
	processor, err := core.NewWorkflowProcessor(workflowDef)
	if err != nil {
		panic(fmt.Sprintf("create workflow processor: %v", err))
	}
	return &SimplifiedStateMachine{processor: processor}
}

// FindTriggeredNodes finds all nodes that should be triggered by the given events.
func (sm *SimplifiedStateMachine) FindTriggeredNodes(events []*core.WorkflowEvent, nodeOutputs map[string]interface{}, workflowInputs map[string]interface{}) ([]*core.TriggeredNode, error) {
	nextState, triggeredNodes, err := sm.processor.Process(sm.state, core.ProcessInput{
		Events:         events,
		NodeOutputs:    nodeOutputs,
		WorkflowInputs: workflowInputs,
	})
	if err != nil {
		return nil, fmt.Errorf("process workflow events: %w", err)
	}
	sm.state = nextState
	return triggeredNodes, nil
}

// evaluateCELValue evaluates a CEL expression against the provided context.
// Uses NewEnvFromContext to auto-detect which namespaces to include.
func evaluateCELValue(expr string, context map[string]interface{}) (interface{}, error) {
	env, err := wfcel.NewEnvFromContext(context, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL program: %w", err)
	}

	evalCtx := wfcel.EnsureNamespaceDefaults(context, wfcel.AllNamespaces())
	out, _, err := prg.Eval(evalCtx)
	if err != nil {
		return nil, fmt.Errorf("CEL evaluation error: %w", err)
	}

	// convertCELToNative (wfcel.ConvertToNative) maps CEL null — the
	// structpb.NullValue enum — to Go nil at every nesting level.
	return convertCELToNative(out.Value()), nil
}

// EvaluateWorkflowOutputs evaluates workflow output expressions when workflow completes.
func EvaluateWorkflowOutputs(
	outputs map[string]string,
	nodeOutputs map[string]interface{},
	workflowContext map[string]interface{},
) (map[string]interface{}, error) {
	if len(outputs) == 0 {
		return nodeOutputs, nil
	}

	var inputs map[string]interface{}
	if i, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
		inputs = i
	}

	var iter *model.IterContext
	if iterMap, ok := workflowContext["iter"].(map[string]interface{}); ok {
		if iterVal, ok := iterMap["iteration"].(int); ok {
			iter = &model.IterContext{Iteration: iterVal}
		}
	}

	ctx := &wfcel.NodeResolutionContext{
		Inputs:   inputs,
		Nodes:    nodeOutputs,
		Iter:     iter,
		Workflow: workflowContextToTyped(workflowContext),
	}

	result := make(map[string]interface{})
	for name, expr := range outputs {
		val, err := wfcel.EvaluateTemplate(expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate output %q: %w", name, err)
		}
		result[name] = val
	}

	return result, nil
}

// ============================================================================
// WORKFLOW LOADING
// ============================================================================

// LoadWorkflow loads a workflow from JSON into a *reliantv1.Workflow.
//
// Preferred input is protobuf JSON (protojson), but we keep a legacy fallback
// for simplified workflow JSON used in tests and tooling.
func LoadWorkflow(data []byte) (*reliantv1.Workflow, error) {
	workflow := &reliantv1.Workflow{}
	protoErr := protojson.Unmarshal(data, workflow)
	if protoErr == nil {
		return workflow, nil
	}

	// Legacy fallback: parse generic JSON, convert to YAML, then decode via the
	// workflow YAML parser. This supports shorthand structures like node.args.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal workflow JSON: %w", protoErr)
	}

	yamlBytes, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("unmarshal workflow JSON: %w", protoErr)
	}

	legacyWorkflow, err := wfyaml.ParseWorkflow(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshal workflow JSON: %w", protoErr)
	}
	return legacyWorkflow, nil
}
