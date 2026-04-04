// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"os"
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

// TestCELAdversarial_FalsePositives tests valid expressions that might incorrectly fail validation.
// These are expressions that SHOULD pass but might trigger false alarms.
func TestCELAdversarial_FalsePositives(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		desc string
	}{
		{
			name: "null check for conditional node with ternary",
			yaml: `
name: test
entry: [llm1]
inputs:
  enabled:
    type: boolean
    default: true
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: llm2
    type: call_llm
    condition: "inputs.enabled"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm2 != null ? nodes.llm2.response_text : 'default'}}"
edges:
  - from: llm1
    to: llm2
  - from: llm2
    to: test
`,
			desc: "null check should protect conditional node access",
		},
		{
			name: "optional chaining for conditional node",
			yaml: `
name: test
entry: [llm1]
inputs:
  enabled:
    type: boolean
    default: true
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: llm2
    type: call_llm
    condition: "inputs.enabled"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.?llm2.response_text}}"
edges:
  - from: llm1
    to: llm2
  - from: llm2
    to: test
`,
			desc: "optional chaining should be valid",
		},
		{
			name: "null check for conditional node",
			yaml: `
name: test
entry: [llm1]
inputs:
  enabled:
    type: boolean
    default: true
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: llm2
    type: call_llm
    condition: "inputs.enabled"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm2 != null ? nodes.llm2.response_text : 'default'}}"
edges:
  - from: llm1
    to: llm2
  - from: llm2
    to: test
`,
			desc: "null check should protect conditional node access",
		},
		{
			name: "valid nested field access",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm1.message.id}}"
edges:
  - from: llm1
    to: test
`,
			desc: "nested field access on node output should work",
		},

		{
			name: "CEL cross-type numeric comparison int to double",
			yaml: `
name: test
entry: [test]
inputs:
  count:
    type: integer
    default: 10
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.count > 5.5}}"
`,
			desc: "CEL should allow int > double comparison with CrossTypeNumericComparisons enabled",
		},
		{
			name: "in operator with list",
			yaml: `
name: test
entry: [test]
inputs:
  tags:
    type: array
    default: ["foo", "bar"]
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{'foo' in inputs.tags}}"
`,
			desc: "in operator should work with arrays",
		},
		{
			name: "size() on string",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{size(inputs.name) > 0}}"
`,
			desc: "size() should work on strings",
		},
		{
			name: "size() on list",
			yaml: `
name: test
entry: [test]
inputs:
  tags:
    type: array
    default: []
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{size(inputs.tags) > 0}}"
`,
			desc: "size() should work on arrays",
		},
		{
			name: "nested object input access",
			yaml: `
name: test
entry: [test]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
        default: 30
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.config.timeout}}"
`,
			desc: "nested object input access should work",
		},
		{
			name: "complex boolean logic",
			yaml: `
name: test
entry: [test]
inputs:
  enabled:
    type: boolean
    default: true
  count:
    type: integer
    default: 5
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.enabled && (inputs.count > 5 || size(inputs.name) > 0)}}"
`,
			desc: "complex boolean expressions should work",
		},
		{
			name: "string contains()",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "testfile"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.name.contains('test')}}"
`,
			desc: "string.contains() should work",
		},
		{
			name: "string startsWith()",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "prefix_test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.name.startsWith('prefix')}}"
`,
			desc: "string.startsWith() should work",
		},
		{
			name: "list indexing",
			yaml: `
name: test
entry: [test]
inputs:
  tags:
    type: array
    default: ["a", "b", "c"]
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.tags[0]}}"
`,
			desc: "list indexing should work",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			// Check for errors
			errors := result.Errors()
			if len(errors) > 0 {
				t.Errorf("FALSE POSITIVE: %s", tt.desc)
				t.Errorf("  Got %d errors:", len(errors))
				for _, e := range errors {
					t.Errorf("    - [%s] %s", e.Category, e.Message)
				}
			} else {
				t.Logf("✓ %s", tt.desc)
			}

			// Warnings are acceptable for conditional access (unless protected)
			warnings := result.Warnings()
			if len(warnings) > 0 {
				t.Logf("  Got %d warnings:", len(warnings))
				for _, w := range warnings {
					t.Logf("    - [%s] %s", w.Category, w.Message)
				}
			}
		})
	}
}

// TestCELAdversarial_FalseNegatives tests invalid expressions that might incorrectly pass validation.
// These are expressions that SHOULD fail but might slip through.
func TestCELAdversarial_FalseNegatives(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		desc          string
		shouldContain string // Expected error substring
	}{
		{
			name: "typo in node field",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm1.respons_text}}"
edges:
  - from: llm1
    to: test
`,
			desc:          "typo in field name should error",
			shouldContain: "respons_text",
		},
		{
			name: "non-existent node",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.nonexistent.response_text}}"
edges:
  - from: llm1
    to: test
`,
			desc:          "reference to non-existent node should error",
			shouldContain: "nonexistent",
		},
		{
			name: "typo in input name",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.nmae}}"
`,
			desc:          "typo in input name should error",
			shouldContain: "nmae",
		},
		{
			name: "string + int type mismatch",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.name + 5}}"
`,
			desc:          "string + int should error",
			shouldContain: "overload",
		},
		{
			name: "bool in arithmetic",
			yaml: `
name: test
entry: [test]
inputs:
  enabled:
    type: boolean
    default: true
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.enabled + 1}}"
`,
			desc:          "bool + int should error",
			shouldContain: "overload",
		},
		{
			name: "field access on int",
			yaml: `
name: test
entry: [test]
inputs:
  count:
    type: integer
    default: 10
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.count.length}}"
`,
			desc:          "accessing .length on int should error",
			shouldContain: "does not support field selection",
		},
		{
			name: "invalid method on string",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.name.push('x')}}"
`,
			desc:          "calling .push() on string should error",
			shouldContain: "push",
		},
		{
			name: "comparing string with bool",
			yaml: `
name: test
entry: [test]
inputs:
  name:
    type: string
    default: "test"
  enabled:
    type: boolean
    default: true
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.name == inputs.enabled}}"
`,
			desc:          "comparing string with bool should error",
			shouldContain: "overload",
		},
		{
			name: "accessing wrong node output field",
			yaml: `
name: test
entry: [cmd1]
nodes:
  - id: cmd1
    type: run
    args:
      command: "echo test"
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.cmd1.response_text}}"
edges:
  - from: cmd1
    to: test
`,
			desc:          "RunNode doesn't have response_text field",
			shouldContain: "response_text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if len(errors) == 0 {
				t.Errorf("FALSE NEGATIVE: %s", tt.desc)
				t.Errorf("  Expected error but validation passed")
			} else {
				// Check that error contains expected substring
				found := false
				for _, e := range errors {
					if tt.shouldContain != "" && containsIgnoreCase(e.Message, tt.shouldContain) {
						found = true
						break
					}
				}
				if !found && tt.shouldContain != "" {
					t.Errorf("Error doesn't contain expected substring '%s'", tt.shouldContain)
					t.Errorf("Got errors:")
					for _, e := range errors {
						t.Errorf("  - %s", e.Message)
					}
				} else {
					t.Logf("✓ Correctly caught error: %s", errors[0].Message)
				}
			}
		})
	}
}

// TestCELAdversarial_TypeSystemEdgeCases tests edge cases in the type system.
func TestCELAdversarial_TypeSystemEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		shouldError bool
		desc        string
	}{
		{
			name: "object addition",
			yaml: `
name: test
entry: [test]
inputs:
  obj:
    type: object
    default: {}
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.obj + inputs.obj}}"
`,
			shouldError: false, // CEL allows + on dyn types (may fail at runtime)
			desc:        "adding objects is allowed on dyn types",
		},
		{
			name: "object comparison",
			yaml: `
name: test
entry: [test]
inputs:
  obj:
    type: object
    default: {}
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.obj > inputs.obj}}"
`,
			shouldError: false, // CEL allows > on dyn types (may fail at runtime)
			desc:        "comparing objects with > is allowed on dyn types",
		},
		{
			name: "list concatenation",
			yaml: `
name: test
entry: [test]
inputs:
  list:
    type: array
    default: []
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.list + inputs.list}}"
`,
			shouldError: false, // CEL allows list concatenation with +
			desc:        "list concatenation is valid in CEL",
		},
		{
			name: "map field access with additionalProperties",
			yaml: `
name: test
entry: [test]
inputs:
  map:
    type: object
    additionalProperties: true
    default: {}
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.map.any_field}}"
`,
			shouldError: false,
			desc:        "map with additionalProperties allows any field",
		},
		{
			name: "calling method on null",
			yaml: `
name: test
entry: [test]
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{null.toString()}}"
`,
			shouldError: true,
			desc:        "calling method on null should error",
		},
		{
			name: "division by zero (compile time)",
			yaml: `
name: test
entry: [test]
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{5 / 0}}"
`,
			shouldError: false, // Division by zero is runtime error, not compile-time
			desc:        "division by zero is runtime error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()

			if tt.shouldError && len(errors) == 0 {
				t.Errorf("Expected error but got none: %s", tt.desc)
			} else if !tt.shouldError && len(errors) > 0 {
				t.Errorf("Expected no error but got: %s", tt.desc)
				for _, e := range errors {
					t.Errorf("  - [%s] %s", e.Category, e.Message)
				}
			} else {
				t.Logf("✓ %s", tt.desc)
			}
		})
	}
}

// TestCELAdversarial_NullHandling tests null handling edge cases.
func TestCELAdversarial_NullHandling(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		shouldError bool
		shouldWarn  bool
		desc        string
	}{
		{
			name: "null coalescing with ??",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    condition: "false"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm1.response_text ?? 'default'}}"
edges:
  - from: llm1
    to: test
`,
			shouldError: true, // CEL does not support ?? operator
			shouldWarn:  false,
			desc:        "null coalescing with ?? is not valid CEL syntax",
		},
		{
			name: "explicit null check",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    condition: "false"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm1 == null ? 'none' : nodes.llm1.response_text}}"
edges:
  - from: llm1
    to: test
`,
			shouldError: false,
			shouldWarn:  false, // Explicit null check
			desc:        "explicit null check should be safe",
		},
		{
			name: "unsafe conditional node access",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    condition: "false"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.llm1.response_text}}"
edges:
  - from: llm1
    to: test
`,
			shouldError: false,
			shouldWarn:  false, // Note: conditional node warnings are tracked separately by validateCEL, not ValidateCELWithCompilation
			desc:        "access to conditional node (warning handled by different code path)",
		},
		{
			name: "optional chaining protects null",
			yaml: `
name: test
entry: [llm1]
nodes:
  - id: llm1
    type: call_llm
    condition: "false"
    args:
      model: {tags: [flagship]}
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{nodes.?llm1.response_text}}"
edges:
  - from: llm1
    to: test
`,
			shouldError: false,
			shouldWarn:  false, // Optional chaining is safe
			desc:        "optional chaining should be safe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			warnings := result.Warnings()

			hasError := len(errors) > 0
			hasWarning := len(warnings) > 0

			if tt.shouldError != hasError {
				if tt.shouldError {
					t.Errorf("Expected error but got none: %s", tt.desc)
				} else {
					t.Errorf("Expected no error but got: %s", tt.desc)
					for _, e := range errors {
						t.Errorf("  - [%s] %s", e.Category, e.Message)
					}
				}
			}

			if tt.shouldWarn != hasWarning {
				if tt.shouldWarn {
					t.Errorf("Expected warning but got none: %s", tt.desc)
				} else {
					t.Errorf("Expected no warning but got: %s", tt.desc)
					for _, w := range warnings {
						t.Errorf("  - [%s] %s", w.Category, w.Message)
					}
				}
			}

			if !tt.shouldError && (tt.shouldWarn == hasWarning) {
				t.Logf("✓ %s", tt.desc)
			}
		})
	}
}

// TestCELAdversarial_ComplexNesting tests deeply nested object access.
func TestCELAdversarial_ComplexNesting(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		shouldError bool
		desc        string
	}{
		{
			name: "valid 3-level nesting",
			yaml: `
name: test
entry: [test]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          connection:
            type: object
            properties:
              timeout:
                type: integer
                default: 30
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.config.database.connection.timeout}}"
`,
			shouldError: false,
			desc:        "deep nesting should work",
		},
		{
			name: "typo in middle level",
			yaml: `
name: test
entry: [test]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          connection:
            type: object
            properties:
              timeout:
                type: integer
                default: 30
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.config.databas.connection.timeout}}"
`,
			shouldError: true,
			desc:        "typo in nested level should error",
		},
		{
			name: "typo in deepest level",
			yaml: `
name: test
entry: [test]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          connection:
            type: object
            properties:
              timeout:
                type: integer
                default: 30
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.config.database.connection.timout}}"
`,
			shouldError: true,
			desc:        "typo in deepest field should error",
		},
		{
			name: "accessing undefined nested field",
			yaml: `
name: test
entry: [test]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          connection:
            type: object
            properties:
              timeout:
                type: integer
                default: 30
nodes:
  - id: test
    type: call_llm
    args:
      model: {tags: [flagship]}
      messages:
        - role: user
          content: "{{inputs.config.database.connection.retries}}"
`,
			shouldError: true,
			desc:        "undefined nested field should error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()

			if tt.shouldError && len(errors) == 0 {
				t.Errorf("Expected error but got none: %s", tt.desc)
			} else if !tt.shouldError && len(errors) > 0 {
				t.Errorf("Expected no error but got: %s", tt.desc)
				for _, e := range errors {
					t.Errorf("  - [%s] %s", e.Category, e.Message)
				}
			} else {
				t.Logf("✓ %s", tt.desc)
			}
		})
	}
}

// TestCELAdversarial_BuiltinWorkflows tests real builtin workflows for validation issues.
func TestCELAdversarial_BuiltinWorkflows(t *testing.T) {
	builtinDir := "../../../workflow/builtin"
	workflows := []string{
		"agent.yaml",
		"one-ring.yaml",
		"auditing-agent.yaml",
		"parallel-compete.yaml",
		"parallel-loop-sample.yaml",
		"structured-agent.yaml",
	}

	for _, filename := range workflows {
		t.Run(filename, func(t *testing.T) {
			// Load workflow from YAML
			data, err := os.ReadFile(fmt.Sprintf("%s/%s", builtinDir, filename))
			if err != nil {
				t.Skipf("Could not read workflow: %v", err)
				return
			}
			wf, parseErr := wfyaml.ParseWorkflow(data)
			if parseErr != nil {
				t.Skipf("Could not parse workflow: %v", parseErr)
				return
			}

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			warnings := result.Warnings()

			if len(errors) > 0 {
				t.Errorf("Found %d errors in builtin workflow %s:", len(errors), filename)
				for _, e := range errors {
					t.Errorf("  [%s] %s: %s", e.Category, strings.Join(e.Path, "."), e.Message)
				}
			}

			if len(warnings) > 0 {
				t.Logf("Found %d warnings in builtin workflow %s:", len(warnings), filename)
				for _, w := range warnings {
					t.Logf("  [%s] %s: %s", w.Category, strings.Join(w.Path, "."), w.Message)
				}
			}

			if len(errors) == 0 {
				t.Logf("✓ No errors in %s (%d warnings)", filename, len(warnings))
			}
		})
	}
}

// Helper function to check if a string contains a substring (case-insensitive)
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
