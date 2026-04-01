// Copyright (c) 2025 Reliant Labs
//
// Workflow schema generator - generates top-level Workflow type documentation.
//
// SOURCE OF TRUTH:
//   - Proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
//
// For complex fields (inputs, nodes, etc.), the generator adds notes
// pointing to discovery tools rather than expanding the full type tree.
//
// GENERATED FILES:
//   - generated/docs-source/reference/workflow-schema.md
//
// Usage: go run ./tools/docgen/schema <md_output>

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TypeInfo holds extracted type metadata
type TypeInfo struct {
	Name        string
	Description string
	Fields      []FieldInfo
	Order       int
}

// FieldInfo holds extracted field metadata
type FieldInfo struct {
	Name        string
	Type        string
	JSONName    string
	Required    bool
	Description string
	ToolHint    string
}

// Complex types that should point to discovery tools
var toolHints = map[string]string{
	"inputs":  "Use list_input_types and get_input_type tools for input type details.",
	"nodes":   "Use list_node_types and get_node_type tools for node type details.",
	"outputs": "CEL expressions mapping output names to values. Use get_cel_reference for CEL syntax.",
	"edges":   "See Edge type below.",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <md_output>\n", os.Args[0])
		os.Exit(1)
	}

	mdOutput := os.Args[1]

	types := extractTypesFromProto()

	markdown := generateMarkdown(types)
	if err := os.MkdirAll(filepath.Dir(mdOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdOutput, []byte(markdown), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing markdown: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated workflow schema: %s (%d types)\n", mdOutput, len(types))
}

func extractTypesFromProto() []TypeInfo {
	return []TypeInfo{
		extractTypeFromMessage("Workflow",
			(&reliantv1.Workflow{}).ProtoReflect().Descriptor(),
			"Defines a complete workflow with nodes, edges, inputs, and outputs.", 1),
		extractTypeFromMessage("Edge",
			(&reliantv1.Edge{}).ProtoReflect().Descriptor(),
			"Connects a source node to destination(s) with conditional routing.", 2),
		extractTypeFromMessage("EdgeCase",
			(&reliantv1.EdgeCase{}).ProtoReflect().Descriptor(),
			"Defines one conditional routing path from an edge.", 3),
	}
}

func extractTypeFromMessage(name string, md protoreflect.MessageDescriptor, desc string, order int) TypeInfo {
	info := TypeInfo{
		Name:        name,
		Description: desc,
		Order:       order,
	}

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		fieldName := string(fd.Name())

		// Skip UI metadata and internal fields
		if fieldName == "ui" {
			continue
		}

		fieldType := simplifyProtoType(fd)
		fieldDesc := getFieldComment(fd)

		// Check for tool hints
		toolHint := toolHints[fieldName]

		info.Fields = append(info.Fields, FieldInfo{
			Name:        fieldName,
			Type:        fieldType,
			JSONName:    fieldName,
			Required:    fieldName == "name" || fieldName == "from",
			Description: fieldDesc,
			ToolHint:    toolHint,
		})
	}

	return info
}

func simplifyProtoType(fd protoreflect.FieldDescriptor) string {
	if fd.IsMap() {
		keyType := simplifyKind(fd.MapKey().Kind())
		valType := simplifyKind(fd.MapValue().Kind())
		if fd.MapValue().Kind() == protoreflect.MessageKind {
			valName := string(fd.MapValue().Message().Name())
			switch valName {
			case "V2Input":
				return "map[string]Input"
			case "Param":
				return "map[string]Input"
			}
			return "map[" + keyType + "]" + valName
		}
		return "map[" + keyType + "]" + valType
	}

	if fd.IsList() {
		if fd.Kind() == protoreflect.MessageKind {
			msgName := string(fd.Message().Name())
			return simplifyMessageName(msgName) + "[]"
		}
		return simplifyKind(fd.Kind()) + "[]"
	}

	if fd.Kind() == protoreflect.MessageKind {
		msgName := string(fd.Message().Name())
		return simplifyMessageName(msgName)
	}

	return simplifyKind(fd.Kind())
}

func simplifyKind(k protoreflect.Kind) string {
	switch k {
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return "integer"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	case protoreflect.MessageKind:
		return "object"
	default:
		return "any"
	}
}

func simplifyMessageName(name string) string {
	switch name {
	case "V2Node":
		return "Node"
	case "V2Edge":
		return "Edge"
	case "V2EdgeCase":
		return "EdgeCase"
	case "V2WorkflowUI":
		return "WorkflowUI"
	case "V2PresetsConfig":
		return "PresetsConfig"
	case "V2Input":
		return "Input"
	case "DirectCelBool":
		return "string"
	case "CelString":
		return "string"
	default:
		// Strip V2 prefix if present
		if strings.HasPrefix(name, "V2") {
			return name[2:]
		}
		return name
	}
}

func getFieldComment(fd protoreflect.FieldDescriptor) string {
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
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

func generateMarkdown(types []TypeInfo) string {
	var sb strings.Builder

	sb.WriteString(`<!--
=============================================================================
GENERATED FILE - DO NOT EDIT DIRECTLY

Source: proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
Generator: tools/docgen/schema
Regenerate: make generate-schema
=============================================================================
-->

# Workflow Schema Reference

`)

	for _, t := range types {
		sb.WriteString(fmt.Sprintf("## %s\n\n", t.Name))

		if t.Description != "" {
			sb.WriteString(t.Description + "\n\n")
		}

		if len(t.Fields) > 0 {
			sb.WriteString("### Fields\n\n")
			sb.WriteString("| Field | Type | Required | Description |\n")
			sb.WriteString("|-------|------|----------|-------------|\n")

			for _, f := range t.Fields {
				required := "No"
				if f.Required {
					required = "Yes"
				}

				desc := strings.Split(f.Description, "\n")[0]
				if f.ToolHint != "" {
					if desc != "" {
						desc += " "
					}
					desc += "*" + f.ToolHint + "*"
				}
				if desc == "" {
					desc = "-"
				}
				desc = strings.ReplaceAll(desc, "|", "\\|")

				sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n",
					f.JSONName, f.Type, required, desc))
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
	}

	return sb.String()
}
