// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"

	"github.com/google/cel-go/cel"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// =============================================================================
// FIELD INFO — local replacement for v3.FieldInfo
// =============================================================================

// FieldInfo contains metadata about a struct field for CEL validation.
// This is a local definition replacing v3.FieldInfo for validation purposes.
type FieldInfo struct {
	Name        string       // JSON field name (used in CEL expressions)
	Description string       // Human-readable description
	GoType      reflect.Type // Go type of the field
	Kind        reflect.Kind // Kind (string, int, slice, etc.)
	IsPointer   bool         // Whether the field is a pointer type
	IsSlice     bool         // Whether the field is a slice
	IsMap       bool         // Whether the field is a map
	IsOptional  bool         // Whether the field has omitempty
	IsDynamic   bool         // Whether this is a dynamic type (can't validate sub-fields)
	ElemType    reflect.Type // Element type for slices/maps

	// Properties holds nested property information for object types with JSON Schema.
	Properties map[string]*FieldInfo

	// AdditionalPropertiesAllowed indicates whether undefined properties are allowed.
	AdditionalPropertiesAllowed bool
}

// =============================================================================
// WORKFLOW TYPE CONTEXT — local replacement for v3.WorkflowTypeContext
// =============================================================================

// WorkflowTypeContext contains type information for a specific workflow.
// This enables deep validation of field access within dynamic namespaces.
type WorkflowTypeContext struct {
	// InputFields maps top-level input name to its type info
	InputFields map[string]*FieldInfo

	// InputGroups maps group name to its field info
	// Accessed as inputs.<group>.<field>
	InputGroups map[string]map[string]*FieldInfo

	// NodeOutputs maps node ID to its output field info
	// The inner map is field name -> field info
	NodeOutputs map[string]map[string]*FieldInfo

	// OutputFields maps workflow output name to its type info
	OutputFields map[string]*FieldInfo

	// NodeTypes maps node ID to its activity type (e.g., "call_llm", "execute_tools")
	NodeTypes map[string]string

	// Registry is the v2cel TypeRegistry used for node output type resolution.
	// When set, it replaces static maps for determining which node types have outputs.
	Registry *wfcel.TypeRegistry

	// ConditionalNodes tracks which nodes have a condition expression.
	// Key is node ID, value is the condition expression (for error messages).
	// JoinNodes are excluded since they use condition differently (for join mode: all/any).
	ConditionalNodes map[string]string

	// CurrentNodeOutputType is the CEL type for the current node's output.
	// This enables typed validation of the 'output' namespace in save_message contexts.
	// If nil, the output namespace falls back to DynType for backward compatibility.
	CurrentNodeOutputType *cel.Type

	// CurrentNodeID tracks which node we're validating templates for.
	// Used to provide better error messages and context-specific validation.
	CurrentNodeID string

	// ResponseTools holds type information for response tool validation.
	// This enables type-safe validation of response_data.<tool>.<field> access.
	ResponseTools *ResponseToolContext

	// NodesWithExtendedOutputs tracks node IDs that have runtime-extended outputs
	// beyond the proto schema (e.g., call_llm with response tools/structured output).
	// For these nodes, unknown field access in CEL returns dyn instead of an error.
	NodesWithExtendedOutputs map[string]bool

	// LenientInputs skips strict input field validation. Set for inline workflows
	// that receive inputs dynamically via args from the parent, so input names
	// cannot be validated statically.
	LenientInputs bool

	// IterItemFields holds typed field info for the iter.item namespace.
	// When set, iter is declared as ObjectType("iter") so the custom type provider
	// can validate field access on iter.item (e.g., iter.item.has_frontend).
	// When nil, iter falls back to DynType for backward compatibility.
	IterItemFields map[string]*FieldInfo
}

// =============================================================================
// RESPONSE TOOL TYPE CONTEXT — local replacement for v3.ResponseToolContext
// =============================================================================

// ResponseToolSchema represents a single response tool's schema.
type ResponseToolSchema struct {
	ToolName     string
	Fields       map[string]*FieldInfo // Field name -> type info
	SourceNodeID string                // Which call_llm defined this
}

// SourceType identifies the pattern of tool_calls source.
type SourceType int

const (
	SourceNode    SourceType = iota // {{nodes.X.tool_calls}}
	SourceLoop                      // {{nodes.X.outputs.tool_calls}}
	SourceDynamic                   // Complex expression
)

// ToolCallSource indicates where tool_calls comes from.
type ToolCallSource struct {
	Type       SourceType
	NodeID     string // For SourceNode
	LoopNodeID string // For SourceLoop
	Expression string // For SourceDynamic (fallback)
}

// ResponseToolContext holds all response tool type info for a workflow.
type ResponseToolContext struct {
	// For each execute_tools node, what tools are available?
	// Map: execute_tools node ID -> tool name -> schema
	AvailableTools map[string]map[string]*ResponseToolSchema

	// For each execute_tools node, where do tool_calls come from?
	// Map: execute_tools node ID -> source info
	ToolCallSources map[string]ToolCallSource
}
