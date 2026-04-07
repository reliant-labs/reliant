// Copyright (c) 2025 Reliant Labs
//
// Scenario schema generator - extracts Scenario types with docstrings.
//
// SOURCE OF TRUTH:
//   - internal/workflow/runtime/simulator/types.go
//
// GENERATED FILES:
//   - generated/docs-source/reference/scenario-schema.md
//
// Usage: go run ./tools/docgen/scenarios <simulator_dir> <md_output>

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
}

// Types to extract in order
var schemaTypes = map[string]int{
	"Scenario":       1,
	"SimulatedEvent": 2,
	"SimToolCall":    3,
	"Expectation":    4,
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <simulator_dir> <md_output>\n", os.Args[0])
		os.Exit(1)
	}

	simulatorDir := os.Args[1]
	mdOutput := os.Args[2]

	types, err := extractTypesFromDir(simulatorDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error extracting types: %v\n", err)
		os.Exit(1)
	}

	markdown := generateMarkdown(types)
	if err := os.MkdirAll(filepath.Dir(mdOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdOutput, []byte(markdown), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing markdown: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated scenario schema: %s (%d types)\n", mdOutput, len(types))
}

func extractTypesFromDir(dir string) ([]TypeInfo, error) {
	allTypes := []TypeInfo{}

	path := filepath.Join(dir, "types.go")
	types, err := extractTypes(path)
	if err != nil {
		return nil, fmt.Errorf("parsing types.go: %w", err)
	}
	allTypes = append(allTypes, types...)

	sort.Slice(allTypes, func(i, j int) bool {
		return allTypes[i].Order < allTypes[j].Order
	})

	return allTypes, nil
}

func extractTypes(path string) ([]TypeInfo, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	types := []TypeInfo{}
	commentMap := ast.NewCommentMap(fset, node, node.Comments)

	ast.Inspect(node, func(n ast.Node) bool {
		genDecl, ok := n.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			return true
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			typeName := typeSpec.Name.Name
			order, wanted := schemaTypes[typeName]
			if !wanted {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			var typeDoc string
			if genDecl.Doc != nil {
				typeDoc = genDecl.Doc.Text()
			} else if comments := commentMap[genDecl]; len(comments) > 0 {
				typeDoc = comments[0].Text()
			}

			typeInfo := TypeInfo{
				Name:        typeName,
				Description: strings.TrimSpace(typeDoc),
				Fields:      extractFields(structType),
				Order:       order,
			}

			types = append(types, typeInfo)
		}

		return true
	})

	return types, nil
}

func extractFields(structType *ast.StructType) []FieldInfo {
	fields := []FieldInfo{}

	for _, field := range structType.Fields.List {
		if len(field.Names) == 0 {
			continue
		}

		fieldName := field.Names[0].Name
		if !ast.IsExported(fieldName) {
			continue
		}

		fieldType := exprToString(field.Type)

		var jsonName string
		var omitempty bool
		if field.Tag != nil {
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
			jsonTag := tag.Get("json")
			if jsonTag != "" && jsonTag != "-" {
				parts := strings.Split(jsonTag, ",")
				jsonName = parts[0]
				omitempty = len(parts) > 1 && parts[1] == "omitempty"
			}
			if jsonName == "" {
				yamlTag := tag.Get("yaml")
				if yamlTag != "" && yamlTag != "-" {
					parts := strings.Split(yamlTag, ",")
					jsonName = parts[0]
					omitempty = len(parts) > 1 && parts[1] == "omitempty"
				}
			}
		}

		if jsonName == "" || jsonName == "-" {
			continue
		}

		var fieldDoc string
		if field.Doc != nil {
			fieldDoc = strings.TrimSpace(field.Doc.Text())
		} else if field.Comment != nil {
			fieldDoc = strings.TrimSpace(field.Comment.Text())
		}

		isPointer := strings.HasPrefix(fieldType, "*")
		required := !omitempty && !isPointer

		fields = append(fields, FieldInfo{
			Name:        fieldName,
			Type:        simplifyType(fieldType),
			JSONName:    jsonName,
			Required:    required,
			Description: fieldDoc,
		})
	}

	return fields
}

func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.ArrayType:
		return "[]" + exprToString(t.Elt)
	case *ast.MapType:
		return "map[" + exprToString(t.Key) + "]" + exprToString(t.Value)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "any"
	}
}

func simplifyType(t string) string {
	t = strings.TrimPrefix(t, "*")
	switch t {
	case "string":
		return "string"
	case "int", "int32", "int64":
		return "integer"
	case "float32", "float64":
		return "number"
	case "bool":
		return "boolean"
	case "interface{}":
		return "any"
	case "ExpectedOutcome":
		return "string"
	}
	if strings.HasPrefix(t, "[]") {
		inner := strings.TrimPrefix(t, "[]")
		return simplifyType(inner) + "[]"
	}
	if strings.HasPrefix(t, "map[string]interface{}") {
		return "object"
	}
	if strings.HasPrefix(t, "map[string]map[string]interface{}") {
		return "map[string]object"
	}
	if strings.HasPrefix(t, "map[") {
		return "object"
	}
	return t
}

func generateMarkdown(types []TypeInfo) string {
	var sb strings.Builder

	sb.WriteString(`---
title: Scenario Schema
description: Reference documentation for workflow testing scenarios
weight: 60
---

<!--
=============================================================================
GENERATED FILE - DO NOT EDIT DIRECTLY

Source: internal/workflow/runtime/simulator/types.go
Generator: tools/docgen/scenarios
Regenerate: make generate-scenario-schema
=============================================================================
-->

Scenarios define test cases for workflows. They provide simulated events (mocking LLM and tool interactions)
and expectations (assertions about what should happen).

`)

	for _, t := range types {
		fmt.Fprintf(&sb, "## %s\n\n", t.Name)

		if t.Description != "" {
			// Format description to handle code blocks
			desc := formatDocString(t.Description)
			sb.WriteString(desc + "\n\n")
		}

		if len(t.Fields) > 0 {
			sb.WriteString("### Fields\n\n")
			sb.WriteString("| Field | Type | Required | Description |\n")
			sb.WriteString("|-------|------|----------|-------------|\n")

			for _, f := range t.Fields {
				required := "No"
				if f.Required {
					required := "Yes"
					_ = required
				}
				if f.Required {
					required = "Yes"
				}

				// Take first line of description for table
				desc := f.Description
				if idx := strings.Index(desc, "\n"); idx > 0 {
					desc = desc[:idx]
				}
				if desc == "" {
					desc = "-"
				}
				desc = strings.ReplaceAll(desc, "|", "\\|")

				fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n",
					f.JSONName, f.Type, required, desc)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
	}

	// Add targeting nodes section
	sb.WriteString(`## Targeting Nodes

The ` + "`node`" + ` field on events targets specific nodes by ID:

- **Top-level nodes**: ` + "`node: \"call_llm\"`" + `
- **Inner loop nodes**: ` + "`node: \"loop_id.inner_node_id\"`" + ` (dot-separated)
- **Inner workflow nodes**: ` + "`node: \"workflow_id.inner_node_id\"`" + ` (for ` + "`type: workflow`" + ` with ` + "`inline:`" + `)
- **Nested structures**: ` + "`node: \"outer.inner.node_id\"`" + `

For inline loops and inline workflow nodes, the simulator executes each inner node individually, evaluates conditions, and tracks skipped nodes with their qualified IDs.
For ref-based nodes (external workflow reference), the node is mocked as a black box using the ref name.

**Event matching:**
- Events with a ` + "`node`" + ` field are matched to that specific node
- Events without a ` + "`node`" + ` field are consumed sequentially in order
- Multiple events with the same node are consumed in order per-node (use for multi-iteration loops)

---

## Event Types

| Type | Description | Required Fields |
|------|-------------|-----------------|
| ` + "`llm_response`" + ` | Simulate LLM returning text and/or tool_calls | ` + "`text`" + ` or ` + "`tool_calls`" + ` |
| ` + "`tool_result`" + ` | Simulate tool returning output | ` + "`tool`" + `, ` + "`tool_output`" + ` |
| ` + "`tool_error`" + ` | Simulate tool error | ` + "`tool`" + `, ` + "`tool_output`" + ` (with error) |
| ` + "`llm_error`" + ` | Simulate LLM error | ` + "`output`" + ` (with error structure) |
| ` + "`user_input`" + ` | Simulate user message | ` + "`text`" + ` |

---

## Complete Example

` + "```yaml" + `
name: agent_tool_usage
description: Tests agent loop with tool execution

events:
  # First iteration: LLM requests a tool
  - node: agent_loop.call_llm
    type: llm_response
    tool_calls:
      - name: bash
        input: {command: "ls -la"}
  
  # Tool returns result
  - node: agent_loop.execute_tools
    type: tool_result
    tool: bash
    output: {result: "file1.txt\nfile2.txt"}
  
  # Second iteration: LLM completes
  - node: agent_loop.call_llm
    type: llm_response
    text: "I found 2 files: file1.txt and file2.txt"

expect:
  outcome: completed
  reached:
    - agent_loop.call_llm
    - agent_loop.execute_tools
    - agent_loop.save_result
  not_reached:
    - error_handler
  node_outputs:
    agent_loop:
      iterations: 2
` + "```" + `
`)

	return sb.String()
}

func formatDocString(doc string) string {
	if doc == "" {
		return ""
	}

	var sb strings.Builder
	lines := strings.Split(doc, "\n")
	inCodeBlock := false

	for i, line := range lines {
		// Check for indented block (tab or 4 spaces)
		// We only care if it's NOT a list item
		isIndented := strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")

		if isIndented {
			if !inCodeBlock {
				// Check if we need a newline before starting the block
				if i > 0 && lines[i-1] != "" {
					sb.WriteString("\n")
				}
				sb.WriteString("```yaml\n")
				inCodeBlock = true
			}
			// Remove one level of indentation
			content := strings.TrimPrefix(line, "\t")
			if content == line {
				content = strings.TrimPrefix(line, "    ")
			}
			sb.WriteString(content + "\n")
		} else {
			if inCodeBlock {
				if strings.TrimSpace(line) == "" {
					// Check if the next line is indented to decide whether to close the block
					isNextIndented := false
					if i+1 < len(lines) {
						next := lines[i+1]
						isNextIndented = strings.HasPrefix(next, "\t") || strings.HasPrefix(next, "    ")
					}

					if isNextIndented {
						sb.WriteString("\n")
						continue
					}
				}

				sb.WriteString("```\n")
				inCodeBlock = false
			}
			sb.WriteString(line + "\n")
		}
	}

	if inCodeBlock {
		sb.WriteString("```\n")
	}

	return strings.TrimSpace(sb.String())
}
