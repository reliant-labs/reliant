// Copyright (c) 2025 Reliant Labs
package cel

import (
	"context"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// Engine wraps CEL environment with custom functions
type Engine struct {
	env *cel.Env
}

// NewEngine creates a CEL engine with custom functions
func NewEngine() (*Engine, error) {
	env, err := cel.NewEnv(
		// Custom functions
		cel.Function("hasToolResult",
			cel.Overload("hasToolResult_string",
				[]*cel.Type{cel.StringType},
				cel.BoolType,
				cel.UnaryBinding(hasToolResultImpl),
			),
		),

		cel.Function("getMetadata",
			cel.Overload("getMetadata_string",
				[]*cel.Type{cel.StringType},
				cel.AnyType,
				cel.UnaryBinding(getMetadataImpl),
			),
		),

		cel.Function("hasError",
			cel.Overload("hasError",
				[]*cel.Type{},
				cel.BoolType,
				cel.FunctionBinding(hasErrorImpl),
			),
		),

		cel.Function("toolRequiresApproval",
			cel.Overload("toolRequiresApproval_string",
				[]*cel.Type{cel.StringType},
				cel.BoolType,
				cel.UnaryBinding(toolRequiresApprovalImpl),
			),
		),

		cel.Function("matchesPattern",
			cel.Overload("matchesPattern_string_string",
				[]*cel.Type{cel.StringType, cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(matchesPatternImpl),
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	return &Engine{env: env}, nil
}

// Compile compiles a CEL expression
// For dynamic variable support, use CompileWithVars instead
func (e *Engine) Compile(expression string) (*CompiledExpression, error) {
	ast, issues := e.env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compilation failed: %w", issues.Err())
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program construction failed: %w", err)
	}

	return &CompiledExpression{
		expression: expression,
		program:    prg,
	}, nil
}

// CompileWithVars compiles a CEL expression with known variable types
func (e *Engine) CompileWithVars(expression string, vars map[string]interface{}) (*CompiledExpression, error) {
	// Create environment options from variables
	var envOpts []cel.EnvOption
	for k := range vars {
		envOpts = append(envOpts, cel.Variable(k, cel.DynType))
	}

	// Create a new environment with variables declared
	env, err := e.env.Extend(envOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to extend environment: %w", err)
	}

	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compilation failed: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program construction failed: %w", err)
	}

	return &CompiledExpression{
		expression: expression,
		program:    prg,
	}, nil
}

type CompiledExpression struct {
	expression string
	program    cel.Program
}

// convertCELToNative recursively converts CEL types to native Go types.
// CEL's result.Value() returns a Go value but may contain nested ref.Val types
// (e.g., []ref.Val, map[ref.Val]ref.Val) which don't serialize properly to JSON.
// This function ensures all CEL internal types are converted to native types.
func convertCELToNative(v interface{}) interface{} {
	switch val := v.(type) {
	case ref.Val:
		// CEL value - extract native and recurse
		return convertCELToNative(val.Value())
	case []ref.Val:
		// Slice of CEL values
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = convertCELToNative(item)
		}
		return result
	case []interface{}:
		// Slice that might contain CEL values
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = convertCELToNative(item)
		}
		return result
	case map[string]interface{}:
		// Map that might contain CEL values
		result := make(map[string]interface{})
		for k, item := range val {
			result[k] = convertCELToNative(item)
		}
		return result
	case map[ref.Val]ref.Val:
		// CEL map type
		result := make(map[string]interface{})
		for k, item := range val {
			keyStr, _ := k.Value().(string)
			result[keyStr] = convertCELToNative(item)
		}
		return result
	default:
		// Already native type
		return v
	}
}

// Evaluate evaluates the compiled expression with given context
func (c *CompiledExpression) Evaluate(ctx context.Context, vars map[string]interface{}) (interface{}, error) {
	out, _, err := c.program.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("evaluation failed: %w", err)
	}

	// Recursively convert any CEL ref.Val types to native Go types
	// This ensures proper JSON serialization for nested structures
	return convertCELToNative(out.Value()), nil
}

// EvaluateBool evaluates and returns a boolean result
func (c *CompiledExpression) EvaluateBool(ctx context.Context, vars map[string]interface{}) (bool, error) {
	result, err := c.Evaluate(ctx, vars)
	if err != nil {
		return false, err
	}

	b, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("expression did not return boolean, got %T", result)
	}

	return b, nil
}

// CompileAndEvaluate is a convenience method that compiles and evaluates in one step
func (e *Engine) CompileAndEvaluate(ctx context.Context, expression string, vars map[string]interface{}) (interface{}, error) {
	compiled, err := e.CompileWithVars(expression, vars)
	if err != nil {
		return nil, err
	}

	return compiled.Evaluate(ctx, vars)
}

// CompileAndEvaluateBool compiles and evaluates, returning boolean
func (e *Engine) CompileAndEvaluateBool(ctx context.Context, expression string, vars map[string]interface{}) (bool, error) {
	compiled, err := e.CompileWithVars(expression, vars)
	if err != nil {
		return false, err
	}

	return compiled.EvaluateBool(ctx, vars)
}

// Custom function implementations

// hasToolResult checks if a tool call has a result
// Usage: hasToolResult("tool_call_123")
func hasToolResultImpl(arg ref.Val) ref.Val {
	toolCallID, ok := arg.Value().(string)
	if !ok {
		return types.NewErr("hasToolResult requires string argument")
	}

	// This will be called from workflow context
	// For now, return false (Agent 5 will integrate with actual state)
	_ = toolCallID
	return types.Bool(false)
}

// getMetadata retrieves metadata from workflow context
// Usage: getMetadata("chat.auto_approve")
func getMetadataImpl(arg ref.Val) ref.Val {
	key, ok := arg.Value().(string)
	if !ok {
		return types.NewErr("getMetadata requires string argument")
	}

	// This will be implemented by Agent 5
	_ = key
	return types.NullValue
}

// hasError checks if workflow has encountered an error
// Usage: hasError()
func hasErrorImpl(args ...ref.Val) ref.Val {
	// To be integrated by Agent 5
	return types.Bool(false)
}
