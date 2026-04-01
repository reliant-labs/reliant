// Copyright (c) 2025 Reliant Labs
package schema

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ActivityCategory for grouping activities in UI
type ActivityCategory string

const (
	CategoryGit     ActivityCategory = "git"
	CategoryAgentic ActivityCategory = "agentic"
	CategoryUtility ActivityCategory = "utility"
	CategoryDebug   ActivityCategory = "debug"

	// Internal categories — used by non-builder-visible activities.
	CategoryWorktree           ActivityCategory = "git"
	CategoryMessageProcessing  ActivityCategory = "agentic"
	CategoryWorkflowManagement ActivityCategory = "utility"
	CategoryRunStep            ActivityCategory = "utility"
)

// ActivityMetadata contains metadata about an activity for UI display
type ActivityMetadata struct {
	ID           string           // Activity name (e.g., "CallLLM", "Approval")
	DisplayName  string           // Human-readable name
	Description  string           // What the activity does
	Category     ActivityCategory // For UI grouping
	InputFields  []InputFieldInfo // Generated from struct reflection
	OutputFields []InputFieldInfo // Output fields from struct reflection
	IconHint     string           // Optional icon hint for UI
}

// InputFieldInfo describes a single input field
type InputFieldInfo struct {
	Name               string   // JSON field name
	Type               string   // Schema type (string, integer, boolean, array, object)
	Description        string   // Human-readable description from reliant:"desc=..."
	Required           bool     // True if field is required (no omitempty)
	Default            any      // Default value from reliant:"default=..."
	EnumValues         []string // Valid values for enum fields from reliant:"enum=val1|val2"
	UIHint             string   // UI rendering hint from reliant:"ui=dropdown|textarea|input"
	Min                *float64 // Minimum value for numeric fields from reliant:"min=..."
	Max                *float64 // Maximum value for numeric fields from reliant:"max=..."
	Label              string   // Short UI label from proto metadata
	Placeholder        *string  // Optional helper text for text-like controls
	VisibilityContexts []string // Optional UI visibility contexts (basic/advanced/debug)
	CleanupSemantics   *string  // Optional cleanup behavior hint for clients
	IsCEL              bool     // True if this field supports CEL expressions (CelX wrapper)
	Category           string   // Per-field grouping category from proto annotation
}

// ActivityWithMetadata is implemented by activities that should appear in the workflow builder
type ActivityWithMetadata interface {
	Name() string
	DisplayName() string
	Description() string
	Category() ActivityCategory
}

// activityMetadataRegistry stores metadata for builder-visible activities
var activityMetadataRegistry = make(map[string]ActivityWithMetadata)

// RegisterActivityMetadata registers an activity that should be visible in the workflow builder.
// The activity must implement ActivityWithMetadata interface.
func RegisterActivityMetadata(activity ActivityWithMetadata) {
	activityMetadataRegistry[activity.Name()] = activity
}

// nodeMetaAdapter wraps a NodeMeta proto annotation to satisfy ActivityWithMetadata.
type nodeMetaAdapter struct {
	activityName string
	meta         *reliantv1.NodeMeta
}

func (a *nodeMetaAdapter) Name() string               { return a.activityName }
func (a *nodeMetaAdapter) DisplayName() string        { return a.meta.DisplayName }
func (a *nodeMetaAdapter) Description() string        { return a.meta.Description }
func (a *nodeMetaAdapter) Category() ActivityCategory { return ActivityCategory(a.meta.Category) }
func (a *nodeMetaAdapter) IconHint() string           { return a.meta.Icon }

// RegisterNodeTypeActivity registers proto descriptors and metadata for a node-type activity
// using NodeMeta proto annotations as the source of truth. This replaces separate calls to
// RegisterActivityProtoDescriptors and RegisterActivityMetadata for node-type activities.
func RegisterNodeTypeActivity(activityName string, meta *reliantv1.NodeMeta, inputDesc, outputDesc protoreflect.MessageDescriptor) {
	RegisterActivityProtoDescriptors(activityName, inputDesc, outputDesc)
	RegisterActivityMetadata(&nodeMetaAdapter{activityName: activityName, meta: meta})
}

// GetActivityMetadata returns metadata for a specific activity
func GetActivityMetadata(name string) (ActivityMetadata, bool) {
	activity, ok := activityMetadataRegistry[name]
	if !ok {
		return ActivityMetadata{}, false
	}

	// Get the activity type info for input/output fields
	typeInfo, hasTypeInfo := activityTypes[name]

	var inputFields []InputFieldInfo
	var outputFields []InputFieldInfo
	if hasTypeInfo {
		// Prefer proto descriptor-based extraction when available
		if typeInfo.InputDescriptor != nil {
			inputFields = extractInputFieldsFromProto(typeInfo.InputDescriptor)
		} else if typeInfo.InputType != nil {
			inputFields = extractInputFields(typeInfo.InputType)
		}
		if typeInfo.OutputDescriptor != nil {
			outputFields = extractInputFieldsFromProto(typeInfo.OutputDescriptor)
		} else if typeInfo.OutputType != nil {
			outputFields = extractInputFields(typeInfo.OutputType)
		}
	}

	// Strip V2_ prefix and convert to snake_case for consistency with step type field
	displayID := strings.TrimPrefix(name, "")
	snakeCaseID := toSnakeCase(displayID)

	iconHint := ""
	if adapter, ok := activity.(*nodeMetaAdapter); ok {
		iconHint = adapter.IconHint()
	}

	return ActivityMetadata{
		ID:           snakeCaseID,
		DisplayName:  activity.DisplayName(),
		Description:  activity.Description(),
		Category:     activity.Category(),
		InputFields:  inputFields,
		OutputFields: outputFields,
		IconHint:     iconHint,
	}, true
}

// ListVisibleActivities returns all activities that should be visible in the workflow builder
func ListVisibleActivities() []ActivityMetadata {
	result := make([]ActivityMetadata, 0, len(activityMetadataRegistry))

	for name := range activityMetadataRegistry {
		if meta, ok := GetActivityMetadata(name); ok {
			result = append(result, meta)
		}
	}

	// Sort by display name
	sort.Slice(result, func(i, j int) bool {
		return result[i].DisplayName < result[j].DisplayName
	})

	return result
}

// extractInputFields uses reflection to get input field info from a struct type.
// Fields with `reliant:"-"` tag are excluded (internal-only fields like chat_id).
//
// Descriptions are resolved in order:
//  1. From `reliant:"desc=..."` tag if present
//  2. From generated FieldDescriptions map (populated from Go docstrings)
//
// Supported reliant tag options:
//   - `-` : Hide field from schema (internal-only)
//   - `desc=text` : Human-readable description (optional, docstrings preferred)
//   - `default=value` : Default value
//   - `enum=val1|val2|val3` : Valid enum values (pipe-separated)
//   - `ui=dropdown|textarea|input` : UI rendering hint
//   - `min=0` : Minimum value for numeric fields
//   - `max=100` : Maximum value for numeric fields
//
// Example:
//
//	Thread string `json:"thread" reliant:"default=0,enum=0|child"`
func extractInputFields(t reflect.Type) []InputFieldInfo {
	if t == nil {
		return nil
	}

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil
	}

	typeName := t.Name()
	var fields []InputFieldInfo

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Skip fields marked as internal-only with reliant:"-" tag
		reliantTag := field.Tag.Get("reliant")
		if reliantTag == "-" {
			continue
		}

		// Get JSON field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse json tag (handle "name,omitempty" format)
		parts := strings.Split(jsonTag, ",")
		jsonName := parts[0]
		if jsonName == "" {
			jsonName = field.Name
		}

		// Check if required (no omitempty)
		required := true
		for _, part := range parts[1:] {
			if part == "omitempty" {
				required = false
				break
			}
		}

		// Parse reliant tag options
		fieldInfo := parseReliantTag(reliantTag)

		// If no description in tag, try the generated descriptions
		description := fieldInfo.description
		if description == "" {
			description = GetFieldDescription(typeName, jsonName)
		}

		// Map Go types to schema types
		fieldType := mapGoTypeToSchema(field.Type)

		fields = append(fields, InputFieldInfo{
			Name:        jsonName,
			Type:        fieldType,
			Description: description,
			Required:    required,
			Default:     fieldInfo.defaultVal,
			EnumValues:  fieldInfo.enumValues,
			UIHint:      fieldInfo.uiHint,
			Min:         fieldInfo.min,
			Max:         fieldInfo.max,
		})
	}

	return fields
}

// protoTypeToSchemaType maps proto-native type names to the frontend-expected
// schema type names that the UI switch statement can handle.
var protoTypeToSchemaType = map[string]string{
	"bool":           "boolean",
	"int":            "integer",
	"double":         "number",
	"string_list":    "array",
	"model_selector": "model",
}

// extractInputFieldsFromProto uses protoreflect and wfcel.ExtractFieldInfo to build
// InputFieldInfo from proto message descriptors. This replaces Go reflect-based
// extraction for proto types, using proto annotations as the single source of truth.
func extractInputFieldsFromProto(md protoreflect.MessageDescriptor) []InputFieldInfo {
	celFields := wfcel.ExtractFieldInfo(md)
	var fields []InputFieldInfo
	for _, f := range celFields {
		if f.Hidden {
			continue
		}
		// Skip raw message fields — they require custom UI components
		// and can't be rendered by the generic field renderer.
		// (CelX wrapper types like CelString are already resolved above.)
		if f.Type == "message" {
			continue
		}

		// Normalize proto type names to frontend-expected schema types
		fieldType := f.Type
		if mapped, ok := protoTypeToSchemaType[fieldType]; ok {
			fieldType = mapped
		}

		info := InputFieldInfo{
			Name:               f.Name,
			Type:               fieldType,
			Description:        f.Description,
			EnumValues:         f.EnumValues,
			UIHint:             f.UIHint,
			Min:                f.MinValue,
			Max:                f.MaxValue,
			Label:              f.Label,
			Placeholder:        f.Placeholder,
			VisibilityContexts: append([]string(nil), f.VisibilityContexts...),
			CleanupSemantics:   f.CleanupSemantics,
			IsCEL:              f.IsCEL || f.IsDirect,
			Category:           f.Category,
		}
		if f.DefaultValue != "" {
			info.Default = f.DefaultValue
		}
		fields = append(fields, info)
	}
	return fields
}

// reliantTagInfo holds parsed values from a reliant struct tag
type reliantTagInfo struct {
	description string
	defaultVal  any
	enumValues  []string
	uiHint      string
	min         *float64
	max         *float64
}

// parseReliantTag parses the reliant struct tag into its components.
// Format: reliant:"desc=text,default=val,enum=a|b|c,ui=dropdown,min=0,max=100"
func parseReliantTag(tag string) reliantTagInfo {
	info := reliantTagInfo{}
	if tag == "" || tag == "-" {
		return info
	}

	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch {
		case strings.HasPrefix(part, "desc="):
			info.description = strings.TrimPrefix(part, "desc=")

		case strings.HasPrefix(part, "default="):
			info.defaultVal = strings.TrimPrefix(part, "default=")

		case strings.HasPrefix(part, "enum="):
			enumStr := strings.TrimPrefix(part, "enum=")
			if enumStr != "" {
				info.enumValues = strings.Split(enumStr, "|")
			}

		case strings.HasPrefix(part, "ui="):
			info.uiHint = strings.TrimPrefix(part, "ui=")

		case strings.HasPrefix(part, "min="):
			if v, err := parseFloat(strings.TrimPrefix(part, "min=")); err == nil {
				info.min = &v
			}

		case strings.HasPrefix(part, "max="):
			if v, err := parseFloat(strings.TrimPrefix(part, "max=")); err == nil {
				info.max = &v
			}
		}
	}

	return info
}

// parseFloat parses a string to float64
func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

// toSnakeCase converts PascalCase to snake_case
// e.g., "CallLLM" -> "call_llm", "SaveMessage" -> "save_message"
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			// Add underscore before uppercase letter (unless previous char was also uppercase)
			prevIsUpper := i > 0 && s[i-1] >= 'A' && s[i-1] <= 'Z'
			nextIsLower := i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z'
			if !prevIsUpper || nextIsLower {
				result.WriteRune('_')
			}
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// mapGoTypeToSchema maps Go types to schema type strings.
// Some specific types are preserved for frontend special handling.
func mapGoTypeToSchema(t reflect.Type) string {
	// Check for specific named types that need special UI handling
	typeName := t.Name()
	if typeName == "ResponseToolDefinition" {
		return "ResponseToolDefinition"
	}

	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Ptr:
		return mapGoTypeToSchema(t.Elem())
	default:
		return "any"
	}
}
