// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateCrossWorkflow validates cross-workflow contracts.
// This includes validating child workflow input contracts plus spawn workflow loadability.
func validateCrossWorkflow(wf *reliantv1.Workflow, opts *ValidationOptions, result *Result) {
	canonicalWorkflowRef := wf.GetName()
	if opts != nil && strings.TrimSpace(opts.CanonicalWorkflowRef) != "" {
		canonicalWorkflowRef = strings.TrimSpace(opts.CanonicalWorkflowRef)
	}

	compileOptions := core.CompileOptions{
		CanonicalWorkflowRef: canonicalWorkflowRef,
	}
	if opts != nil && opts.WorkflowLoader != nil {
		compileOptions.WorkflowLoader = func(workflowRef string) (*reliantv1.Workflow, error) {
			return opts.WorkflowLoader(workflowRef)
		}
	}

	program, err := core.Compile(wf, compileOptions)
	if err != nil {
		result.AddError(CategoryCrossWorkflow, []string{wf.GetName()}, "",
			fmt.Sprintf("failed to compile core semantic contracts: %v", err))
		return
	}

	visited := make(map[string]bool)
	validateWorkflowTree(wf, wf, opts, program.Semantics, "", []string{}, program.Semantics.CanonicalWorkflowRef, visited, result)
}

// validateWorkflowTree recursively validates a workflow and its children using core semantic contracts.
func validateWorkflowTree(
	workflow *reliantv1.Workflow,
	parentWorkflow *reliantv1.Workflow,
	opts *ValidationOptions,
	semantics *core.CompiledSemantics,
	nodePathPrefix string,
	path []string,
	workflowIdentity string,
	visited map[string]bool,
	result *Result,
) {
	workflowName := workflow.GetName()
	if visited[workflowName] {
		return // Prevent infinite recursion
	}
	visited[workflowName] = true

	currentPath := append(path, workflowName)

	for _, node := range workflow.GetNodes() {
		if opts != nil && opts.WorkflowLoader != nil {
			validateSpawnRefsLoadable(node, opts.WorkflowLoader, currentPath, workflowIdentity, result)
		}

		nodeContractPath := composeNodeContractPath(nodePathPrefix, node.GetId())
		contract, hasSubWorkflowContract := semantics.SubWorkflows[nodeContractPath]
		if !hasSubWorkflowContract {
			continue
		}

		nodePath := append(currentPath, "nodes", node.GetId())
		switch contract.InputPolicy {
		case core.InputPolicyInlineInheritParentInputs:
			inlineWorkflow := resolveInlineWorkflow(node)
			if inlineWorkflow == nil {
				result.AddError(CategoryCrossWorkflow, nodePath, "inline",
					"core semantic contract expects inline workflow, but no inline definition was found")
				continue
			}
			validateWorkflowTree(
				inlineWorkflow,
				parentWorkflow,
				opts,
				semantics,
				nodeContractPath,
				path,
				contract.WorkflowIdentity,
				visited,
				result,
			)
		case core.InputPolicyRefPresetsArgsDefaults:
			if opts == nil || opts.WorkflowLoader == nil || contract.WorkflowRef == "" || containsTemplate(contract.WorkflowRef) {
				continue
			}

			childWorkflow, err := opts.WorkflowLoader(contract.WorkflowRef)
			if err != nil {
				result.AddError(CategoryCrossWorkflow, nodePath, "ref",
					fmt.Sprintf("failed to load workflow '%s': %v", contract.WorkflowRef, err))
				continue
			}

			validateProtoInputsAgainstSchema(
				nodePath,
				contract.Args,
				contract.Presets,
				childWorkflow.GetInputs(),
				opts.PresetLoader,
				parentWorkflow,
				result,
			)

			validateWorkflowTree(
				childWorkflow,
				childWorkflow,
				opts,
				semantics,
				nodeContractPath,
				path,
				contract.WorkflowIdentity,
				visited,
				result,
			)
		}
	}
}

func composeNodeContractPath(nodePathPrefix, nodeID string) string {
	if nodePathPrefix == "" {
		return nodeID
	}
	return nodePathPrefix + "/" + nodeID
}

func resolveInlineWorkflow(node *reliantv1.Node) *reliantv1.Workflow {
	if workflowArgs := node.GetWorkflow(); workflowArgs != nil {
		return workflowArgs.GetInline()
	}
	if loopArgs := node.GetLoop(); loopArgs != nil {
		return loopArgs.GetInline()
	}
	return nil
}

// DefaultPresetGroup is the key used for presets that apply to top-level (ungrouped) inputs.
const DefaultPresetGroup = "default"

// validateProtoInputsAgainstSchema validates provided args against a proto workflow's input schema.
// Args are map[string]*structpb.Value (from proto), which we convert to plain Go values for validation.
func validateProtoInputsAgainstSchema(
	path []string,
	providedArgs map[string]interface{},
	presets map[string]string,
	schema map[string]*reliantv1.Input,
	presetLoader PresetLoader,
	parentWf *reliantv1.Workflow,
	result *Result,
) {
	if len(schema) == 0 {
		return // No schema to validate against
	}

	// Build type context from parent workflow for CEL type validation
	var typeCtx *typeContext
	if parentWf != nil {
		typeCtx = buildTypeContext(parentWf)
	}

	// Build merged inputs: preset params (base) + provided args (override)
	merged := make(map[string]interface{})

	// Step 1: Load and merge preset params if preset loader is available
	if presetLoader != nil && len(presets) > 0 {
		for groupName, presetName := range presets {
			if presetName == "" {
				continue
			}

			// Skip CEL template preset names - can't validate statically
			if containsTemplate(presetName) {
				continue
			}

			params, err := presetLoader(presetName)
			if err != nil {
				result.AddError(CategoryCrossWorkflow, path, "presets",
					fmt.Sprintf("failed to load preset '%s': %v", presetName, err))
				continue
			}

			if groupName == DefaultPresetGroup {
				for paramName, paramValue := range params {
					merged[paramName] = paramValue
				}
			} else {
				groupMap := make(map[string]interface{})
				if existing, ok := merged[groupName].(map[string]interface{}); ok {
					groupMap = existing
				}
				for paramName, paramValue := range params {
					groupMap[paramName] = paramValue
				}
				merged[groupName] = groupMap
			}
		}
	}

	// Step 2: Apply provided args on top (args override presets)
	for key, value := range providedArgs {
		// Validate arg key is a valid identifier
		if err := validateIdentifier(key, "arg key"); err != nil {
			result.AddError(CategoryCrossWorkflow, append(path, "args"), key, err.message)
			continue
		}

		if mapVal, isMap := value.(map[string]interface{}); isMap {
			groupMap := make(map[string]interface{})
			if existing, ok := merged[key].(map[string]interface{}); ok {
				for k, v := range existing {
					groupMap[k] = v
				}
			}
			for k, v := range mapVal {
				groupMap[k] = v
			}
			merged[key] = groupMap
		} else {
			merged[key] = value
		}
	}

	// Check for required inputs and validate types
	var missingRequired []string
	var unknownInputs []string

	for name, inputDef := range schema {
		if inputDef == nil {
			continue
		}

		if model.IsGroupInput(inputDef) {
			nested := model.GetGroupInputs(inputDef)
			groupVal, hasGroup := merged[name]
			if hasGroup {
				if mapVal, ok := groupVal.(map[string]interface{}); ok {
					for nestedName, nestedInput := range nested {
						if nestedInput != nil && model.IsInputRequired(nestedInput) {
							if _, provided := mapVal[nestedName]; !provided {
								missingRequired = append(missingRequired, name+"."+nestedName)
							}
						}
					}
				}
			} else {
				for nestedName, nestedInput := range nested {
					if nestedInput != nil && model.IsInputRequired(nestedInput) {
						missingRequired = append(missingRequired, name+"."+nestedName)
					}
				}
			}
			continue
		}

		// Non-group: check if required and not provided
		if model.IsInputRequired(inputDef) {
			if _, exists := merged[name]; !exists {
				missingRequired = append(missingRequired, name)
			}
		}
	}

	if len(missingRequired) > 0 {
		sort.Strings(missingRequired)
		result.AddError(CategoryCrossWorkflow, path, "args",
			fmt.Sprintf("missing required input(s): %s", strings.Join(missingRequired, ", ")))
	}

	// Walk merged args to check for unknowns and validate types
	for key, value := range merged {
		inputDef, exists := schema[key]
		if !exists {
			// Skip CEL template expressions - evaluated at runtime
			if strVal, ok := value.(string); ok && containsTemplate(strVal) {
				continue
			}
			unknownInputs = append(unknownInputs, key)
			continue
		}

		// If the input is a group and the value is a map, validate nested keys
		if model.IsGroupInput(inputDef) {
			if mapVal, isMap := value.(map[string]interface{}); isMap {
				nested := model.GetGroupInputs(inputDef)
				for nestedKey, nestedVal := range mapVal {
					nestedInput, nestedExists := nested[nestedKey]
					if !nestedExists {
						unknownInputs = append(unknownInputs, key+"."+nestedKey)
						continue
					}
					// Handle CEL template expressions with type validation
					if strVal, ok := nestedVal.(string); ok && containsTemplate(strVal) {
						expr := extractCELExpression(strVal)
						if expr != "" {
							argPath := append(path, "args", key, nestedKey)
							validateProtoCELExpressionType(expr, nestedInput, typeCtx, argPath, result)
						}
						continue
					}
					// Type-check nested value
					if err := validateProtoInputType(key+"."+nestedKey, nestedVal, nestedInput); err != nil {
						result.AddError(CategoryCrossWorkflow, append(path, "args", key, nestedKey), "", err.Error())
					}
				}
			}
			continue
		}

		// Non-group: validate with CEL type checking for templates
		if strVal, ok := value.(string); ok && containsTemplate(strVal) {
			expr := extractCELExpression(strVal)
			if expr != "" {
				argPath := append(path, "args", key)
				validateProtoCELExpressionType(expr, inputDef, typeCtx, argPath, result)
			}
			continue
		}
		if err := validateProtoInputType(key, value, inputDef); err != nil {
			result.AddError(CategoryCrossWorkflow, append(path, "args", key), "", err.Error())
		}
	}

	if len(unknownInputs) > 0 {
		sort.Strings(unknownInputs)

		var validInputs []string
		for name, inputDef := range schema {
			if model.IsGroupInput(inputDef) {
				for nestedName := range model.GetGroupInputs(inputDef) {
					validInputs = append(validInputs, name+"."+nestedName)
				}
			} else {
				validInputs = append(validInputs, name)
			}
		}
		sort.Strings(validInputs)

		suggestion := ""
		if len(unknownInputs) == 1 {
			suggestion = suggestSimilar(unknownInputs[0], validInputs)
		}

		if suggestion != "" {
			result.AddErrorWithSuggestion(CategoryCrossWorkflow, path, "args",
				fmt.Sprintf("unknown input(s): %s", strings.Join(unknownInputs, ", ")),
				suggestion)
		} else {
			result.AddError(CategoryCrossWorkflow, path, "args",
				fmt.Sprintf("unknown input(s): %s", strings.Join(unknownInputs, ", ")))
		}
	}
}

// =============================================================================
// CEL TYPE VALIDATION FOR SUB-WORKFLOW ARGS
// =============================================================================

// protoInputToCELType maps a proto input type to its expected CEL type.
func protoInputToCELType(input *reliantv1.Input) *cel.Type {
	switch input.GetType() {
	case "string", "message", "enum":
		return cel.StringType
	case "model":
		return cel.DynType
	case "number":
		return cel.DoubleType
	case "integer":
		return cel.IntType
	case "boolean":
		return cel.BoolType
	case "array", "attachments", "tools":
		return cel.ListType(cel.DynType)
	case "object":
		return cel.MapType(cel.StringType, cel.DynType)
	default:
		return nil
	}
}

// validateProtoCELExpressionType validates that a CEL expression returns a type compatible with the expected proto input.
func validateProtoCELExpressionType(expr string, expectedInput *reliantv1.Input, typeCtx *typeContext, argPath []string, result *Result) {
	expectedType := protoInputToCELType(expectedInput)
	if expectedType == nil {
		return
	}

	var wtCtx *WorkflowTypeContext
	if typeCtx != nil {
		wtCtx = typeContextToWorkflowTypeContext(typeCtx)
	}

	namespaces := []wfcel.CELNamespace{
		wfcel.CELInputs,
		wfcel.CELWorkflow,
		wfcel.CELNodes,
		wfcel.CELIter,
	}

	env, err := newValidationCELEnv(namespaces, wtCtx)
	if err != nil {
		return
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return
	}

	outputType := ast.OutputType()
	if outputType == nil {
		return
	}

	if outputType.String() == "dyn" {
		return
	}

	if !isProtoTypeCompatible(outputType, expectedType, expectedInput) {
		inputTypeName := protoInputTypeName(expectedInput)
		actualTypeName := outputType.String()

		result.AddError(CategoryCrossWorkflow, argPath, "",
			fmt.Sprintf("type mismatch: expression returns %s but input expects %s", actualTypeName, inputTypeName))
	}
}

// isProtoTypeCompatible checks if actualType is compatible with expectedType.
func isProtoTypeCompatible(actualType, expectedType *cel.Type, input *reliantv1.Input) bool {
	if actualType.String() == expectedType.String() {
		return true
	}

	// NumberInput accepts both int and double
	if input.GetType() == "number" {
		if actualType == cel.IntType || actualType == cel.DoubleType {
			return true
		}
	}

	if expectedType.IsAssignableType(actualType) {
		return true
	}

	return false
}

// typeContextToWorkflowTypeContext converts a validation typeContext to a WorkflowTypeContext for CEL.
func typeContextToWorkflowTypeContext(tc *typeContext) *WorkflowTypeContext {
	if tc == nil {
		return nil
	}

	celCtx := &WorkflowTypeContext{
		InputFields:  make(map[string]*FieldInfo),
		InputGroups:  make(map[string]map[string]*FieldInfo),
		NodeOutputs:  make(map[string]map[string]*FieldInfo),
		OutputFields: make(map[string]*FieldInfo),
		NodeTypes:    make(map[string]string),
		Registry:     sharedRegistry,
	}

	// Convert inputs
	for inputName, inputType := range tc.inputTypes {
		celCtx.InputFields[inputName] = &FieldInfo{
			Name: inputName,
			Kind: celTypeNameToReflectKind(inputType),
		}
	}

	// Convert input groups
	for groupName, fieldNames := range tc.inputGroups {
		groupFields := make(map[string]*FieldInfo)
		for _, fieldName := range fieldNames {
			groupFields[fieldName] = &FieldInfo{
				Name: fieldName,
			}
		}
		celCtx.InputGroups[groupName] = groupFields
	}

	// Convert nodes
	for nodeID, nodeType := range tc.nodes {
		celCtx.NodeTypes[nodeID] = string(nodeType)
	}

	return celCtx
}

// celTypeNameToReflectKind converts a CEL type name string to reflect.Kind.
func celTypeNameToReflectKind(celType string) reflect.Kind {
	switch celType {
	case "string":
		return reflect.String
	case "int":
		return reflect.Int64
	case "float", "double":
		return reflect.Float64
	case "bool":
		return reflect.Bool
	case "array", "list":
		return reflect.Slice
	case "object", "map":
		return reflect.Map
	default:
		return reflect.Invalid
	}
}

// extractCELExpression extracts a CEL expression from a template string.
// For simple templates like "{{inputs.count}}", it returns the expression "inputs.count".
// For complex templates with multiple expressions or mixed content, returns empty string.
func extractCELExpression(template string) string {
	template = strings.TrimSpace(template)

	if !strings.HasPrefix(template, "{{") || !strings.HasSuffix(template, "}}") {
		return ""
	}

	expr := template[2 : len(template)-2]
	expr = strings.TrimSpace(expr)

	if strings.Contains(expr, "{{") || strings.Contains(expr, "}}") {
		return ""
	}

	return expr
}

func normalizeCELExpression(expr string) string {
	return strings.Join(strings.Fields(expr), "")
}

func hasSpawnWorkflowNameExpression(expr string) bool {
	normalized := normalizeCELExpression(expr)
	return strings.Contains(normalized, "spawn(workflow.name")
}

func isSpawnWorkflowNameIdentityExpression(expr string) bool {
	normalized := normalizeCELExpression(expr)
	return strings.Contains(normalized, "spawn(workflow.name,")
}

// validateSpawnRefsLoadable checks that literal spawn refs in call_llm tool_filter
// are loadable workflows. Recurses into inline workflows and validates core workflow identity contract.
func validateSpawnRefsLoadable(node *reliantv1.Node, loader WorkflowLoader, path []string, workflowIdentity string, result *Result) {
	nodePath := append(path, "nodes", node.GetId())

	// Check call_llm nodes
	if args := model.GetCallLLMArgs(node); args != nil {
		toolFilter := args.GetToolFilter()
		filters := model.CelStringListValue(toolFilter)
		if model.CelStringListIsExpr(toolFilter) {
			filters = []string{model.CelStringListExpr(toolFilter)}
		}

		for _, filter := range filters {
			if containsTemplate(filter) {
				expression := extractCELExpression(filter)
				if expression != "" && hasSpawnWorkflowNameExpression(expression) {
					if !isSpawnWorkflowNameIdentityExpression(expression) {
						result.AddErrorWithSuggestion(CategoryCrossWorkflow, append(nodePath, "args", "tool_filter"), "spawn",
							fmt.Sprintf("spawn(workflow.name, ...) expression must use workflow.name as the direct first argument, got %q", expression),
							"use spawn(workflow.name, <presets>) directly, not computed or transformed variants")
						continue
					}
					if workflowIdentity == "" {
						continue
					}
					if _, err := loader(workflowIdentity); err != nil {
						result.AddError(CategoryCrossWorkflow, append(nodePath, "args", "tool_filter"), "spawn",
							fmt.Sprintf("spawn(workflow.name, ...) resolved to '%s' which is not a loadable workflow: %v", workflowIdentity, err))
					}
				}
				continue
			}
			if !strings.HasPrefix(filter, "spawn:") {
				continue
			}

			// Extract workflow ref from spawn:WORKFLOW(presets)
			rest := strings.TrimPrefix(filter, "spawn:")
			parenIdx := strings.Index(rest, "(")
			workflowRef := rest
			if parenIdx >= 0 {
				workflowRef = rest[:parenIdx]
			}
			workflowRef = strings.TrimSpace(workflowRef)

			if workflowRef == "" {
				continue
			}

			if strings.Contains(workflowRef, "::") {
				result.AddErrorWithSuggestion(CategoryCrossWorkflow, append(nodePath, "args", "tool_filter"), "spawn",
					fmt.Sprintf("spawn ref '%s' contains '::' which indicates a synthetic inline workflow name — this is not a loadable workflow ref", workflowRef),
					"use the canonical workflow identity (workflow.name) or a loadable workflow ref")
				continue
			}

			// Try loading the workflow
			_, err := loader(workflowRef)
			if err != nil {
				result.AddError(CategoryCrossWorkflow, append(nodePath, "args", "tool_filter"), "spawn",
					fmt.Sprintf("spawn ref '%s' is not a loadable workflow: %v", workflowRef, err))
			}
		}
	}

	// Recurse into inline workflows with the same workflow identity per core contract.
	if wfArgs := model.GetSubWorkflowArgs(node); wfArgs != nil {
		if inline := wfArgs.GetInline(); inline != nil {
			for _, child := range inline.GetNodes() {
				validateSpawnRefsLoadable(child, loader, append(nodePath, "inline"), workflowIdentity, result)
			}
		}
	}
	if loopArgs := model.GetLoopArgs(node); loopArgs != nil {
		if inline := loopArgs.GetInline(); inline != nil {
			for _, child := range inline.GetNodes() {
				validateSpawnRefsLoadable(child, loader, append(nodePath, "inline"), workflowIdentity, result)
			}
		}
	}
}
