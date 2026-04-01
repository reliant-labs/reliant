// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"regexp"
	"testing"
)

// llmToolNamePattern is the regex that LLM APIs (Anthropic, OpenAI, etc.)
// require tool names to match. Colons, dots, spaces, and other special
// characters are rejected by these APIs.
var llmToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestValidateResponseToolData(t *testing.T) {
	tests := []struct {
		name      string
		jsonStr   string
		schema    map[string]interface{}
		wantError bool
	}{
		{
			name:    "valid data with all required fields",
			jsonStr: `{"verdict": "approved", "summary": "Looks good"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"approved", "needs_revision"},
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
				},
			},
			wantError: false,
		},
		{
			name:    "missing required field - summary",
			jsonStr: `{"verdict": "approved"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
				},
			},
			wantError: true,
		},
		{
			name:    "missing required field - verdict",
			jsonStr: `{"summary": "The plan is incomplete"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
				},
			},
			wantError: true,
		},
		{
			name:    "invalid enum value",
			jsonStr: `{"verdict": "invalid_value", "summary": "test"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"approved", "needs_revision"},
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
				},
			},
			wantError: true,
		},
		{
			name:    "with optional fields",
			jsonStr: `{"verdict": "needs_revision", "summary": "Has issues", "concerns": ["Missing error handling"], "confidence": 8}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"approved", "needs_revision"},
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
					"concerns": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"confidence": map[string]interface{}{
						"type":    "integer",
						"minimum": float64(1),
						"maximum": float64(10),
					},
				},
			},
			wantError: false,
		},
		{
			name:    "confidence out of range",
			jsonStr: `{"verdict": "approved", "summary": "Good", "confidence": 15}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{
						"type": "string",
					},
					"summary": map[string]interface{}{
						"type": "string",
					},
					"confidence": map[string]interface{}{
						"type":    "integer",
						"minimum": float64(1),
						"maximum": float64(10),
					},
				},
			},
			wantError: true,
		},
		{
			name:      "nil schema passes (no validation)",
			jsonStr:   `{"anything": "goes"}`,
			schema:    nil,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip validation if no schema
			if tt.schema == nil {
				return
			}

			err := validateResponseToolData(tt.jsonStr, tt.schema)
			if (err != nil) != tt.wantError {
				t.Errorf("validateResponseToolData() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestExecuteResponseToolInline_WithValidation(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		toolInput  string
		schema     map[string]interface{}
		wantError  bool
		wantInResp string // substring to check in response content
	}{
		{
			name:      "valid response passes validation",
			toolName:  "plan_review",
			toolInput: `{"verdict": "approved", "summary": "Plan is good"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{"type": "string"},
					"summary": map[string]interface{}{"type": "string"},
				},
			},
			wantError:  false,
			wantInResp: "approved",
		},
		{
			name:      "missing required field fails validation",
			toolName:  "plan_review",
			toolInput: `{"verdict": "approved"}`,
			schema: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"verdict", "summary"},
				"properties": map[string]interface{}{
					"verdict": map[string]interface{}{"type": "string"},
					"summary": map[string]interface{}{"type": "string"},
				},
			},
			wantError:  true,
			wantInResp: "schema validation failed",
		},
		{
			name:       "no schema skips validation",
			toolName:   "plan_review",
			toolInput:  `{"verdict": "approved"}`,
			schema:     nil,
			wantError:  false,
			wantInResp: "approved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executeResponseToolInline("test-call-id", tt.toolName, tt.toolInput, tt.schema)

			if result.IsError != tt.wantError {
				t.Errorf("executeResponseToolInline() IsError = %v, want %v, content: %s", result.IsError, tt.wantError, result.Content)
			}

			if tt.wantInResp != "" && !containsString(result.Content, tt.wantInResp) {
				t.Errorf("executeResponseToolInline() content = %q, want to contain %q", result.Content, tt.wantInResp)
			}

			// Verify tool name is preserved
			if result.Name != tt.toolName {
				t.Errorf("executeResponseToolInline() Name = %q, want %q", result.Name, tt.toolName)
			}
		})
	}
}

// TestResponseToolName_MatchesLLMAPIPattern verifies that response tool names
// conform to the pattern required by LLM APIs. This is a regression test for
// the __response__: prefix bug where colons leaked into tool names.
func TestResponseToolName_MatchesLLMAPIPattern(t *testing.T) {
	validNames := []string{
		"submit_verdict",
		"plan_review",
		"filtered_results",
		"code-review",
		"analysis123",
	}
	for _, name := range validNames {
		if !llmToolNamePattern.MatchString(name) {
			t.Errorf("response tool name %q does not match LLM API pattern %s", name, llmToolNamePattern.String())
		}
	}

	invalidNames := []string{
		"__response__:submit_verdict",
		"tool.with.dots",
		"tool with spaces",
		"tool:with:colons",
		"",
	}
	for _, name := range invalidNames {
		if llmToolNamePattern.MatchString(name) {
			t.Errorf("expected response tool name %q to be rejected by LLM API pattern, but it matched", name)
		}
	}
}

// TestExecuteResponseToolInline_PreservesValidToolName verifies that
// executeResponseToolInline does not modify or prefix the tool name.
// Regression test for the __response__: prefix bug.
func TestExecuteResponseToolInline_PreservesValidToolName(t *testing.T) {
	toolNames := []string{"submit_verdict", "plan_review", "code-review"}
	for _, name := range toolNames {
		result := executeResponseToolInline("test-id", name, `{"key": "value"}`, nil)
		if result.Name != name {
			t.Errorf("executeResponseToolInline modified tool name: got %q, want %q", result.Name, name)
		}
		if !llmToolNamePattern.MatchString(result.Name) {
			t.Errorf("tool name in result %q does not match LLM API pattern", result.Name)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
