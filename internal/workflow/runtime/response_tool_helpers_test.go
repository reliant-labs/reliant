// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// loadTestNodes is a helper that creates a []*reliantv1.Node from a workflow JSON string.
func loadTestNodes(t *testing.T, wfJSON string) []*reliantv1.Node {
	t.Helper()
	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)
	return wf.GetNodes()
}

func Test_detectResponseToolsFromWorkflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		toolCallsArg   string
		workflowInputs map[string]interface{}
		wfJSON         string
		wantNames      []string
		wantHasSchemas bool
	}{
		{
			name:         "detects response tool from call_llm node (template form)",
			toolCallsArg: "{{nodes.call_llm.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "plan_review",
							"schema": {
								"type": "object",
								"required": ["verdict", "summary"]
							}
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      []string{"plan_review"},
			wantHasSchemas: true,
		},
		{
			name:         "detects response tool from call_llm node (bare expression form)",
			toolCallsArg: "nodes.call_llm.tool_calls",
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "plan_review",
							"schema": {
								"type": "object",
								"required": ["verdict", "summary"]
							}
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      []string{"plan_review"},
			wantHasSchemas: true,
		},
		{
			name:         "detects response tool without schema",
			toolCallsArg: "{{nodes.call_llm.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "simple_review"
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      []string{"simple_review"},
			wantHasSchemas: false,
		},
		{
			name:         "returns nil for non-template arg",
			toolCallsArg: "not a template",
			wfJSON: `{
				"name": "test", "entry": ["n"],
				"nodes": [{"id": "n", "type": "noop"}],
				"edges": []
			}`,
			wantNames:      nil,
			wantHasSchemas: false,
		},
		{
			name:         "returns nil when source node not found",
			toolCallsArg: "{{nodes.missing_node.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["other_node"],
				"nodes": [{"id": "other_node", "type": "call_llm"}],
				"edges": []
			}`,
			wantNames:      nil,
			wantHasSchemas: false,
		},
		{
			name:         "returns nil for non-call_llm source node",
			toolCallsArg: "{{nodes.execute_tools.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["execute_tools"],
				"nodes": [{"id": "execute_tools", "type": "execute_tools", "args": {"tool_calls": "test"}}],
				"edges": []
			}`,
			wantNames:      nil,
			wantHasSchemas: false,
		},
		{
			name:         "returns nil when response_tool not configured",
			toolCallsArg: "{{nodes.call_llm.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{"id": "call_llm", "type": "call_llm", "args": {"model": "claude-sonnet-4-20250514"}}],
				"edges": []
			}`,
			wantNames:      nil,
			wantHasSchemas: false,
		},
		{
			name:         "skips CelString expression in name without inputs",
			toolCallsArg: "{{nodes.call_llm.tool_calls}}",
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "{{inputs.tool_name}}"
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      nil,
			wantHasSchemas: false,
		},
		{
			name:         "resolves CelString expression in name with workflow inputs",
			toolCallsArg: "nodes.call_llm.tool_calls",
			workflowInputs: map[string]interface{}{
				"response_tool_name": "submit_review",
			},
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "{{inputs.response_tool_name}}",
							"schema": {
								"type": "object",
								"required": ["strategy", "winner"]
							}
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      []string{"submit_review"},
			wantHasSchemas: true,
		},
		{
			name:         "resolves schema from workflow inputs when proto schema is CEL expression",
			toolCallsArg: "nodes.call_llm.tool_calls",
			workflowInputs: map[string]interface{}{
				"response_tool_name": "submit_verdict",
				"response_schema": map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"verdict", "reason"},
				},
			},
			wfJSON: `{
				"name": "test", "entry": ["call_llm"],
				"nodes": [{
					"id": "call_llm", "type": "call_llm",
					"args": {
						"response_tool": {
							"name": "{{inputs.response_tool_name}}",
							"schema": "{{inputs.response_schema}}"
						}
					}
				}],
				"edges": []
			}`,
			wantNames:      []string{"submit_verdict"},
			wantHasSchemas: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := loadTestNodes(t, tt.wfJSON)
			names, schemas := detectResponseToolsFromWorkflow(tt.toolCallsArg, nodes, tt.workflowInputs)

			// Check names
			if len(names) != len(tt.wantNames) {
				t.Errorf("detectResponseToolsFromWorkflow() names = %v, want %v", names, tt.wantNames)
				return
			}
			for i, name := range names {
				if name != tt.wantNames[i] {
					t.Errorf("detectResponseToolsFromWorkflow() names[%d] = %v, want %v", i, name, tt.wantNames[i])
				}
			}

			// Check schemas
			hasSchemas := len(schemas) > 0
			if hasSchemas != tt.wantHasSchemas {
				t.Errorf("detectResponseToolsFromWorkflow() hasSchemas = %v, want %v, schemas = %v", hasSchemas, tt.wantHasSchemas, schemas)
			}

			// Verify no schema contains the __cel_expr__ sentinel key
			for toolName, schema := range schemas {
				if _, hasSentinel := schema["__cel_expr__"]; hasSentinel {
					t.Errorf("schema for %q still contains __cel_expr__ sentinel (not resolved): %v", toolName, schema)
				}
			}
		})
	}
}

// TestDetectResponseTools_SchemaContent verifies the actual schema content
// is correct after sentinel resolution, not just that schemas exist.
func TestDetectResponseTools_SchemaContent(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test", "entry": ["call_llm"],
		"nodes": [{
			"id": "call_llm", "type": "call_llm",
			"args": {
				"response_tool": {
					"name": "{{inputs.response_tool_name}}",
					"schema": "{{inputs.response_schema}}"
				}
			}
		}],
		"edges": []
	}`

	nodes := loadTestNodes(t, wfJSON)
	inputs := map[string]interface{}{
		"response_tool_name": "submit_verdict",
		"response_schema": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"verdict", "reason"},
			"properties": map[string]interface{}{
				"verdict": map[string]interface{}{"type": "string"},
				"reason":  map[string]interface{}{"type": "string"},
			},
		},
	}

	names, schemas := detectResponseToolsFromWorkflow("nodes.call_llm.tool_calls", nodes, inputs)
	require.Equal(t, []string{"submit_verdict"}, names)
	require.Contains(t, schemas, "submit_verdict")

	schema := schemas["submit_verdict"]

	// Must NOT contain the sentinel
	_, hasSentinel := schema["__cel_expr__"]
	require.False(t, hasSentinel, "schema should not contain __cel_expr__ sentinel, got: %v", schema)

	// Must contain actual schema properties
	require.Equal(t, "object", schema["type"], "schema type should be 'object', got: %v", schema)
	props, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok, "schema should have 'properties' map, got: %v", schema)
	require.Contains(t, props, "verdict")
	require.Contains(t, props, "reason")
}

func TestExtractToolCallsSourceNode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
		want string
	}{
		{
			name: "template form",
			expr: "{{nodes.call_llm.tool_calls}}",
			want: "call_llm",
		},
		{
			name: "bare expression form (CelStringRaw output)",
			expr: "nodes.call_llm.tool_calls",
			want: "call_llm",
		},
		{
			name: "template with underscores",
			expr: "{{nodes.my_call_llm_node.tool_calls}}",
			want: "my_call_llm_node",
		},
		{
			name: "bare expression with underscores",
			expr: "nodes.my_call_llm_node.tool_calls",
			want: "my_call_llm_node",
		},
		{
			name: "template with numbers",
			expr: "{{nodes.call_llm_1.tool_calls}}",
			want: "call_llm_1",
		},
		{
			name: "bare expression with numbers",
			expr: "nodes.call_llm_1.tool_calls",
			want: "call_llm_1",
		},
		{
			name: "not a template",
			expr: "some_string",
			want: "",
		},
		{
			name: "wrong property (template)",
			expr: "{{nodes.call_llm.message}}",
			want: "",
		},
		{
			name: "wrong property (bare)",
			expr: "nodes.call_llm.message",
			want: "",
		},
		{
			name: "empty string",
			expr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolCallsSourceNode(tt.expr)
			if got != tt.want {
				t.Errorf("extractToolCallsSourceNode(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

func TestResolveSimpleInputExpr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		expr   string
		inputs map[string]interface{}
		want   string
	}{
		{
			name:   "resolves bare input expression",
			expr:   "inputs.response_tool_name",
			inputs: map[string]interface{}{"response_tool_name": "submit_review"},
			want:   "submit_review",
		},
		{
			name:   "resolves template-wrapped input expression",
			expr:   "{{inputs.response_tool_name}}",
			inputs: map[string]interface{}{"response_tool_name": "submit_review"},
			want:   "submit_review",
		},
		{
			name:   "returns empty for missing input",
			expr:   "inputs.missing_key",
			inputs: map[string]interface{}{"other_key": "value"},
			want:   "",
		},
		{
			name:   "returns empty for non-input expression",
			expr:   "nodes.call_llm.tool_calls",
			inputs: map[string]interface{}{"response_tool_name": "submit_review"},
			want:   "",
		},
		{
			name:   "returns empty for nil inputs",
			expr:   "inputs.response_tool_name",
			inputs: nil,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSimpleInputExpr(tt.expr, tt.inputs)
			if got != tt.want {
				t.Errorf("resolveSimpleInputExpr(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}
