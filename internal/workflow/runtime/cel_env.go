// Copyright (c) 2025 Reliant Labs
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: the Temporal workflow runtime (DynamicWorkflow); the shape is dictated by the Temporal SDK
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

// WithOutputs sets the enclosing loop's previous-iteration outputs. Pass a
// non-nil map (loopBodyOutputs) exactly when resolving inside a loop body; nil
// leaves `outputs` undeclared, as it is everywhere outside a loop.
func (b *CELContextBuilder) WithOutputs(outputs map[string]interface{}) *CELContextBuilder {
	b.outputs = outputs
	return b
}

// resolutionContext is the builder's scope as the typed context every node
// config site evaluates against. The namespace set comes from
// NodeResolutionContext.Namespaces() — the builder adds no namespace of its own,
// so what validation declares for node config is exactly what resolves here.
// Dynamic namespaces are numerically normalized (JSON float64 → int64).
func (b *CELContextBuilder) resolutionContext() *wfcel.NodeResolutionContext {
	rc := &wfcel.NodeResolutionContext{
		Inputs: normalizeNumericTypes(b.inputs),
		Nodes:  normalizeNumericTypes(b.nodeOutputs),
		Iter:   b.iter,
		Workflow: workflowContextToTyped(map[string]interface{}{
			workflowContextKeyID:     b.workflowID,
			workflowContextKeyName:   b.workflowName,
			workflowContextKeyPath:   b.path,
			workflowContextKeyBranch: b.branch,
		}),
	}
	if b.outputs != nil {
		rc.Outputs = normalizeNumericTypes(b.outputs)
	}
	return rc
}

// Build returns the CEL activation for this builder's scope.
func (b *CELContextBuilder) Build() map[string]interface{} {
	return b.resolutionContext().Activation()
}

// env builds the CEL environment declaring exactly the scope's namespaces.
func (b *CELContextBuilder) env() (*cel.Env, map[string]interface{}, error) {
	rc := b.resolutionContext()
	env, err := wfcel.NewEnv(wfcel.CELEnvConfig{
		Namespaces:             rc.Namespaces(),
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	return env, rc.Activation(), nil
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
	env, evalCtx, err := b.env()
	if err != nil {
		return nil, err
	}

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

	// convertCELToNative (wfcel.ConvertToNative) maps CEL null — the
	// structpb.NullValue enum — to Go nil at every nesting level.
	return convertCELToNative(out.Value()), nil
}

// EvalBool evaluates a direct CEL expression (no {{ }}) as a boolean.
// Implements wfcel.CELEvaluator.
func (b *CELContextBuilder) EvalBool(expr string) (bool, error) {
	env, evalCtx, err := b.env()
	if err != nil {
		return false, err
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL program: %w", err)
	}

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
