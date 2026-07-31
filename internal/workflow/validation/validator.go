// Copyright (c) 2025 Reliant Labs
// Package validation provides unified workflow validation for v2 workflows.
//
// The validation system has three phases:
//
//  1. StaticAnalysis - Run at workflow load/parse time.
//  2. ValidateInputs - Run before execution when input values are bound.
//  3. BeforeNode/AfterNode - Runtime validation during execution.
//
// All functions are stateless - pass what you need per call.
package validation

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// WorkflowLoader loads a workflow by name. Used for cross-workflow validation.
type WorkflowLoader func(workflowName string) (*reliantv1.Workflow, error)

// PresetLoader loads a preset by name and returns its params.
// Used for cross-workflow validation to simulate runtime preset merging.
type PresetLoader func(presetName string) (map[string]interface{}, error)

// ValidationOptions configures optional validation behaviors.
type ValidationOptions struct {
	// WorkflowLoader loads child workflows by reference.
	// Required for cross-workflow validation.
	WorkflowLoader WorkflowLoader

	// PresetLoader loads presets by name.
	// When provided, enables preset-aware validation that simulates runtime merging.
	PresetLoader PresetLoader

	// CanonicalWorkflowRef provides the loadable workflow identity for workflow.name
	// semantic validation (for example: "builtin://agent").
	// When empty, validation falls back to wf.name.
	CanonicalWorkflowRef string

	// SkillResolver resolves the skill names a workflow declares against a real
	// catalog. When provided, enables the skill-reference layer that fails a
	// workflow naming a skill nothing resolves.
	SkillResolver *SkillResolver
}

// =============================================================================
// STATIC ANALYSIS (Load-time)
// =============================================================================

// StaticAnalysis performs all load-time validation on a workflow.
// Pass options to enable cross-workflow validation (child workflow contracts)
// and preset-aware validation.
func StaticAnalysis(wf *reliantv1.Workflow, loader WorkflowLoader) *Result {
	opts := &ValidationOptions{
		WorkflowLoader: loader,
	}
	return StaticAnalysisWithOptions(wf, opts)
}

// StaticAnalysisWithOptions performs all load-time validation on a workflow.
// Pass options to enable cross-workflow validation and preset-aware validation.
func StaticAnalysisWithOptions(wf *reliantv1.Workflow, opts *ValidationOptions) *Result {
	result := NewResult()

	if wf == nil {
		result.AddError(CategoryStructure, nil, "", "workflow is nil")
		return result
	}

	if opts == nil {
		opts = &ValidationOptions{}
	}

	// Layer 1: Structural validation
	validateStructure(wf, result)
	if result.HasErrors() {
		return result
	}

	// Layer 2: CEL expression validation (compilation-based)
	ValidateCELWithCompilation(wf, result, opts.WorkflowLoader)

	// Layer 3: Parallel write detection
	validateParallelWrites(wf, result)

	// Layer 4: Cross-workflow validation via core semantic contracts.
	// Loader is optional: compile contracts are still validated even without ref loading.
	validateCrossWorkflow(wf, opts, result)

	// Layer 5: Skill references resolve against a real catalog.
	// Resolver is optional; without one the layer is a no-op.
	validateSkillReferences(wf, opts, result)

	return result
}

// =============================================================================
// INPUT VALIDATION (Pre-execution)
// =============================================================================

// ValidateInputs validates actual input values against the workflow's input schema.
// Call this right before workflow execution when input values are bound.
func ValidateInputs(wf *reliantv1.Workflow, inputs map[string]any) *Result {
	result := NewResult()

	if wf == nil {
		result.AddError(CategoryInput, nil, "", "workflow is nil")
		return result
	}

	validateBoundInputs(wf, inputs, result)
	return result
}

// =============================================================================
// RUNTIME VALIDATION (During execution)
// =============================================================================

// ExecutionState holds the current state of workflow execution.
// The caller (workflow engine) manages this state and passes it to validation.
type ExecutionState struct {
	Outputs map[string]any // nodeID -> output value
}

// NewExecutionState creates a new empty execution state.
func NewExecutionState() *ExecutionState {
	return &ExecutionState{
		Outputs: make(map[string]any),
	}
}

// IsCompleted returns true if a node has completed.
func (s *ExecutionState) IsCompleted(nodeID string) bool {
	_, ok := s.Outputs[nodeID]
	return ok
}

// RecordOutput records a node's output.
func (s *ExecutionState) RecordOutput(nodeID string, output any) {
	s.Outputs[nodeID] = output
}

// BeforeNode validates that a node is ready to execute.
func BeforeNode(wf *reliantv1.Workflow, state *ExecutionState, nodeID string) *Result {
	result := NewResult()

	if wf == nil || state == nil {
		result.AddError(CategoryRuntime, nil, "", "workflow or state is nil")
		return result
	}

	node := model.FindNode(wf, nodeID)
	if node == nil {
		result.AddError(CategoryRuntime, []string{wf.GetName(), "nodes", nodeID}, "",
			fmt.Sprintf("node '%s' not found", nodeID))
		return result
	}

	// Find dependencies and check they're complete
	deps := findNodeDependencies(node)
	var missing []string
	for _, depID := range deps {
		if !state.IsCompleted(depID) {
			missing = append(missing, depID)
		}
	}

	if len(missing) > 0 {
		result.AddError(CategoryRuntime, []string{wf.GetName(), "nodes", nodeID}, "",
			fmt.Sprintf("depends on uncompleted node(s): %s", strings.Join(missing, ", ")))
	}

	return result
}

// AfterNode validates a node's output after execution.
func AfterNode(wf *reliantv1.Workflow, nodeID string, output any) *Result {
	result := NewResult()

	if wf == nil {
		result.AddError(CategoryRuntime, nil, "", "workflow is nil")
		return result
	}

	node := model.FindNode(wf, nodeID)
	if node == nil {
		result.AddError(CategoryRuntime, []string{wf.GetName(), "nodes", nodeID}, "",
			fmt.Sprintf("node '%s' not found", nodeID))
		return result
	}

	// Proto nodes don't carry OutputType() — skip runtime output type checking.
	// The engine validates output types via activity handlers directly.
	_ = node
	return result
}

// ValidateRuntimeCEL validates a CEL expression against current execution state.
func ValidateRuntimeCEL(wf *reliantv1.Workflow, state *ExecutionState, expr string, location string) *Result {
	result := NewResult()

	if wf == nil || state == nil {
		result.AddError(CategoryRuntime, nil, "", "workflow or state is nil")
		return result
	}

	// Pattern captures full field path including nested access (e.g., message.content, tool_calls[0].name)
	nodeFieldPattern := regexp.MustCompile(`nodes\.([a-zA-Z_][a-zA-Z0-9_]*)\.([a-zA-Z_][a-zA-Z0-9_.\[\]]*)`)
	for _, match := range nodeFieldPattern.FindAllStringSubmatch(expr, -1) {
		nodeID := match[1]

		if !state.IsCompleted(nodeID) {
			result.AddError(CategoryRuntime, []string{location}, "",
				fmt.Sprintf("references uncompleted node '%s'", nodeID))
			continue
		}

		// Extract top-level field for validation
		fieldPath := match[2]
		topLevelField := extractTopLevelField(fieldPath)

		output := state.Outputs[nodeID]
		if output != nil && !hasField(output, topLevelField) {
			validFields := getFieldNames(output)
			suggestion := suggestSimilar(topLevelField, validFields)
			if suggestion != "" {
				result.AddErrorWithSuggestion(CategoryRuntime, []string{location}, "",
					fmt.Sprintf("node '%s' has no field '%s'", nodeID, topLevelField), suggestion)
			} else {
				result.AddError(CategoryRuntime, []string{location}, "",
					fmt.Sprintf("node '%s' has no field '%s'", nodeID, topLevelField))
			}
		}
	}

	return result
}

// =============================================================================
// HELPERS
// =============================================================================

func findNodeDependencies(node *reliantv1.Node) []string {
	var deps []string
	seen := make(map[string]bool)
	nodeRefPattern := regexp.MustCompile(`nodes\.(\w+)`)

	extract := func(s string) {
		for _, match := range nodeRefPattern.FindAllStringSubmatch(s, -1) {
			depID := match[1]
			if !seen[depID] {
				seen[depID] = true
				deps = append(deps, depID)
			}
		}
	}

	// Check condition
	if condExpr := model.ConditionExpr(node); condExpr != "" {
		extract(condExpr)
	}

	// Check save_message fields
	if sm := node.GetSaveMessage(); sm != nil {
		extract(model.CelStringRaw(sm.GetRole()))
		extract(model.CelStringRaw(sm.GetContent()))
		extract(model.CelStringRaw(sm.GetToolCalls()))
		extract(model.CelStringRaw(sm.GetToolResults()))
		extract(model.CelStringRaw(sm.GetAttachments()))
	}

	// Extract from all string fields in the node's args
	extractCELRefsFromNode(node, extract)
	return deps
}

// hasField checks if a struct or map has the given field name.
func hasField(output any, fieldName string) bool {
	if output == nil {
		return false
	}
	v := reflect.ValueOf(output)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if fmt.Sprintf("%v", key.Interface()) == fieldName {
				return true
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			// Check json tag
			if tag := field.Tag.Get("json"); tag != "" {
				name := strings.Split(tag, ",")[0]
				if name == fieldName {
					return true
				}
			}
			if strings.EqualFold(field.Name, fieldName) {
				return true
			}
		}
	}
	return false
}

// getFieldNames returns the field names of a struct or map.
func getFieldNames(output any) []string {
	if output == nil {
		return nil
	}
	v := reflect.ValueOf(output)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		var names []string
		for _, key := range v.MapKeys() {
			names = append(names, fmt.Sprintf("%v", key.Interface()))
		}
		return names
	case reflect.Struct:
		t := v.Type()
		var names []string
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			if tag := field.Tag.Get("json"); tag != "" {
				name := strings.Split(tag, ",")[0]
				if name != "" && name != "-" {
					names = append(names, name)
					continue
				}
			}
			names = append(names, strings.ToLower(field.Name))
		}
		return names
	}
	return nil
}

// extractCELRefsFromNode extracts node references from all CEL string fields
// in a proto node's args. This is a best-effort scan using the proto string fields.
func extractCELRefsFromNode(node *reliantv1.Node, extract func(string)) {
	if node == nil {
		return
	}
	// Scan all CelString/string fields in args by checking each node type
	switch {
	case node.GetCallLlm() != nil:
		args := node.GetCallLlm()
		extract(model.CelStringRaw(args.GetSystemPrompt()))
		extract(model.CelStringRaw(args.GetThinkingLevel()))
		for _, msg := range args.GetMessages() {
			extract(msg.GetContent())
			extract(msg.GetRole())
		}
	case node.GetExecuteTools() != nil:
		args := node.GetExecuteTools()
		extract(model.CelStringRaw(args.GetToolCalls()))
	case node.GetRun() != nil:
		args := node.GetRun()
		extract(model.CelStringRaw(args.GetCommand()))
		extract(model.CelStringRaw(args.GetWorkDir()))
	case node.GetWorkflow() != nil:
		args := node.GetWorkflow()
		extract(model.CelStringRaw(args.GetRef()))
		for _, v := range args.GetArgs() {
			if v != nil {
				extract(v.GetStringValue())
			}
		}
	case node.GetLoop() != nil:
		args := node.GetLoop()
		extract(model.CelStringRaw(args.GetRef()))
		extract(model.DirectCelExpr(args.GetWhile()))
		for _, v := range args.GetArgs() {
			if v != nil {
				extract(v.GetStringValue())
			}
		}
	}
}
