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

// LoopScope carries the loop namespaces a node condition or edge case condition
// is evaluated against: `iter` and, inside a loop body, `outputs` (the enclosing
// loop's PREVIOUS iteration outputs). Nil at the top level. The same scope feeds
// node conditions AND edge conditions (SimplifiedStateMachine.WithLoopScope), and
// it matches what the scope's node config resolves against, so a node's
// condition, its outgoing edges and its templates all see one scope.
//
// Build it with loopBodyScope or subWorkflowScope, never as a literal: `outputs`
// is declared exactly when Outputs is non-nil (wfcel.withLoopOutputs), and the
// constructors are what guarantee a loop body declares it on EVERY iteration,
// including iteration 0 and parallel iterations, which have no predecessor.
type LoopScope struct {
	Iter    *model.IterContext
	Outputs map[string]interface{}
}

// loopBodyScope is the scope of a loop body's expressions: this iteration's
// iter and the previous iteration's outputs (empty when there is none).
func loopBodyScope(iter *model.IterContext, prevOutputs map[string]interface{}) *LoopScope {
	return &LoopScope{Iter: iter, Outputs: loopBodyOutputs(prevOutputs)}
}

// subWorkflowScope is the scope of a sub-workflow body that runs inside a loop:
// its nodes see the enclosing iteration's iter (as their config does), but a
// sub-workflow body is not a loop body, so `outputs` is not declared. nil iter
// (not inside a loop) yields a nil scope.
func subWorkflowScope(iter *model.IterContext) *LoopScope {
	if iter == nil {
		return nil
	}
	return &LoopScope{Iter: iter}
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

// iterContextFromMap converts a published `iter` map (model.BuildIterContext /
// BuildParallelIterContext shape) into the typed IterContext, keeping item and
// key. nil in, nil out: no map means no enclosing loop.
func iterContextFromMap(iterMap map[string]interface{}) *model.IterContext {
	if iterMap == nil {
		return nil
	}
	iter := &model.IterContext{}
	if iterationVal, ok := iterCounter(iterMap["iteration"]); ok {
		iter.Iteration = iterationVal
		iter.Index = iterationVal
	}
	if indexVal, ok := iterCounter(iterMap["index"]); ok {
		iter.Index = indexVal
	}
	if itemVal, ok := iterMap["item"]; ok {
		iter.Item = itemVal
	}
	if keyVal, ok := iterMap["key"].(string); ok {
		iter.Key = keyVal
	}
	return iter
}

// iterCounter accepts the integer shapes an iteration counter takes after
// crossing a JSON or CEL boundary.
func iterCounter(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// loopBodyOutputs is the `outputs` namespace for an expression inside a loop
// body: the enclosing loop's previous-iteration outputs, or the empty map when
// there is no previous iteration. Every in-loop site goes through it so the
// namespace is never undeclared on iteration 0.
func loopBodyOutputs(prev map[string]interface{}) map[string]interface{} {
	if prev == nil {
		return map[string]interface{}{}
	}
	return prev
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
	// scope returns the loop namespaces for edge conditions at routing time;
	// nil outside a loop body. A func, not a value, because a sequential
	// loop's iteration counter and previous outputs move after construction.
	scope func() *LoopScope
}

// WithLoopScope makes edge case conditions routed by this machine see the loop
// body's `iter` and `outputs` — the same LoopScope its node conditions get.
func (sm *SimplifiedStateMachine) WithLoopScope(scope func() *LoopScope) *SimplifiedStateMachine {
	sm.scope = scope
	return sm
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
	var scope *LoopScope
	if sm.scope != nil {
		scope = sm.scope()
	}
	nextState, triggeredNodes, err := sm.processor.Process(sm.state, core.ProcessInput{
		Events:         events,
		NodeOutputs:    nodeOutputs,
		WorkflowInputs: workflowInputs,
		Iter:           scope.iter(),
		LoopOutputs:    scope.outputs(),
	})
	if err != nil {
		return nil, fmt.Errorf("process workflow events: %w", err)
	}
	sm.state = nextState
	return triggeredNodes, nil
}

// EvaluateWorkflowOutputs evaluates workflow output expressions when workflow completes.
func EvaluateWorkflowOutputs(
	outputs map[string]string,
	nodeOutputs map[string]interface{},
	workflowContext map[string]interface{},
) (map[string]interface{}, error) {
	return EvaluateDeclaredOutputs(outputs, nodeOutputs, workflowContext, nil, nil)
}

// EvaluateDeclaredOutputs evaluates declared output expressions, falling back to
// a node field's typed zero value when the expression is a bare reference to a
// field the node did not produce.
//
// Passing a non-nil wf enables that fallback; with a nil wf this behaves exactly
// as it always has and every failure propagates. The fallback applies uniformly
// to loop iteration outputs and root workflow outputs: the same expression
// resolving differently depending on which caller evaluated it would be a worse
// trap than the wider reach. See loop_output_schema.go for why the substitution
// happens after a failed evaluation rather than by seeding the nodes namespace.
func EvaluateDeclaredOutputs(
	outputs map[string]string,
	nodeOutputs map[string]interface{},
	workflowContext map[string]interface{},
	wf *reliantv1.Workflow,
	logger outputSubstitutionLogger,
) (map[string]interface{}, error) {
	if len(outputs) == 0 {
		return nodeOutputs, nil
	}

	var inputs map[string]interface{}
	if i, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
		inputs = i
	}

	// The enclosing loop's full iteration context (item/key included). Callers
	// may pass it explicitly; otherwise it is the `iter` the loop executors
	// publish into the scope's inputs, which is also where every node in the
	// scope reads it from (StepExecutor.iterContext).
	iterMap, _ := workflowContext["iter"].(map[string]interface{})
	if iterMap == nil {
		iterMap, _ = inputs["iter"].(map[string]interface{})
	}

	ctx := &wfcel.NodeResolutionContext{
		Inputs:   inputs,
		Nodes:    nodeOutputs,
		Iter:     iterContextFromMap(iterMap),
		Workflow: workflowContextToTyped(workflowContext),
	}

	result := make(map[string]interface{})
	for name, expr := range outputs {
		val, err := wfcel.EvaluateTemplate(expr, ctx)
		if err != nil {
			// The expression failed. If it was a bare reference to a field of a
			// declared node that simply has not run, resolve it to that field's
			// typed zero instead of failing the whole evaluation — which, in a
			// loop, is fatal to the iteration.
			zero, nodeID, substituted := substituteTypedZero(expr, nodeOutputs, wf)
			if substituted {
				logTypedZeroSubstitution(logger, name, expr, nodeID, zero)
				result[name] = zero
				continue
			}
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
