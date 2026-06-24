// Copyright (c) 2025 Reliant Labs
//
// Node type reference generator - generates documentation and runtime descriptions.
//
// SOURCE OF TRUTH:
//   - Proto annotations [(reliant)] in workflow_v2.proto
//   - internal/workflow/v2/schema (node type metadata registry)
//
// The generator reads proto descriptors via wfcel.ExtractFieldInfo to get
// field metadata (descriptions, enum values, defaults, UI hints).
//
// GENERATED FILES:
//   - generated/docs-source/reference/nodes.md
//   - internal/workflow/v2/schema/field_descriptions.go (runtime descriptions for UI)
//
// Usage: go run ./tools/docgen/nodes <output>
// Regenerate with: make generate-nodes

package main

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	// Import activities package to trigger registration via init()
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// helperTypeInfo describes a nested type for inline expansion
type helperTypeInfo struct {
	Name       string
	Fields     []helperFieldInfo
	AccessPath string // e.g., "message.*" or "tool_calls[].*"
}

// helperFieldInfo describes a field within a helper type
type helperFieldInfo struct {
	Name        string
	Type        string
	Description string
}

// helperTypes defines the nested types to expand inline in output tables.
// Maps field name -> helper type info. Built from proto message descriptors.
var helperTypes map[string]helperTypeInfo

func init() {
	helperTypes = buildHelperTypes()
}

func buildHelperTypes() map[string]helperTypeInfo {
	result := make(map[string]helperTypeInfo)

	// MessageOutput
	msgFields := wfcel.ExtractFieldInfo((&reliantv1.MessageOutput{}).ProtoReflect().Descriptor())
	result["message"] = helperTypeInfo{
		Name:       "MessageOutput",
		AccessPath: "message",
		Fields:     celFieldsToHelper(msgFields),
	}

	// ThinkingOutput
	thinkFields := wfcel.ExtractFieldInfo((&reliantv1.ThinkingOutput{}).ProtoReflect().Descriptor())
	result["thinking"] = helperTypeInfo{
		Name:       "ThinkingOutput",
		AccessPath: "thinking",
		Fields:     celFieldsToHelper(thinkFields),
	}

	// ToolCalls
	tcFields := wfcel.ExtractFieldInfo((&reliantv1.ToolCallMsg{}).ProtoReflect().Descriptor())
	result["tool_calls"] = helperTypeInfo{
		Name:       "ToolCall",
		AccessPath: "tool_calls[]",
		Fields:     celFieldsToHelper(tcFields),
	}

	// ToolResults
	trFields := wfcel.ExtractFieldInfo((&reliantv1.ToolResultMsg{}).ProtoReflect().Descriptor())
	result["tool_results"] = helperTypeInfo{
		Name:       "ToolResult",
		AccessPath: "tool_results[]",
		Fields:     celFieldsToHelper(trFields),
	}

	return result
}

func celFieldsToHelper(fields []wfcel.FieldInfo) []helperFieldInfo {
	var result []helperFieldInfo
	for _, f := range fields {
		if f.Hidden {
			continue
		}
		result = append(result, helperFieldInfo{
			Name:        f.Name,
			Type:        f.Type,
			Description: f.Description,
		})
	}
	return result
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <hugo_output>\n", os.Args[0])
		os.Exit(1)
	}

	hugoOutput := os.Args[1]

	// Generate Go file with descriptions from proto annotations for runtime use
	goOutput := "internal/workflow/runtime/schema/field_descriptions.go"
	if err := generateGoDescriptions(goOutput); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing Go output: %v\n", err)
		os.Exit(1)
	}

	// Get all node types from the registry (already proto-powered)
	nodeTypes := schema.ListVisibleActivities()

	// Sort by display name for consistent output
	sort.Slice(nodeTypes, func(i, j int) bool {
		return nodeTypes[i].DisplayName < nodeTypes[j].DisplayName
	})

	// Parse NodeBase fields from V2Node proto message descriptor
	nodeBaseFields := parseNodeBaseFromProto()

	// Generate Hugo markdown (with frontmatter)
	hugoMarkdown := generateMarkdown(nodeTypes, nodeBaseFields, true)
	if err := os.MkdirAll(filepath.Dir(hugoOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(hugoOutput, []byte(hugoMarkdown), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing Hugo output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated node types reference: %s (%d node types)\n", hugoOutput, len(nodeTypes))
}

// formatGoCode formats Go source code using go/format.
func formatGoCode(code string, description string) []byte {
	formatted, err := format.Source([]byte(code))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to format %s: %v\n", description, err)
		return []byte(code)
	}
	return formatted
}

// generateGoDescriptions generates a Go file with field descriptions from proto annotations.
// Reads proto descriptors for all registered activity types and extracts descriptions.
func generateGoDescriptions(outputPath string) error {
	var sb strings.Builder

	sb.WriteString(`// Code generated by tools/docgen/nodes. DO NOT EDIT.
// Source: proto annotations [(reliant)] in workflow_v2.proto
// Regenerate with: make generate-nodes

package schema

// init populates FieldDescriptions with descriptions from proto annotations.
// The map and GetFieldDescription function are defined in field_descriptions_base.go.
func init() {
`)

	// Collect all field descriptions from registered activities
	descriptions := make(map[string]string)
	activityNames := schema.ListActivities()
	for _, name := range activityNames {
		meta, ok := schema.GetActivityMetadata(name)
		if !ok {
			continue
		}
		// Use activity ID (snake_case) as type prefix for input fields
		for _, f := range meta.InputFields {
			if f.Description != "" {
				key := meta.ID + "." + f.Name
				descriptions[key] = f.Description
			}
		}
		// Also index by display name for backward compat
		for _, f := range meta.OutputFields {
			if f.Description != "" {
				key := meta.ID + "_output." + f.Name
				descriptions[key] = f.Description
			}
		}
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(descriptions))
	for k := range descriptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		desc := descriptions[key]
		desc = strings.ReplaceAll(desc, `"`, `\"`)
		fmt.Fprintf(&sb, "\tFieldDescriptions[%q] = %q\n", key, desc)
	}

	sb.WriteString(`}
`)

	formatted := formatGoCode(sb.String(), "generated code")

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, formatted, 0644)
}

// NodeBaseField holds metadata for a common node field from V2Node proto.
type NodeBaseField struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

// parseNodeBaseFromProto extracts common node fields from V2Node proto message descriptor.
func parseNodeBaseFromProto() []NodeBaseField {
	md := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	fields := md.Fields()

	var result []NodeBaseField
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// Skip the args oneof (type-specific fields)
		if fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic() {
			continue
		}

		name := string(fd.Name())
		fieldType := protoFieldTypeDisplay(fd)
		description := getFieldDescription(fd)

		result = append(result, NodeBaseField{
			Name:        name,
			Type:        fieldType,
			Required:    name == "id" || name == "type",
			Description: description,
		})
	}

	return result
}

// protoFieldTypeDisplay returns a display type string for a proto field.
func protoFieldTypeDisplay(fd protoreflect.FieldDescriptor) string {
	if fd.Kind() == protoreflect.MessageKind {
		msgName := string(fd.Message().Name())
		// Map V2 prefix types to display names
		switch msgName {
		case "V2ThreadConfig":
			return "[ThreadConfig](/docs/reference/types/#threadconfig)"
		case "V2SaveMessageConfig":
			return "[SaveMessageConfig](/docs/reference/types/#savemessageconfig)"
		case "DirectCelBool":
			return "string"
		case "CelString":
			return "string"
		}
		return msgName
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return "integer"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	default:
		return fd.Kind().String()
	}
}

// getFieldDescription extracts the description from proto field's leading comment.
func getFieldDescription(fd protoreflect.FieldDescriptor) string {
	loc := fd.ParentFile().SourceLocations().ByDescriptor(fd)
	if loc.LeadingComments != "" {
		return cleanComment(loc.LeadingComments)
	}
	return ""
}

func cleanComment(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "// ")
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, " "))
}

// escapeMarkdownTable escapes characters that would break markdown table formatting.
func escapeMarkdownTable(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

// shouldExpandField returns true if this field type should have nested fields expanded inline.
func shouldExpandField(fieldType string) bool {
	return fieldType == "object" || fieldType == "array" || fieldType == "message"
}

// generateCommonNodeFields returns the markdown section documenting V2Node common fields.
func generateCommonNodeFields(fields []NodeBaseField) string {
	var sb strings.Builder

	sb.WriteString(`## Common Node Fields

These fields are available on **all** node types. They provide shared functionality for node identification, conditional execution, thread management, and output handling.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
`)

	for _, f := range fields {
		requiredStr := "No"
		if f.Required {
			requiredStr = "Yes"
		}
		desc := escapeMarkdownTable(f.Description)
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n",
			f.Name, f.Type, requiredStr, desc)
	}

	sb.WriteString(`
### Thread Configuration

The ` + "`thread`" + ` field controls conversation context:

` + "```yaml" + `
- id: analyze
  type: call_llm
  thread:
    mode: fork      # fork, new, or inherit (default)
    memo: true      # reuse across loop iterations
    inject:
      role: user
      content: "Analyze: {{inputs.data}}"
` + "```" + `

### Conditional Execution

Use ` + "`condition`" + ` to skip nodes based on runtime state:

` + "```yaml" + `
- id: cleanup
  type: run
  condition: "{{nodes.process.exit_code != 0}}"
  run: "rm -rf temp/"
` + "```" + `

### Automatic Message Saving

Use ` + "`save_message`" + ` to capture output without a separate save_message node:

` + "```yaml" + `
- id: summarize
  type: workflow
  ref: builtin://summarizer
  thread:
    mode: fork
  save_message:
    role: assistant
    content: "{{output.summary}}"
` + "```" + `

---

`)

	return sb.String()
}

func generateMarkdown(nodeTypes []schema.ActivityMetadata, nodeBaseFields []NodeBaseField, withHugoFrontmatter bool) string {
	var sb strings.Builder

	if withHugoFrontmatter {
		sb.WriteString(`---
# =============================================================================
# GENERATED FILE - DO NOT EDIT DIRECTLY
#
# Source of truth: proto annotations [(reliant)] + v2/schema (registry)
# Generated by: go run ./tools/docgen/nodes
# Regenerate with: make generate-nodes
# =============================================================================
title: Node Types Reference
weight: 15
description: Auto-generated API reference for workflow node type Input/Output schemas
---

`)
	} else {
		sb.WriteString(`<!-- =============================================================================
     GENERATED FILE - DO NOT EDIT DIRECTLY
     
     Source of truth: proto annotations [(reliant)] + v2/schema (registry)
     Generated by: go run ./tools/docgen/nodes
     Regenerate with: make generate-nodes
     ============================================================================= -->

`)
	}

	if !withHugoFrontmatter {
		sb.WriteString("# Node Types Reference\n\n")
	}

	sb.WriteString("This auto-generated reference documents the Input and Output schemas for workflow node types.\n")
	sb.WriteString("Descriptions are extracted from proto annotations in workflow_v2.proto.\n\n")

	// Add common node fields section
	if len(nodeBaseFields) > 0 {
		sb.WriteString(generateCommonNodeFields(nodeBaseFields))
	}

	for _, nodeType := range nodeTypes {
		fmt.Fprintf(&sb, "## %s\n\n", nodeType.DisplayName)

		if nodeType.Description != "" {
			fmt.Fprintf(&sb, "%s\n\n", cleanDescription(nodeType.Description))
		}

		if len(nodeType.InputFields) > 0 {
			sb.WriteString("### Inputs\n\n")
			sb.WriteString("| Field | Type | Required | Default | Description |\n")
			sb.WriteString("|-------|------|----------|---------|-------------|\n")

			for _, f := range nodeType.InputFields {
				required := "No"
				if f.Required {
					required = "Yes"
				}
				defaultVal := "-"
				if f.Default != nil {
					defaultVal = fmt.Sprintf("%v", f.Default)
				}
				desc := f.Description
				if desc == "" {
					desc = "-"
				} else {
					desc = cleanDescription(desc)
				}
				if len(f.EnumValues) > 0 {
					desc += fmt.Sprintf(" (enum: %s)", strings.Join(f.EnumValues, "|"))
				}
				if f.Min != nil || f.Max != nil {
					if f.Min != nil && f.Max != nil {
						desc += fmt.Sprintf(" (range: %.0f-%.0f)", *f.Min, *f.Max)
					} else if f.Min != nil {
						desc += fmt.Sprintf(" (min: %.0f)", *f.Min)
					} else {
						desc += fmt.Sprintf(" (max: %.0f)", *f.Max)
					}
				}

				fmt.Fprintf(&sb, "| `%s` | %s | %s | %s | %s |\n",
					f.Name, f.Type, required, defaultVal, escapeMarkdownTable(desc))
			}
			sb.WriteString("\n")
		}

		if len(nodeType.OutputFields) > 0 {
			sb.WriteString("### Outputs\n\n")
			sb.WriteString("| Field | Type | Description |\n")
			sb.WriteString("|-------|------|-------------|\n")

			for _, f := range nodeType.OutputFields {
				desc := f.Description
				if desc == "" {
					desc = "-"
				} else {
					desc = cleanDescription(desc)
				}

				fmt.Fprintf(&sb, "| `%s` | %s | %s |\n", f.Name, f.Type, escapeMarkdownTable(desc))

				// Expand nested types inline if this is an object or array field
				if shouldExpandField(f.Type) {
					if helper, ok := helperTypes[f.Name]; ok {
						for _, subField := range helper.Fields {
							fieldPath := fmt.Sprintf("%s.%s", helper.AccessPath, subField.Name)
							fmt.Fprintf(&sb, "| `%s` | %s | %s |\n",
								fieldPath, subField.Type, escapeMarkdownTable(subField.Description))
						}
					}
				}
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
	}

	return sb.String()
}

func cleanDescription(desc string) string {
	desc = strings.ReplaceAll(desc, "❌ ", "")
	desc = strings.ReplaceAll(desc, "❌", "")
	desc = strings.ReplaceAll(desc, "<good-example>", "```bash")
	desc = strings.ReplaceAll(desc, "</good-example>", "```")
	desc = strings.ReplaceAll(desc, "<bad-example>", "```bash")
	desc = strings.ReplaceAll(desc, "</bad-example>", "```")

	lines := strings.Split(desc, "\n")
	var newLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			newLines = append(newLines, "#### "+strings.TrimPrefix(line, "# "))
			continue
		}
		newLines = append(newLines, line)
	}
	return strings.Join(newLines, "\n")
}
