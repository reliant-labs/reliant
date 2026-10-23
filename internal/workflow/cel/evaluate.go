package wfcel

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	v, _, err := EvaluateTemplateTyped(expr, ctx)
	return v, err
}

// EvaluateTemplateTyped is EvaluateTemplate that also reports the CEL type of
// the result: the expression's own type for a pure {{ expr }}, "string" for
// text with holes in it or none ("null_type" for an expression yielding null).
func EvaluateTemplateTyped(expr string, ctx CELEvalContext) (interface{}, string, error) {
	if expr == "" {
		return "", "string", nil
	}

	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", "string", nil
	}

	// A {{ that does not close is an error, never literal text.
	matches, err := ScanTemplate(trimmed)
	if err != nil {
		return nil, "", err
	}

	// No template expressions — return trimmed string as-is.
	if len(matches) == 0 {
		return trimmed, "string", nil
	}

	// Pure expression: entire trimmed string is a single {{expr}}.
	// Return the expression's native type, not a string.
	if len(matches) == 1 && matches[0].Start == 0 && matches[0].End == len(trimmed) {
		return evaluateRawTyped(matches[0].Expr, ctx)
	}

	// Mixed string with embedded expressions — interpolate to string.
	var result strings.Builder
	lastEnd := 0

	for _, match := range matches {
		result.WriteString(trimmed[lastEnd:match.Start])

		value, _, err := evaluateRawTyped(match.Expr, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("template expression '{{%s}}': %w", match.Expr, err)
		}

		if value != nil {
			result.WriteString(valueToString(value))
		}

		lastEnd = match.End
	}

	result.WriteString(trimmed[lastEnd:])
	return result.String(), "string", nil
}

// EvaluateValueTyped is EvaluateValue that also reports the CEL type name of
// the result ("int", "string", "list", "map", "bool", "double", "null_type",
// "google.protobuf.Timestamp", ...).
func EvaluateValueTyped(expr string, ctx CELEvalContext) (interface{}, string, error) {
	return evaluateRawTyped(expr, ctx)
}

// =============================================================================
// INTERNAL EVALUATION
// =============================================================================

// EvalPhase says which step of evaluating an expression failed.
type EvalPhase string

const (
	// PhaseSyntax: the text is not CEL.
	PhaseSyntax EvalPhase = "syntax"
	// PhaseCompile: it parses but does not type-check (an unknown name, a
	// function called with the wrong types).
	PhaseCompile EvalPhase = "compile"
	// PhaseEval: it compiled and failed on this data (a missing key, a
	// division by zero, the cost limit).
	PhaseEval EvalPhase = "eval"
)

// EvalIssue is one located problem in an expression. Line and Column are
// 1-based within the expression text.
type EvalIssue struct {
	Line, Column int
	Message      string
}

// EvalError is a failed evaluation of one CEL expression, with the phase it
// failed in and, for a syntax or compile failure, where.
type EvalError struct {
	Phase  EvalPhase
	Expr   string
	Issues []EvalIssue
	Err    error
}

func (e *EvalError) Error() string {
	if e.Phase == PhaseEval {
		return "CEL evaluation error: " + e.Err.Error()
	}
	return "CEL compilation error: " + e.Err.Error()
}

func (e *EvalError) Unwrap() error { return e.Err }

// evaluateRaw compiles and evaluates a CEL expression against a typed context.
// Returns the native Go value with all CEL internal types converted.
func evaluateRaw(expr string, ctx CELEvalContext) (interface{}, error) {
	v, _, err := evaluateRawTyped(expr, ctx)
	return v, err
}

func evaluateRawTyped(expr string, ctx CELEvalContext) (interface{}, string, error) {
	// Build the CEL environment from the context's namespaces.
	config := CELEnvConfig{
		Namespaces:             ctx.Namespaces(),
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}

	compiled, issues, err := Compile(config, expr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create CEL program: %w", err)
	}
	if issues != nil {
		evalErr := &EvalError{Phase: PhaseCompile, Expr: expr, Err: issues.Err()}
		for _, issue := range issues.Errors() {
			if strings.Contains(issue.Message, "Syntax error") {
				evalErr.Phase = PhaseSyntax
			}
			evalErr.Issues = append(evalErr.Issues, EvalIssue{
				Line:    issue.Location.Line(),
				Column:  issue.Location.Column() + 1,
				Message: strings.SplitN(issue.Message, "\n", 2)[0],
			})
		}
		return nil, "", evalErr
	}

	activation := ctx.Activation()
	if err := CheckWorkflowPathReference(compiled.Env, compiled.AST, activation); err != nil {
		return nil, "", err
	}
	out, _, err := compiled.Program.Eval(activation)
	if err != nil {
		return nil, "", &EvalError{Phase: PhaseEval, Expr: expr, Err: ExplainEvalError(err)}
	}

	return ConvertToNative(out.Value()), out.Type().TypeName(), nil
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
	case time.Time:
		// A timestamp has no JSON/structpb form; its string form is what
		// string(ts) gives, so an output can be a timestamp without a wrapper.
		return val.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return strconv.FormatFloat(val.Seconds(), 'f', -1, 64) + "s"
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
