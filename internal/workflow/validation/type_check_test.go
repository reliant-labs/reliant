// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestCheckTypeCompatibility_ExactMatch tests that exact type matches are compatible.
func TestCheckTypeCompatibility_ExactMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected *FieldInfo
		actual   *FieldInfo
		want     bool
	}{
		{
			name: "string matches string",
			expected: &FieldInfo{
				Kind: reflect.String,
			},
			actual: &FieldInfo{
				Kind: reflect.String,
			},
			want: true,
		},
		{
			name: "int matches int",
			expected: &FieldInfo{
				Kind: reflect.Int64,
			},
			actual: &FieldInfo{
				Kind: reflect.Int64,
			},
			want: true,
		},
		{
			name: "bool matches bool",
			expected: &FieldInfo{
				Kind: reflect.Bool,
			},
			actual: &FieldInfo{
				Kind: reflect.Bool,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckTypeCompatibility(tt.expected, tt.actual)
			assert.Equal(t, tt.want, result.Compatible, "result.Reason: %s", result.Reason)
		})
	}
}

// TestCheckTypeCompatibility_SliceTypes tests array/slice type compatibility.
func TestCheckTypeCompatibility_SliceTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected *FieldInfo
		actual   *FieldInfo
		want     bool
		reason   string
	}{
		{
			name: "[]ToolResult matches []ToolResult",
			expected: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolResult{}),
			},
			actual: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolResult{}),
			},
			want: true,
		},
		{
			name: "[]ToolResult does not match []ToolCall",
			expected: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolResult{}),
			},
			actual: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolCall{}),
			},
			want: false,
		},
		{
			name: "[]string matches []string",
			expected: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(""),
			},
			actual: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(""),
			},
			want: true,
		},
		{
			name: "[]any is dynamic, always compatible",
			expected: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolResult{}),
			},
			actual: &FieldInfo{
				Kind:      reflect.Slice,
				IsSlice:   true,
				IsDynamic: true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckTypeCompatibility(tt.expected, tt.actual)
			assert.Equal(t, tt.want, result.Compatible, "result.Reason: %s", result.Reason)
		})
	}
}

// TestCheckTypeCompatibility_DynamicAllowed tests that dynamic types are always allowed.
func TestCheckTypeCompatibility_DynamicAllowed(t *testing.T) {
	t.Parallel()
	expected := &FieldInfo{
		Kind:     reflect.Slice,
		IsSlice:  true,
		ElemType: reflect.TypeOf(message.ToolResult{}),
	}

	actual := &FieldInfo{
		Kind:      reflect.Interface,
		IsDynamic: true,
	}

	result := CheckTypeCompatibility(expected, actual)
	assert.True(t, result.Compatible, "dynamic types should always be compatible")
	assert.Contains(t, result.Reason, "dynamic")
}

// TestCheckTypeCompatibility_Mismatch tests type mismatches are detected.
func TestCheckTypeCompatibility_Mismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		expected   *FieldInfo
		actual     *FieldInfo
		wantReason string
	}{
		{
			name: "string vs int",
			expected: &FieldInfo{
				Kind: reflect.String,
			},
			actual: &FieldInfo{
				Kind: reflect.Int64,
			},
			wantReason: "type mismatch",
		},
		{
			name: "array vs string",
			expected: &FieldInfo{
				Kind:    reflect.Slice,
				IsSlice: true,
			},
			actual: &FieldInfo{
				Kind: reflect.String,
			},
			wantReason: "type mismatch",
		},
		{
			name: "int vs bool",
			expected: &FieldInfo{
				Kind: reflect.Int64,
			},
			actual: &FieldInfo{
				Kind: reflect.Bool,
			},
			wantReason: "type mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckTypeCompatibility(tt.expected, tt.actual)
			assert.False(t, result.Compatible)
			assert.Contains(t, result.Reason, tt.wantReason)
		})
	}
}

// TestGetExpectedFieldType_SaveMessage tests expected types for save_message fields.
func TestGetExpectedFieldType_SaveMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		fieldName string
		wantKind  reflect.Kind
		wantElem  reflect.Type
	}{
		{
			name:      "tool_results expects []ToolResult",
			fieldName: "tool_results",
			wantKind:  reflect.Slice,
			wantElem:  reflect.TypeOf(message.ToolResult{}),
		},
		{
			name:      "tool_calls expects []ToolCall",
			fieldName: "tool_calls",
			wantKind:  reflect.Slice,
			wantElem:  reflect.TypeOf(message.ToolCall{}),
		},
		{
			name:      "attachments expects []string",
			fieldName: "attachments",
			wantKind:  reflect.Slice,
			wantElem:  reflect.TypeOf(""),
		},
		{
			name:      "role expects string",
			fieldName: "role",
			wantKind:  reflect.String,
		},
		{
			name:      "content expects string",
			fieldName: "content",
			wantKind:  reflect.String,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := GetExpectedFieldType("save_message", tt.fieldName)
			require.NotNil(t, info)
			assert.Equal(t, tt.wantKind, info.Kind)
			if tt.wantElem != nil {
				assert.Equal(t, tt.wantElem, info.ElemType)
			}
		})
	}
}

// TestInferCELOutputType_Simple tests output type inference for simple expressions.
func TestInferCELOutputType_Simple(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		cel.Variable("str", cel.StringType),
		cel.Variable("num", cel.IntType),
		cel.Variable("flag", cel.BoolType),
	)
	require.NoError(t, err)

	tests := []struct {
		name     string
		expr     string
		wantKind reflect.Kind
	}{
		{
			name:     "string literal",
			expr:     `"hello"`,
			wantKind: reflect.String,
		},
		{
			name:     "int literal",
			expr:     "42",
			wantKind: reflect.Int64,
		},
		{
			name:     "bool literal",
			expr:     "true",
			wantKind: reflect.Bool,
		},
		{
			name:     "string variable",
			expr:     "str",
			wantKind: reflect.String,
		},
		{
			name:     "int variable",
			expr:     "num",
			wantKind: reflect.Int64,
		},
		{
			name:     "bool comparison",
			expr:     "num > 10",
			wantKind: reflect.Bool,
		},
		{
			name:     "string concat",
			expr:     `str + " world"`,
			wantKind: reflect.String,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := InferCELOutputType(tt.expr, env)
			require.NoError(t, err)
			require.NotNil(t, info)
			assert.Equal(t, tt.wantKind, info.Kind)
		})
	}
}

// TestInferCELOutputType_ObjectLiteral tests object literal construction.
func TestInferCELOutputType_ObjectLiteral(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&message.ToolResult{}),
		),
		cel.Variable("id", cel.StringType),
		cel.Variable("name", cel.StringType),
		cel.Variable("content", cel.StringType),
	)
	require.NoError(t, err)

	// Test object construction
	expr := `message.ToolResult{tool_call_id: id, name: name, content: content}`

	info, err := InferCELOutputType(expr, env)
	require.NoError(t, err)
	require.NotNil(t, info)

	// CEL should infer this as a struct type
	assert.True(t, info.Kind == reflect.Struct || info.IsDynamic,
		"object literals should be struct or dynamic")
}

// TestInferCELOutputType_MapExpression tests map/filter operations.
func TestInferCELOutputType_MapExpression(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&message.ToolResult{}),
		),
		cel.Variable("results", cel.ListType(cel.ObjectType("message.ToolResult"))),
	)
	require.NoError(t, err)

	tests := []struct {
		name     string
		expr     string
		wantKind reflect.Kind
	}{
		{
			name:     "filter returns list",
			expr:     "results.filter(r, !r.is_error)",
			wantKind: reflect.Slice,
		},
		{
			name:     "map returns list",
			expr:     "results.map(r, r.content)",
			wantKind: reflect.Slice,
		},
		{
			name:     "size returns int",
			expr:     "results.size()",
			wantKind: reflect.Int64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := InferCELOutputType(tt.expr, env)
			require.NoError(t, err)
			require.NotNil(t, info)
			assert.Equal(t, tt.wantKind, info.Kind)
		})
	}
}

// TestIntegration_ToolResultsValidation tests end-to-end validation of tool_results field.
func TestIntegration_ToolResultsValidation(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&message.ToolResult{}),
		),
		cel.Variable("results", cel.ListType(cel.ObjectType("message.ToolResult"))),
		cel.Variable("wrong_type", cel.StringType),
	)
	require.NoError(t, err)

	tests := []struct {
		name       string
		expr       string
		wantCompat bool
	}{
		{
			name:       "correct: []ToolResult variable",
			expr:       "results",
			wantCompat: true,
		},
		{
			name:       "correct: filtered []ToolResult",
			expr:       "results.filter(r, !r.is_error)",
			wantCompat: true,
		},
		{
			name:       "incorrect: string instead of array",
			expr:       "wrong_type",
			wantCompat: false,
		},
		{
			name:       "correct: empty array literal",
			expr:       "[]",
			wantCompat: true, // Dynamic list is compatible
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Infer actual type
			actual, err := InferCELOutputType(tt.expr, env)
			require.NoError(t, err)

			// Get expected type
			expected := GetExpectedFieldType("save_message", "tool_results")
			require.NotNil(t, expected)

			// Check compatibility
			result := CheckTypeCompatibility(expected, actual)
			assert.Equal(t, tt.wantCompat, result.Compatible,
				"expr: %s, reason: %s", tt.expr, result.Reason)
		})
	}
}

// TestIntegration_ManualConstruction tests manually constructed objects.
func TestIntegration_ManualConstruction(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&message.ToolResult{}),
		),
		cel.Variable("id", cel.StringType),
		cel.Variable("name", cel.StringType),
		cel.Variable("content", cel.StringType),
	)
	require.NoError(t, err)

	// Manual construction of ToolResult array
	expr := `[message.ToolResult{
		tool_call_id: id,
		name: name,
		content: content,
		is_error: false
	}]`

	actual, err := InferCELOutputType(expr, env)
	require.NoError(t, err)

	expected := GetExpectedFieldType("save_message", "tool_results")
	require.NotNil(t, expected)

	result := CheckTypeCompatibility(expected, actual)
	assert.True(t, result.Compatible,
		"manually constructed array should be compatible, reason: %s", result.Reason)
}

// TestIntegration_ConditionalLogic tests conditional expressions.
func TestIntegration_ConditionalLogic(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv(
		ext.NativeTypes(
			ext.ParseStructTag("json"),
			reflect.TypeOf(&message.ToolResult{}),
		),
		cel.Variable("results", cel.ListType(cel.ObjectType("message.ToolResult"))),
		cel.Variable("has_errors", cel.BoolType),
	)
	require.NoError(t, err)

	// Conditional: both branches return []ToolResult
	expr := `has_errors ? results.filter(r, !r.is_error) : results`

	actual, err := InferCELOutputType(expr, env)
	require.NoError(t, err)

	expected := GetExpectedFieldType("save_message", "tool_results")
	require.NotNil(t, expected)

	result := CheckTypeCompatibility(expected, actual)
	assert.True(t, result.Compatible,
		"conditional with same type branches should be compatible, reason: %s", result.Reason)
}

// TestFormatTypeError tests error message formatting.
func TestFormatTypeError(t *testing.T) {
	t.Parallel()
	expected := &FieldInfo{
		Kind:     reflect.Slice,
		IsSlice:  true,
		ElemType: reflect.TypeOf(message.ToolResult{}),
	}

	actual := &FieldInfo{
		Kind: reflect.String,
	}

	result := CheckTypeCompatibility(expected, actual)
	require.False(t, result.Compatible)

	msg := FormatTypeError("tool_results", expected, actual, result)
	assert.Contains(t, msg, "tool_results")
	assert.Contains(t, msg, "Type mismatch")
	assert.NotEmpty(t, msg)
}

// TestInferElementType tests element type inference from list type strings.
func TestInferElementType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		typeStr  string
		wantType reflect.Type
	}{
		{
			name:     "list(string)",
			typeStr:  "list(string)",
			wantType: reflect.TypeOf(""),
		},
		{
			name:     "list(int)",
			typeStr:  "list(int)",
			wantType: reflect.TypeOf(int64(0)),
		},
		{
			name:     "list(bool)",
			typeStr:  "list(bool)",
			wantType: reflect.TypeOf(false),
		},
		{
			name:     "list(message.ToolResult)",
			typeStr:  "list(message.ToolResult)",
			wantType: reflect.TypeOf(message.ToolResult{}),
		},
		{
			name:     "list(dyn)",
			typeStr:  "list(dyn)",
			wantType: nil, // Dynamic
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferElementType(tt.typeStr)
			assert.Equal(t, tt.wantType, got)
		})
	}
}

// TestFormatFieldType tests field type formatting for user messages.
func TestFormatFieldType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info *FieldInfo
		want string
	}{
		{
			name: "string",
			info: &FieldInfo{Kind: reflect.String},
			want: "string",
		},
		{
			name: "int",
			info: &FieldInfo{Kind: reflect.Int64},
			want: "int",
		},
		{
			name: "[]ToolResult",
			info: &FieldInfo{
				Kind:     reflect.Slice,
				IsSlice:  true,
				ElemType: reflect.TypeOf(message.ToolResult{}),
			},
			want: "[]ToolResult",
		},
		{
			name: "dynamic",
			info: &FieldInfo{
				Kind:      reflect.Interface,
				IsDynamic: true,
			},
			want: "dynamic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatFieldType(tt.info)
			assert.Equal(t, tt.want, got)
		})
	}
}
