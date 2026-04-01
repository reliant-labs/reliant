// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// RESPONSE TOOL DETECTION
// =============================================================================
//
// These helpers extract response tool information from upstream call_llm nodes.
// Used by StepExecutor to:
//   1. Ensure response_data has entries for expected tools (even if LLM doesn't call them)
//   2. Validate response tool data against JSON schemas at runtime
//
// Detection flow:
//   1. Look at tool_calls arg of execute_tools node (e.g., "{{nodes.call_llm.tool_calls}}")
//   2. Extract source node ID from the template
//   3. Find that node in workflow definition
//   4. Extract response_tool.name and schema if defined
// =============================================================================

// responseToolInfo contains information about a response tool from an upstream call_llm node.
type responseToolInfo struct {
	Name   string
	Schema map[string]interface{}
}

// detectResponseToolsFromWorkflow finds response tool info from upstream call_llm nodes.
// It examines the tool_calls arg to find the source call_llm node and extracts
// the response_tool name and schema.
//
// Parameters:
//   - toolCallsArg: The tool_calls argument from execute_tools node.
//     Accepts both template form "{{nodes.call_llm.tool_calls}}" and bare
//     CEL expression form "nodes.call_llm.tool_calls" (from CelStringRaw).
//   - nodes: All nodes in the workflow for lookup
//   - workflowInputs: Resolved workflow inputs, used to resolve CelString
//     expressions in response_tool.name (e.g., inputs.response_tool_name).
//     May be nil if inputs are not available.
//
// Returns:
//   - names: List of response tool names for response_data entry creation
//   - schemas: Map of tool name to JSON schema for validation
func detectResponseToolsFromWorkflow(
	toolCallsArg string,
	nodes []*reliantv1.Node,
	workflowInputs map[string]interface{},
) ([]string, map[string]map[string]interface{}) {
	// Extract source node ID from template like "{{nodes.X.tool_calls}}"
	// or bare expression "nodes.X.tool_calls" (from CelStringRaw)
	sourceNodeID := extractToolCallsSourceNode(toolCallsArg)
	if sourceNodeID == "" {
		return nil, nil
	}

	// Find the source node in the workflow
	var sourceNode *reliantv1.Node
	for _, node := range nodes {
		if model.NodeID(node) == sourceNodeID {
			sourceNode = node
			break
		}
	}
	if sourceNode == nil || model.NodeType(sourceNode) != model.NodeTypeCallLLM {
		return nil, nil
	}

	// Extract response tool info directly from proto fields
	info := extractResponseToolInfoProto(sourceNode, workflowInputs)
	if info == nil || info.Name == "" {
		return nil, nil
	}

	names := []string{info.Name}
	schemas := make(map[string]map[string]interface{})

	if info.Schema != nil {
		schemas[info.Name] = info.Schema
	}

	return names, schemas
}

// =============================================================================
// INTERNAL HELPERS
// =============================================================================

// toolCallsSourceRegex matches nodes.X.tool_calls patterns.
// Handles both bare expressions ("nodes.call_llm.tool_calls") and
// template-wrapped expressions ("{{nodes.call_llm.tool_calls}}").
var toolCallsSourceRegex = regexp.MustCompile(`^(?:\{\{)?nodes\.([a-zA-Z_][a-zA-Z0-9_]*)\.tool_calls(?:\}\})?$`)

// extractToolCallsSourceNode extracts a node ID from a tool_calls expression.
// Accepts both template form and bare CEL expression form:
//   - "{{nodes.call_llm.tool_calls}}" -> "call_llm"
//   - "nodes.call_llm.tool_calls" -> "call_llm"
func extractToolCallsSourceNode(expr string) string {
	expr = strings.TrimSpace(expr)
	matches := toolCallsSourceRegex.FindStringSubmatch(expr)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// extractResponseToolInfoProto extracts response_tool info from a proto V2Node.
// When the response_tool name is a CelString expression (e.g., inputs.response_tool_name),
// it resolves the name using workflowInputs if available.
func extractResponseToolInfoProto(node *reliantv1.Node, workflowInputs map[string]interface{}) *responseToolInfo {
	callLLMArgs := model.GetCallLLMArgs(node)
	if callLLMArgs == nil {
		return nil
	}

	rt := callLLMArgs.GetResponseTool()
	if rt == nil {
		return nil
	}

	var name string
	if model.CelStringIsExpr(rt.GetName()) {
		// Name is a CEL expression — try to resolve it using workflow inputs.
		name = resolveSimpleInputExpr(model.CelStringRaw(rt.GetName()), workflowInputs)
	} else {
		name = model.CelStringRaw(rt.GetName())
	}
	if name == "" {
		return nil
	}

	info := &responseToolInfo{Name: name}

	if schema := rt.GetSchema(); schema != nil {
		schemaMap := schema.AsMap()
		// The YAML parser stores CEL expressions as a sentinel Struct with
		// key "__cel_expr__". Resolve it using workflow inputs.
		if sentinelVal, ok := schemaMap["__cel_expr__"]; ok {
			switch v := sentinelVal.(type) {
			case string:
				// Resolve the CEL expression from inputs
				resolved := resolveSimpleInputExpr(v, workflowInputs)
				if resolved != "" {
					// resolveSimpleInputExpr returns a string via fmt.Sprintf;
					// for schemas we need the actual map from inputs.
					if workflowInputs != nil {
						exprKey := strings.TrimSpace(v)
						if strings.HasPrefix(exprKey, "{{") && strings.HasSuffix(exprKey, "}}") {
							exprKey = strings.TrimSpace(exprKey[2 : len(exprKey)-2])
						}
						inputKey := strings.TrimPrefix(exprKey, "inputs.")
						if inputKey != exprKey {
							if m, ok := workflowInputs[inputKey].(map[string]interface{}); ok {
								schemaMap = m
							}
						}
					}
				}
			case map[string]interface{}:
				// ResolveCELFields already resolved it; unwrap.
				schemaMap = v
			}
		}
		info.Schema = schemaMap
	}

	return info
}

// resolveSimpleInputExpr resolves simple "inputs.X" CEL expressions
// by looking up the value in the workflow inputs map.
// Handles both bare expressions ("inputs.X") and template-wrapped ("{{inputs.X}}").
// Returns "" if the expression is not a simple input reference or can't be resolved.
func resolveSimpleInputExpr(expr string, inputs map[string]interface{}) string {
	if inputs == nil {
		return ""
	}
	// Strip {{}} wrapper if present
	e := strings.TrimSpace(expr)
	if strings.HasPrefix(e, "{{") && strings.HasSuffix(e, "}}") {
		e = strings.TrimSpace(e[2 : len(e)-2])
	}
	key := strings.TrimPrefix(e, "inputs.")
	if key == e {
		// Not an inputs.X expression
		return ""
	}
	if val, ok := inputs[key]; ok {
		return fmt.Sprintf("%v", val)
	}
	return ""
}
