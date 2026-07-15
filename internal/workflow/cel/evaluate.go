package wfcel

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"
)

// =============================================================================
// UNIFIED CEL EVALUATION API
// =============================================================================

// EvaluateBool evaluates a CEL expression to a boolean using a typed context.
// Returns an error if the expression does not evaluate to a boolean.
func EvaluateBool(expr string, ctx CELEvalContext) (bool, error) {
	val, err := evaluateRaw(expr, ctx)
	if err != nil {
		return false, err
	}

	result, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return boolean, got %T", val)
	}
	return result, nil
}

// EvaluateValue evaluates a CEL expression to any value using a typed context.
// The returned value has all CEL internal types converted to native Go types.
func EvaluateValue(expr string, ctx CELEvalContext) (interface{}, error) {
	return evaluateRaw(expr, ctx)
}

// EvaluateTemplate evaluates a string that may contain {{...}} template expressions.
//   - Strings without {{...}} are returned as-is (literals).
//   - Pure {{expression}} strings return the expression's native type (not string).
//   - Mixed strings with embedded {{...}} are interpolated to a string.
//
// Handles YAML multi-line strings (| operator) by trimming leading/trailing whitespace
// before checking for pure expressions.
func EvaluateTemplate(expr string, ctx CELEvalContext) (interface{}, error) {
	if expr == "" {
		return "", nil
	}

	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", nil
	}

	// Find all template expressions using balanced brace parsing.
	matches := extractTemplates(trimmed)

	// No template expressions — return trimmed string as-is.
	if len(matches) == 0 {
		return trimmed, nil
	}

	// Pure expression: entire trimmed string is a single {{expr}}.
	// Return the expression's native type, not a string.
	if len(matches) == 1 && matches[0].start == 0 && matches[0].end == len(trimmed) {
		return evaluateRaw(matches[0].expr, ctx)
	}

	// Mixed string with embedded expressions — interpolate to string.
	var result strings.Builder
	lastEnd := 0

	for _, match := range matches {
		result.WriteString(trimmed[lastEnd:match.start])

		value, err := evaluateRaw(match.expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("template expression '{{%s}}': %w", match.expr, err)
		}

		if value != nil {
			result.WriteString(valueToString(value))
		}

		lastEnd = match.end
	}

	result.WriteString(trimmed[lastEnd:])
	return result.String(), nil
}

// =============================================================================
// INTERNAL EVALUATION
// =============================================================================

// evaluateRaw compiles and evaluates a CEL expression against a typed context.
// Returns the native Go value with all CEL internal types converted.
func evaluateRaw(expr string, ctx CELEvalContext) (interface{}, error) {
	// Build the CEL environment from the context's namespaces.
	config := CELEnvConfig{
		Namespaces:             ctx.Namespaces(),
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}

	env, err := NewEnv(config)
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

	activation := ctx.Activation()
	out, _, err := prg.Eval(activation)
	if err != nil {
		return nil, fmt.Errorf("CEL evaluation error: %w", err)
	}

	return ConvertToNative(out.Value()), nil
}

// =============================================================================
// TEMPLATE PARSING
// =============================================================================

// templateExpr represents a matched {{...}} template expression.
type templateExpr struct {
	expr  string // The extracted expression (trimmed)
	start int    // Start position in the input string
	end   int    // End position in the input string
}

// extractTemplates finds all {{...}} template expressions using balanced brace parsing.
// Correctly handles nested braces in CEL expressions like: {{ items.map(x, {id: x.id}) }}
func extractTemplates(input string) []templateExpr {
	var matches []templateExpr
	i := 0

	for i < len(input)-1 {
		if input[i] == '{' && input[i+1] == '{' {
			start := i
			exprStart := i + 2

			// Count braces to find matching }}
			braceCount := 2
			j := exprStart
			for j < len(input) && braceCount > 0 {
				switch input[j] {
				case '{':
					braceCount++
				case '}':
					braceCount--
				}
				j++
			}

			if braceCount == 0 {
				expr := input[exprStart : j-2]
				matches = append(matches, templateExpr{
					expr:  strings.TrimSpace(expr),
					start: start,
					end:   j,
				})
				i = j
			} else {
				i++
			}
		} else {
			i++
		}
	}

	return matches
}

// =============================================================================
// VALUE CONVERSION
// =============================================================================

// ConvertToNative recursively converts CEL types to native Go types.
// CEL's result.Value() may contain nested ref.Val types that don't serialize to JSON.
//
// Null handling: CEL represents null as types.NullValue, whose native Value() is
// the structpb.NullValue ENUM (an int32-kinded named type) — not Go nil. That enum
// is rejected by structpb.NewStruct/NewValue with "proto: invalid type:
// structpb.NullValue", and it JSON-marshals to the number 0. We normalize it to
// Go nil here (at every nesting level), which structpb natively represents as
// NullValue — so null CEL results are legal and representable end-to-end.
//
// This is the single shared normalizer for CEL evaluation results; every path
// that feeds CEL output into structpb.NewStruct/NewValue (loop outputs, workflow
// outputs, node config resolution, response_data) must go through it.
func ConvertToNative(v interface{}) interface{} {
	switch val := v.(type) {
	case structpb.NullValue:
		// CEL null → Go nil (see doc comment above).
		return nil
	case ref.Val:
		return ConvertToNative(val.Value())
	case []ref.Val:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = ConvertToNative(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = ConvertToNative(item)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, item := range val {
			result[k] = ConvertToNative(item)
		}
		return result
	case map[ref.Val]ref.Val:
		result := make(map[string]interface{})
		for k, item := range val {
			keyStr, _ := k.Value().(string)
			result[keyStr] = ConvertToNative(item)
		}
		return result
	default:
		return v
	}
}

// valueToString converts a value to a string for template interpolation.
// Arrays/slices are joined with commas; other types use default formatting.
func valueToString(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case []string:
		return strings.Join(v, ",")
	case []interface{}:
		parts := make([]string, len(v))
		for i, elem := range v {
			parts[i] = fmt.Sprintf("%v", elem)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", value)
	}
}
