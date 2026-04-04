// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ============================================================================
// CEL CONTEXT BUILDER
// ============================================================================

// CELContextBuilder provides a fluent interface for building CEL evaluation contexts.
// This ensures consistent context structure across all evaluation points.
//
// CELContextBuilder also implements wfcel.CELEvaluator, allowing it to be passed
// directly to wfcel.ResolveCELFields for template resolution.
type CELContextBuilder struct {
	workflowID   string
	workflowName string
	path         string
	branch       string
	inputs       map[string]interface{}
	nodeOutputs  map[string]interface{}
	execContext  *ExecutionContext
	iter         *model.IterContext
	output       map[string]interface{}
	outputs      map[string]interface{}
}

// NewCELContextBuilder creates a new context builder.
func NewCELContextBuilder() *CELContextBuilder {
	return &CELContextBuilder{
		inputs:      make(map[string]interface{}),
		nodeOutputs: make(map[string]interface{}),
	}
}

// WithWorkflow sets the workflow identity.
func (b *CELContextBuilder) WithWorkflow(id, name string) *CELContextBuilder {
	b.workflowID = id
	b.workflowName = name
	return b
}

// WithEnvironment sets the workspace environment.
func (b *CELContextBuilder) WithEnvironment(path, branch string) *CELContextBuilder {
	b.path = path
	b.branch = branch
	return b
}

// WithInputs sets the workflow inputs.
func (b *CELContextBuilder) WithInputs(inputs map[string]interface{}) *CELContextBuilder {
	if inputs != nil {
		b.inputs = inputs
	}
	return b
}

// WithNodeOutputs sets the completed step outputs.
func (b *CELContextBuilder) WithNodeOutputs(nodeOutputs map[string]interface{}) *CELContextBuilder {
	if nodeOutputs != nil {
		b.nodeOutputs = nodeOutputs
	}
	return b
}

// WithExecContext sets the execution context for CEL evaluation.
func (b *CELContextBuilder) WithExecContext(ctx *ExecutionContext) *CELContextBuilder {
	b.execContext = ctx
	return b
}

// WithIter sets the loop iteration context.
func (b *CELContextBuilder) WithIter(iter *model.IterContext) *CELContextBuilder {
	b.iter = iter
	return b
}

// WithOutput sets the current activity output (for save_message).
func (b *CELContextBuilder) WithOutput(output map[string]interface{}) *CELContextBuilder {
	b.output = output
	return b
}

// WithOutputs sets the sub-workflow outputs (for loop while).
func (b *CELContextBuilder) WithOutputs(outputs map[string]interface{}) *CELContextBuilder {
	b.outputs = outputs
	return b
}

// Build creates the CEL context map.
// Uses native Go structs for typed namespaces (workflow, iter) to enable
// compile-time field validation. Dynamic namespaces (inputs, nodes, output, outputs)
// remain as maps.
func (b *CELContextBuilder) Build() map[string]interface{} {
	context := make(map[string]interface{})

	// inputs.* namespace - PRIMARY way to access workflow inputs (dynamic)
	context["inputs"] = b.inputs

	// workflow.* namespace - typed struct for compile-time validation
	context["workflow"] = workflowContextToTyped(map[string]interface{}{
		workflowContextKeyID:     b.workflowID,
		workflowContextKeyName:   b.workflowName,
		workflowContextKeyPath:   b.path,
		workflowContextKeyBranch: b.branch,
	})

	// nodes.* namespace - completed node outputs (dynamic)
	context["nodes"] = b.nodeOutputs

	// iter.* namespace - typed struct for compile-time validation
	if b.iter != nil {
		context["iter"] = map[string]interface{}{
			"iteration": b.iter.Iteration,
			"index":     b.iter.Index,
			"item":      b.iter.Item,
			"key":       b.iter.Key,
		}
	} else {
		context["iter"] = map[string]interface{}{"iteration": 0, "index": 0}
	}

	// output.* namespace - current activity output (dynamic, save_message only)
	if b.output != nil {
		context["output"] = b.output
	}

	// outputs.* namespace - sub-workflow outputs (dynamic, loop while only)
	if b.outputs != nil {
		context["outputs"] = b.outputs
	}

	// Normalize numeric types only for dynamic namespaces
	if inputs, ok := context["inputs"].(map[string]interface{}); ok {
		context["inputs"] = normalizeNumericTypes(inputs)
	}
	if nodes, ok := context["nodes"].(map[string]interface{}); ok {
		context["nodes"] = normalizeNumericTypes(nodes)
	}
	if output, ok := context["output"].(map[string]interface{}); ok {
		context["output"] = normalizeNumericTypes(output)
	}
	if outputs, ok := context["outputs"].(map[string]interface{}); ok {
		context["outputs"] = normalizeNumericTypes(outputs)
	}

	return context
}

// ============================================================================
// CELEvaluator IMPLEMENTATION
// ============================================================================

// EvalString evaluates a {{expr}} template string against this builder's context.
// EvalString evaluates a string that may contain {{expr}} template expressions.
// - Pure {{expr}} strings return the expression's native type (e.g., []interface{}).
// - Mixed strings like "hello {{name}}" interpolate and return a string.
// - Strings without {{}} are returned as-is (literals).
// Implements wfcel.CELEvaluator.
func (b *CELContextBuilder) EvalString(expr string) (interface{}, error) {
	ctx := b.Build()
	env, err := wfcel.NewEnvFromContext(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	evalCtx := wfcel.EnsureNamespaceDefaults(ctx, wfcel.AllNamespaces())

	// Find all {{...}} template expressions
	matches := extractTemplateExpressions(expr)

	// No template expressions — return literal string as-is
	if len(matches) == 0 {
		return expr, nil
	}

	// Pure expression (entire string is {{expr}} with optional whitespace)
	if len(matches) == 1 && isPureExpressionWithWhitespace(expr, matches[0]) {
		result, err := b.evalSingleCEL(matches[0].expr, env, evalCtx)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// Mixed string — interpolate each {{expr}} into a string
	var result strings.Builder
	lastEnd := 0
	for _, match := range matches {
		result.WriteString(expr[lastEnd:match.start])
		value, err := b.evalSingleCEL(match.expr, env, evalCtx)
		if err != nil {
			return nil, err
		}
		if value != nil {
			result.WriteString(valueToInterpolatedString(value))
		} else {
			result.WriteString("NULL_VALUE")
		}
		lastEnd = match.end
	}
	result.WriteString(expr[lastEnd:])
	return result.String(), nil
}

// evalSingleCEL evaluates a single CEL expression and returns the native Go value.
func (b *CELContextBuilder) evalSingleCEL(expr string, env *cel.Env, evalCtx map[string]interface{}) (interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL program: %w", err)
	}

	out, _, err := prg.Eval(evalCtx)
	if err != nil {
		return nil, fmt.Errorf("CEL evaluation error: %w", err)
	}

	value := out.Value()

	// CEL returns structpb.NullValue for null/nil, which marshals to the number 0.
	// Convert it back to Go nil for proper handling.
	if value != nil && fmt.Sprintf("%T", value) == "structpb.NullValue" {
		return nil, nil
	}

	return convertCELToNative(value), nil
}

// EvalBool evaluates a direct CEL expression (no {{ }}) as a boolean.
// Implements wfcel.CELEvaluator.
func (b *CELContextBuilder) EvalBool(expr string) (bool, error) {
	ctx := b.Build()
	env, err := wfcel.NewEnvFromContext(ctx, true)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL program: %w", err)
	}

	evalCtx := wfcel.EnsureNamespaceDefaults(ctx, wfcel.AllNamespaces())
	out, _, err := prg.Eval(evalCtx)
	if err != nil {
		return false, fmt.Errorf("CEL evaluation error: %w", err)
	}

	boolVal, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression %q did not evaluate to bool, got %T", expr, out.Value())
	}
	return boolVal, nil
}

// Ensure CELContextBuilder implements wfcel.CELEvaluator at compile time.
var _ wfcel.CELEvaluator = (*CELContextBuilder)(nil)

// ============================================================================
// NUMERIC TYPE NORMALIZATION
// ============================================================================

// normalizeNumericTypes converts json.Number and float64 to int64 where appropriate.
// CEL expects int64 for integer values, but JSON unmarshaling produces float64.
func normalizeNumericTypes(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = normalizeValue(v)
	}
	return result
}

func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case float64:
		// Convert whole numbers to int64 for CEL compatibility
		if val == float64(int64(val)) {
			return int64(val)
		}
		return val
	case map[string]interface{}:
		return normalizeNumericTypes(val)
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = normalizeValue(item)
		}
		return result
	default:
		return v
	}
}
