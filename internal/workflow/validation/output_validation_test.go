// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// TestOutputTypeValidationDistinction verifies the distinction between:
// - ERROR: accessing unknown field on a KNOWN type (e.g., nodes.llm.unknown_field)
// - WARNING: accessing field on a DYN type (e.g., nodes.external_ref.field where external_ref is dyn)
func TestOutputTypeValidationDistinction(t *testing.T) {
	t.Parallel()
	t.Run("error on unknown field of known type", func(t *testing.T) {
		yaml := `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model:
        id: mock
outputs:
  data: "{{nodes.llm.unknown_field}}"
`
		// Parse workflow
		wf, err := wfyaml.ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("Failed to parse workflow: %v", err)
		}

		// Run validation
		result := StaticAnalysis(wf, nil)

		// Should have an ERROR (not a warning) because we're accessing an unknown field on CallLLMOutput
		if !result.HasErrors() {
			t.Errorf("Expected error for accessing unknown field on known type, but got none")
		}

		// Check that the error is about the field not existing
		foundFieldError := false
		for _, err := range result.Errors() {
			if strings.Contains(err.Error(), "unknown_field") {
				foundFieldError = true
				break
			}
		}
		if !foundFieldError {
			t.Errorf("Expected error about unknown_field, got: %v", result.Errors())
		}
	})

	t.Run("warning on dynamic type from external ref", func(t *testing.T) {
		yaml := `
name: test
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: external-workflow
outputs:
  result: "{{nodes.child.some_field}}"
`
		// Parse workflow
		wf, err := wfyaml.ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("Failed to parse workflow: %v", err)
		}

		// Run validation (no loader, so external ref is unknown)
		result := StaticAnalysis(wf, nil)

		// Should have a WARNING (not error) because nodes.child itself is dyn type
		hasWarning := false
		for _, warn := range result.Warnings() {
			if strings.Contains(warn.Error(), "dynamic type") && strings.Contains(warn.Error(), "result") {
				hasWarning = true
				break
			}
		}
		if !hasWarning {
			t.Errorf("Expected warning about dynamic type for external workflow output, got warnings: %v", result.Warnings())
		}
	})
}

// TestOutputTypeValidation verifies that output expressions are type-checked.
func TestOutputTypeValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		yaml           string
		expectWarning  bool
		warningMessage string
	}{
		{
			name: "valid string output",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  result: "{{nodes.llm.message.text}}"
`,
			expectWarning: false,
		},
		{
			name: "valid integer output",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
  - id: tools
    type: execute_tools
    tool_calls: "{{nodes.llm.tool_calls}}"
edges:
  - from: llm
    default: tools
outputs:
  count: "{{size(nodes.llm.tool_calls)}}"
`,
			expectWarning: false,
		},
		{
			name: "valid boolean output",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  stopped: "{{nodes.llm.stop_reason == 'end_turn'}}"
`,
			expectWarning: false,
		},
		{
			name: "dynamic type from external workflow ref",
			yaml: `
name: test
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: external-workflow
outputs:
  result: "{{nodes.child.some_field}}"
`,
			expectWarning:  true,
			warningMessage: "dynamic type (dyn)",
		},
		{
			name: "dynamic type warning - dyn expression",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  raw: "{{nodes.llm}}"
`,
			expectWarning: false, // The whole node object should be typed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse workflow
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Failed to parse workflow: %v", err)
			}

			// Run validation
			result := StaticAnalysis(wf, nil)

			// Check for warnings
			hasWarning := false
			var warningMsg string
			for _, err := range result.Warnings() {
				if strings.Contains(err.Error(), "outputs") {
					hasWarning = true
					warningMsg = err.Error()
					break
				}
			}

			if tt.expectWarning && !hasWarning {
				t.Errorf("Expected warning about dynamic type, but got none.\nAll warnings: %v", result.Warnings())
			}

			if !tt.expectWarning && hasWarning {
				t.Errorf("Unexpected warning: %s", warningMsg)
			}

			if tt.expectWarning && hasWarning && tt.warningMessage != "" {
				if !strings.Contains(warningMsg, tt.warningMessage) {
					t.Errorf("Warning message doesn't contain expected text.\nExpected substring: %s\nActual: %s",
						tt.warningMessage, warningMsg)
				}
			}
		})
	}
}

// TestOutputTypeInference verifies that output types are correctly inferred.
func TestOutputTypeInference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		yaml          string
		outputName    string
		expectDynamic bool
	}{
		{
			name: "string type from message.text",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  result: "{{nodes.llm.message.text}}"
`,
			outputName:    "result",
			expectDynamic: false,
		},
		{
			name: "int type from size function",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  count: "{{size(nodes.llm.message.text)}}"
`,
			outputName:    "count",
			expectDynamic: false,
		},
		{
			name: "bool type from comparison",
			yaml: `
name: test
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: mock
outputs:
  is_done: "{{nodes.llm.stop_reason == 'end_turn'}}"
`,
			outputName:    "is_done",
			expectDynamic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse workflow
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Failed to parse workflow: %v", err)
			}

			// Run validation
			result := StaticAnalysis(wf, nil)

			// The inferred types should be stored, but we primarily care about warnings
			if tt.expectDynamic {
				// Should have a warning
				hasWarning := false
				for _, warn := range result.Warnings() {
					if strings.Contains(warn.Error(), tt.outputName) && strings.Contains(warn.Error(), "dynamic") {
						hasWarning = true
						break
					}
				}
				if !hasWarning {
					t.Errorf("Expected warning for dynamic type on output '%s'", tt.outputName)
				}
			} else {
				// Should NOT have a warning about dynamic type
				for _, warn := range result.Warnings() {
					if strings.Contains(warn.Error(), tt.outputName) && strings.Contains(warn.Error(), "dynamic") {
						t.Errorf("Unexpected warning for output '%s': %v", tt.outputName, warn)
					}
				}
			}
		})
	}
}
