// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// TypeCheckResult represents the result of a type compatibility check.
type TypeCheckResult struct {
	Compatible bool
	Reason     string
	Suggestion string
}

// CheckTypeCompatibility checks if actual CEL output type is compatible with expected type.
//
// Compatibility rules:
// 1. Exact match → compatible
// 2. Dynamic actual → compatible (CEL couldn't infer, allow at runtime)
// 3. Nil/optional actual for optional expected → compatible
// 4. Element types must match for slices/maps
// 5. Mismatch → incompatible
func CheckTypeCompatibility(expected, actual *FieldInfo) TypeCheckResult {
	// Handle nil cases
	if expected == nil {
		return TypeCheckResult{
			Compatible: true,
			Reason:     "no type constraint",
		}
	}

	if actual == nil {
		if expected.IsOptional {
			return TypeCheckResult{
				Compatible: true,
				Reason:     "optional field allows nil",
			}
		}
		return TypeCheckResult{
			Compatible: false,
			Reason:     "expression evaluates to nil but field is required",
			Suggestion: "ensure expression produces a value or mark field as optional",
		}
	}

	// Rule 2: Dynamic types are always compatible (CEL couldn't infer)
	if actual.IsDynamic {
		return TypeCheckResult{
			Compatible: true,
			Reason:     "dynamic type (runtime check)",
		}
	}

	// Rule 1: Check kind compatibility
	if expected.Kind != actual.Kind {
		return TypeCheckResult{
			Compatible: false,
			Reason:     fmt.Sprintf("type mismatch: expected %s, got %s", formatKind(expected), formatKind(actual)),
			Suggestion: suggestTypeConversion(expected, actual),
		}
	}

	// For slices, check element types
	if expected.IsSlice && actual.IsSlice {
		return checkElementTypeCompatibility(expected, actual)
	}

	// For maps, check value types
	if expected.IsMap && actual.IsMap {
		return checkElementTypeCompatibility(expected, actual)
	}

	// Kinds match, types compatible
	return TypeCheckResult{
		Compatible: true,
		Reason:     "type match",
	}
}

// checkElementTypeCompatibility checks if element types of slices/maps are compatible.
func checkElementTypeCompatibility(expected, actual *FieldInfo) TypeCheckResult {
	// If expected element type is nil or dynamic, allow anything
	if expected.ElemType == nil {
		return TypeCheckResult{
			Compatible: true,
			Reason:     "no element type constraint",
		}
	}

	// If actual element type is nil or dynamic, allow (runtime check)
	if actual.ElemType == nil || actual.IsDynamic {
		return TypeCheckResult{
			Compatible: true,
			Reason:     "dynamic element type (runtime check)",
		}
	}

	// Compare element types - check both type identity and name
	// Type identity check is the most reliable
	if expected.ElemType == actual.ElemType {
		return TypeCheckResult{
			Compatible: true,
			Reason:     "element types match",
		}
	}

	// Check if element types have different names (different structs)
	expectedElemName := expected.ElemType.String()
	actualElemName := actual.ElemType.String()

	if expectedElemName != actualElemName {
		return TypeCheckResult{
			Compatible: false,
			Reason: fmt.Sprintf("element type mismatch: expected %s, got %s",
				expectedElemName, actualElemName),
			Suggestion: fmt.Sprintf("ensure expression produces %s elements", expectedElemName),
		}
	}

	// Check if element types are compatible by kind
	expectedElemKind := expected.ElemType.Kind()
	actualElemKind := actual.ElemType.Kind()

	if expectedElemKind != actualElemKind {
		return TypeCheckResult{
			Compatible: false,
			Reason: fmt.Sprintf("element type kind mismatch: expected %v, got %v",
				expectedElemKind, actualElemKind),
			Suggestion: fmt.Sprintf("ensure expression produces %s elements", expectedElemName),
		}
	}

	return TypeCheckResult{
		Compatible: true,
		Reason:     "element types compatible",
	}
}

// GetExpectedFieldType returns the expected type for a node config field.
// This uses the type registry to look up field expectations.
func GetExpectedFieldType(nodeType string, fieldName string) *FieldInfo {
	// Map node type strings to their config types
	// Currently only save_message is supported, but this can be extended
	var configType reflect.Type
	switch nodeType {
	case model.NodeTypeSaveMessage:
		configType = reflect.TypeOf(reliantv1.SaveMessageConfig{})
	default:
		// Unknown node type - no validation
		return nil
	}

	return GetExpectedFieldTypeByStruct(configType, fieldName)
}

// InferCELOutputType compiles a CEL expression and returns its inferred output type.
// Returns error if the expression doesn't compile.
func InferCELOutputType(expr string, env *cel.Env) (*FieldInfo, error) {
	if env == nil {
		return nil, fmt.Errorf("CEL environment is nil")
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}

	celType := ast.OutputType()
	return celTypeToFieldInfo(expr, celType), nil
}

// celTypeToFieldInfo converts a CEL type to FieldInfo.
func celTypeToFieldInfo(name string, celType *cel.Type) *FieldInfo {
	if celType == nil {
		return &FieldInfo{
			Name:      name,
			Kind:      reflect.Interface,
			IsDynamic: true,
		}
	}

	info := &FieldInfo{
		Name: name,
	}

	typeStr := celType.String()

	switch typeStr {
	case "string":
		info.Kind = reflect.String
	case "int":
		info.Kind = reflect.Int64
	case "uint":
		info.Kind = reflect.Uint64
	case "double":
		info.Kind = reflect.Float64
	case "bool":
		info.Kind = reflect.Bool
	case "bytes":
		info.Kind = reflect.Slice
		info.IsSlice = true
		info.ElemType = reflect.TypeOf(byte(0))
	case "dyn":
		info.Kind = reflect.Interface
		info.IsDynamic = true
	default:
		// Handle list(T), map(K,V), and custom types
		if len(typeStr) > 5 && typeStr[:5] == "list(" {
			info.Kind = reflect.Slice
			info.IsSlice = true
			// Check if element type is dyn
			if strings.Contains(typeStr, "dyn") {
				info.IsDynamic = true
			}
			// Try to infer element type
			info.ElemType = inferElementType(typeStr)
		} else if len(typeStr) > 4 && typeStr[:4] == "map(" {
			info.Kind = reflect.Map
			info.IsMap = true
			if strings.Contains(typeStr, "dyn") {
				info.IsDynamic = true
			}
		} else {
			// Custom object type
			info.Kind = reflect.Struct
			info.Description = typeStr
		}
	}

	return info
}

// inferElementType attempts to infer the element type from a list type string.
// e.g., "list(string)" -> reflect.TypeOf("")
// e.g., "list(message.ToolResult)" -> reflect.TypeOf(message.ToolResult{})
func inferElementType(listTypeStr string) reflect.Type {
	// Extract element type from "list(T)"
	if len(listTypeStr) < 7 { // "list(x)"
		return nil
	}

	elemStr := listTypeStr[5 : len(listTypeStr)-1] // Extract "T" from "list(T)"

	switch elemStr {
	case "string":
		return reflect.TypeOf("")
	case "int":
		return reflect.TypeOf(int64(0))
	case "bool":
		return reflect.TypeOf(false)
	case "double":
		return reflect.TypeOf(float64(0))
	case "message.ToolResult":
		return reflect.TypeOf(message.ToolResult{})
	case "message.ToolCall":
		return reflect.TypeOf(message.ToolCall{})
	case "dyn":
		return nil // Dynamic element type
	default:
		return nil
	}
}

// FormatTypeError creates a user-friendly error message for type mismatches.
func FormatTypeError(fieldName string, expected, actual *FieldInfo, result TypeCheckResult) string {
	if result.Compatible {
		return ""
	}

	msg := fmt.Sprintf("Type mismatch for '%s': %s", fieldName, result.Reason)

	if result.Suggestion != "" {
		msg += fmt.Sprintf("\n  Suggestion: %s", result.Suggestion)
	}

	if expected != nil {
		msg += fmt.Sprintf("\n  Expected: %s", formatFieldType(expected))
	}

	if actual != nil {
		msg += fmt.Sprintf("\n  Actual: %s", formatFieldType(actual))
	}

	return msg
}

// formatKind formats a FieldInfo's kind for display.
func formatKind(info *FieldInfo) string {
	if info == nil {
		return "unknown"
	}

	switch info.Kind {
	case reflect.Slice:
		return "array"
	case reflect.Map:
		return "map"
	case reflect.Struct:
		return "object"
	case reflect.Interface:
		if info.IsDynamic {
			return "dynamic"
		}
		return "any"
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int64:
		return "int"
	case reflect.Float64:
		return "double"
	case reflect.Bool:
		return "bool"
	default:
		return info.Kind.String()
	}
}

// formatFieldType formats a FieldInfo for user-facing error messages.
func formatFieldType(info *FieldInfo) string {
	if info == nil {
		return "unknown"
	}

	base := formatKind(info)

	if info.IsSlice && info.ElemType != nil {
		return fmt.Sprintf("[]%s", info.ElemType.Name())
	}

	if info.IsMap && info.ElemType != nil {
		return fmt.Sprintf("map[string]%s", info.ElemType.Name())
	}

	return base
}

// suggestTypeConversion suggests how to convert between types.
func suggestTypeConversion(expected, actual *FieldInfo) string {
	if expected == nil || actual == nil {
		return ""
	}

	// String to array
	if expected.Kind == reflect.Slice && actual.Kind == reflect.String {
		return "wrap in array: [expr]"
	}

	// Single item to array
	if expected.Kind == reflect.Slice {
		return fmt.Sprintf("wrap in array or use .map() to transform to %s", formatFieldType(expected))
	}

	// Int to string
	if expected.Kind == reflect.String && (actual.Kind == reflect.Int || actual.Kind == reflect.Int64) {
		return "convert to string: string(expr)"
	}

	return fmt.Sprintf("expected %s but got %s", formatFieldType(expected), formatFieldType(actual))
}
