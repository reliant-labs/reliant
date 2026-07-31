// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
)

// OpenAI Responses API is significantly stricter than ChatCompletions about JSON Schema.
// These tests let us iterate quickly without running full workflows.
//
// Current known constraints (based on API errors):
// - Every object schema must explicitly set: additionalProperties=false
// - required must not name a key that is absent from properties
func TestExtractUpstreamCorrelationHeaders(t *testing.T) {
	t.Run("returns empty values when response is nil", func(t *testing.T) {
		requestID, proxymanID := extractUpstreamCorrelationHeaders(nil)
		if requestID != "" || proxymanID != "" {
			t.Fatalf("expected empty correlation IDs for nil response, got requestID=%q proxymanID=%q", requestID, proxymanID)
		}
	})

	t.Run("reads correlation headers and trims whitespace", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("x-oai-request-id", "  req_123  ")
		resp.Header.Set("x-proxyman-id", "  flow_456  ")

		requestID, proxymanID := extractUpstreamCorrelationHeaders(resp)
		if requestID != "req_123" {
			t.Fatalf("requestID = %q, want req_123", requestID)
		}
		if proxymanID != "flow_456" {
			t.Fatalf("proxymanID = %q, want flow_456", proxymanID)
		}
	})
}

func TestResponsesToolSchemaNormalization(t *testing.T) {
	registry := models.MustGetRegistry()
	def, ok := registry.GetDefinition(string(models.GPT54Mini))
	if !ok {
		t.Fatalf("missing model %s", models.GPT54Mini)
	}

	client := NewClient(llm.DriverOptions{Model: def.ToModel()})

	// We only need one tool to reproduce the strictest schema issues.
	repo := &db.Repo{} // schema generation doesn't touch the repo
	addTask := llmtools.NewAddTaskTool(repo)
	updateTask := llmtools.NewUpdateTaskTool(repo)
	// Simulate a malformed/underspecified MCP schema that still must become
	// a strict root object for OpenAI Responses.
	brokenMCP := llmtools.NewSchemaOnlyTool(
		"mcp__docker__list_containers",
		"List all Docker containers [via MCP:docker]",
		map[string]any{},
	)

	respTools := client.convertToolsToResponsesTools([]llmtools.Tool{addTask, updateTask, brokenMCP})
	if len(respTools) != 3 {
		t.Fatalf("expected 3 function tools")
	}
	for i, rt := range respTools {
		if rt.OfFunction == nil {
			t.Fatalf("expected function tool at index %d", i)
		}
		params := rt.OfFunction.Parameters
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		assertObjectSchemaValid(t, m, "parameters["+itoa(i)+"]")
	}

	// Explicit regression assertion for the MCP-style fallback schema path.
	// This mirrors the production failure where OpenAI rejected a root schema
	// without additionalProperties=false.
	last := respTools[len(respTools)-1].OfFunction
	b, err := json.Marshal(last.Parameters)
	if err != nil {
		t.Fatalf("marshal last params: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal last params: %v", err)
	}
	if m["type"] != "object" {
		t.Fatalf("expected fallback MCP root type=object, got %v", m["type"])
	}
	if m["additionalProperties"] != false {
		t.Fatalf("expected fallback MCP additionalProperties=false, got %#v", m["additionalProperties"])
	}
}

func assertObjectSchemaValid(t *testing.T, schema map[string]any, path string) {
	t.Helper()

	// If this looks like an object schema, enforce the object invariants.
	if schema["type"] == "object" || schema["properties"] != nil {
		ap, ok := schema["additionalProperties"]
		if !ok {
			t.Fatalf("%s: missing additionalProperties", path)
		}
		if ap != false {
			t.Fatalf("%s: additionalProperties must be false, got %#v", path, ap)
		}

		props, _ := schema["properties"].(map[string]any)
		if props != nil {
			// required is a subset of properties: OpenAI Responses rejects an
			// entry naming a property that does not exist, but a tool is free to
			// declare parameters optional (and must be, or the model can never
			// omit one).
			var names []string
			switch r := schema["required"].(type) {
			case []any:
				for _, v := range r {
					if s, ok := v.(string); ok {
						names = append(names, s)
					}
				}
			case []string:
				names = append(names, r...)
			}

			for _, name := range names {
				if _, ok := props[name]; !ok {
					t.Fatalf("%s: required names %q which is not a property", path, name)
				}
			}
		}
	}

	// Recurse into properties
	if props, ok := schema["properties"].(map[string]any); ok {
		for k, v := range props {
			if child, ok := v.(map[string]any); ok {
				assertObjectSchemaValid(t, child, path+".properties."+k)
			}
		}
	}

	// Recurse into array items
	if items, ok := schema["items"].(map[string]any); ok {
		assertObjectSchemaValid(t, items, path+".items")
	}

	// Recurse into combinators
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for i, v := range arr {
				if child, ok := v.(map[string]any); ok {
					assertObjectSchemaValid(t, child, path+"."+key+"["+itoa(i)+"]")
				}
			}
		}
	}
}

func TestResponsesToolSchemaNormalization_FallsBackForTupleItemsMCPSchema(t *testing.T) {
	registry := models.MustGetRegistry()
	def, ok := registry.GetDefinition(string(models.GPT54Mini))
	if !ok {
		t.Fatalf("missing model %s", models.GPT54Mini)
	}

	client := NewClient(llm.DriverOptions{Model: def.ToModel()})

	brokenMCP := llmtools.NewSchemaOnlyTool(
		"mcp__statsig__Update_Segment",
		"Update a Statsig segment [via MCP:statsig]",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"params": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"application/json": map[string]any{
							"anyOf": []any{
								map[string]any{
									"type":  "array",
									"items": []any{map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
			},
		},
	)

	respTools := client.convertToolsToResponsesTools([]llmtools.Tool{brokenMCP})
	if len(respTools) != 1 || respTools[0].OfFunction == nil {
		t.Fatalf("expected single function tool response")
	}

	b, err := json.Marshal(respTools[0].OfFunction.Parameters)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}

	if m["type"] != "object" {
		t.Fatalf("expected fallback root type=object, got %v", m["type"])
	}
	if m["additionalProperties"] != false {
		t.Fatalf("expected fallback additionalProperties=false, got %#v", m["additionalProperties"])
	}
	props, _ := m["properties"].(map[string]any)
	if len(props) != 0 {
		t.Fatalf("expected fallback empty properties, got %#v", props)
	}
}

func itoa(i int) string {
	// tiny helper to avoid importing strconv in a test
	if i == 0 {
		return "0"
	}
	b := []byte{}
	for i > 0 {
		b = append([]byte{byte('0' + (i % 10))}, b...)
		i /= 10
	}
	return string(b)
}
