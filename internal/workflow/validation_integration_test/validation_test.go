// Copyright (c) 2025 Reliant Labs
// Package validation_integration_test provides integration tests for workflow validation.
//
// This test suite verifies that validation produces correct results
// using proto-based workflow types.
//
// Tests are in a separate package to avoid import cycles.
package validation_integration_test

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// =============================================================================
// TEST CASE STRUCTURE
// =============================================================================

type validationTestCase struct {
	name           string
	yaml           string
	category       string
	expectValid    bool
	errorMust      []string          // error must contain ALL of these
	childWorkflows map[string]string // for cross-workflow tests
}

// =============================================================================
// VALIDATION TEST
// =============================================================================

// TestWorkflowValidation verifies that validation produces correct results.
func TestWorkflowValidation(t *testing.T) {
	testCases := getAllTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Build loaders
			loader := mockProtoLoader(tc.childWorkflows)

			// Parse YAML to proto
			wf, err := wfyaml.ParseWorkflow([]byte(tc.yaml))
			var valid bool
			var validationErrors []string
			if err != nil {
				valid = false
				validationErrors = []string{err.Error()}
			} else {
				result := validation.StaticAnalysis(wf, loader)
				if result.HasErrors() {
					valid = false
					for _, e := range result.Errors() {
						validationErrors = append(validationErrors, e.Error())
					}
				} else {
					valid = true
				}
			}

			// Verify against expected result
			if tc.expectValid {
				if !valid {
					t.Errorf("Expected valid workflow, got errors: %v", validationErrors)
				}
			} else {
				if valid {
					t.Errorf("Expected invalid workflow, but validation passed")
				} else {
					// Verify error contains expected strings
					errStr := strings.ToLower(strings.Join(validationErrors, " "))
					for _, must := range tc.errorMust {
						if !strings.Contains(errStr, strings.ToLower(must)) {
							t.Errorf("Error should contain %q, got: %v", must, validationErrors)
						}
					}
				}
			}
		})
	}
}

// =============================================================================
// BUILTIN WORKFLOWS TEST
// =============================================================================

// TestBuiltinWorkflowsValidate ensures all builtin workflows pass validation.
func TestBuiltinWorkflowsValidate(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		t.Fatalf("Failed to read builtin workflows: %v", err)
	}

	// Create loader for cross-workflow validation
	loader := func(name string) (*reliantv1.Workflow, error) {
		return parseProtoBuiltin(name)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
			if err != nil {
				t.Fatalf("Failed to read %s: %v", entry.Name(), err)
			}

			// Parse YAML to proto
			wf, parseErr := wfyaml.ParseWorkflow(data)
			if parseErr != nil {
				t.Fatalf("Parse error: %v", parseErr)
			}

			result := validation.StaticAnalysis(wf, loader)
			if result.HasErrors() {
				t.Errorf("Validation errors:\n%s", result.Error())
			}
		})
	}
}

// =============================================================================
// MOCK LOADERS
// =============================================================================

func mockProtoLoader(yamlMap map[string]string) validation.WorkflowLoader {
	return func(name string) (*reliantv1.Workflow, error) {
		yamlStr, ok := yamlMap[name]
		if !ok {
			return nil, fmt.Errorf("not found: %s", name)
		}
		wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
		if err != nil {
			return nil, fmt.Errorf("failed to parse child workflow %s: %w", name, err)
		}
		return wf, nil
	}
}

func parseProtoBuiltin(name string) (*reliantv1.Workflow, error) {
	// Handle builtin:// prefix
	name = strings.TrimPrefix(name, "builtin://")

	// Try top-level workflows first
	paths := []string{name, name + ".yaml"}
	for _, p := range paths {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(p)
		if err == nil {
			wf, parseErr := wfyaml.ParseWorkflow(data)
			if parseErr != nil {
				return nil, parseErr
			}
			return wf, nil
		}
	}

	// Try presets
	presetPaths := []string{"presets/" + name, "presets/" + name + ".yaml"}
	for _, p := range presetPaths {
		data, err := builtin.BuiltinPresetsFS.ReadFile(p)
		if err == nil {
			wf, parseErr := wfyaml.ParseWorkflow(data)
			if parseErr != nil {
				return nil, parseErr
			}
			return wf, nil
		}
	}

	return nil, fmt.Errorf("workflow not found: %s", name)
}

// =============================================================================
// TEST CASES
// =============================================================================

func getAllTestCases() []validationTestCase {
	var cases []validationTestCase
	cases = append(cases, getStructuralTestCases()...)
	cases = append(cases, getCELTestCases()...)
	cases = append(cases, getCrossWorkflowTestCases()...)
	cases = append(cases, getEdgeCaseTestCases()...)
	cases = append(cases, getNodeRoutingTestCases()...)
	return cases
}

// --- Node Routing Validation ---

func getNodeRoutingTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:     "node_routing/valid_basic",
			category: "node_routing",
			yaml: `
name: test
entry: [route, summarize, translate]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: summarize
        description: "Summarize content"
      - id: translate
        description: "Translate text"
  - id: summarize
    type: call_llm
    args:
      model: mock
  - id: translate
    type: call_llm
    args:
      model: mock
`,
			expectValid: true,
		},
		{
			name:     "node_routing/valid_with_fallback",
			category: "node_routing",
			yaml: `
name: test
entry: [route, summarize, translate]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: summarize
        description: "Summarize content"
      - id: translate
        description: "Translate text"
    fallback: summarize
  - id: summarize
    type: call_llm
    args:
      model: mock
  - id: translate
    type: call_llm
    args:
      model: mock
`,
			expectValid: true,
		},
		{
			name:     "node_routing/valid_with_conditional_edges",
			category: "node_routing",
			yaml: `
name: test
entry: [route]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: summarize
      - id: translate
  - id: summarize
    type: call_llm
    args:
      model: mock
  - id: translate
    type: call_llm
    args:
      model: mock
  - id: done
    type: save_message
    args:
      role: "assistant"
      content: "Done"
edges:
  - from: route
    cases:
      - to: summarize
        condition: "nodes.route.selected_node == 'summarize'"
    default: translate
  - from: summarize
    default: done
  - from: translate
    default: done
`,
			expectValid: true,
		},
		{
			name:     "node_routing/both_workflows_and_nodes_rejected",
			category: "node_routing",
			yaml: `
name: test
entry: [route, target]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    workflows:
      - ref: builtin://agent
        presets: [general]
    nodes:
      - id: target
  - id: target
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"cannot have both"},
		},
		{
			name:     "node_routing/candidate_references_unknown_node",
			category: "node_routing",
			yaml: `
name: test
entry: [route]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: nonexistent
        description: "Does not exist"
`,
			expectValid: false,
			errorMust:   []string{"unknown node"},
		},
		{
			name:     "node_routing/candidate_with_empty_id",
			category: "node_routing",
			yaml: `
name: test
entry: [route]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: ""
        description: "No ID"
`,
			expectValid: false,
			errorMust:   []string{"empty id"},
		},
		{
			name:     "node_routing/candidate_references_itself",
			category: "node_routing",
			yaml: `
name: test
entry: [route]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: route
        description: "Self-reference"
`,
			expectValid: false,
			errorMust:   []string{"itself"},
		},
		{
			name:     "node_routing/fallback_not_in_candidates",
			category: "node_routing",
			yaml: `
name: test
entry: [route, target_a, other]
nodes:
  - id: route
    type: router
    model:
      tags: [fast]
    nodes:
      - id: target_a
    fallback: other
  - id: target_a
    type: call_llm
    args:
      model: mock
  - id: other
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"fallback"},
		},
	}
}

// --- Structural Validation ---

func getStructuralTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:     "structural/valid_minimal",
			category: "structure",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
`,
			expectValid: true,
		},
		{
			name:     "structural/missing_name",
			category: "structure",
			yaml: `
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"name"},
		},
		{
			name:     "structural/missing_entry",
			category: "structure",
			yaml: `
name: test
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"entry"},
		},
		{
			name:     "structural/missing_nodes",
			category: "structure",
			yaml: `
name: test
entry: [step1]`,
			expectValid: false,
			errorMust:   []string{"node"},
		},
		{
			name:     "structural/entry_not_found",
			category: "structure",
			yaml: `
name: test
entry: [nonexistent]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"entry", "nonexistent"},
		},
		{
			name:     "structural/duplicate_node_ids",
			category: "structure",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
  - id: step1
    type: call_llm
    args:
      model: mock
`,
			expectValid: false,
			errorMust:   []string{"duplicate"},
		},
		{
			name:     "structural/edge_to_unknown_node",
			category: "structure",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
edges:
  - from: step1
    default: nonexistent
`,
			expectValid: false,
			errorMust:   []string{"nonexistent"},
		},
		{
			name:     "structural/output_reserved_underscore_prefix",
			category: "structure",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
outputs:
  _reserved_name: "{{nodes.step1.message.content}}"
`,
			expectValid: false,
			errorMust:   []string{"_reserved_name", "cannot start with '_'", "reserved"},
		},
		{
			name:     "structural/output_valid_name",
			category: "structure",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
outputs:
  result: "{{nodes.step1.message.content}}"
`,
			expectValid: true,
		},
		{
			name:     "structural/inline_output_reserved_underscore_prefix",
			category: "structure",
			yaml: `
name: test
entry: [loop1]
nodes:
  - id: loop1
    type: loop
    while: "iter.iteration < 5"
    inline:
      entry: [inner]
      outputs:
        _bad_output: "{{nodes.inner.message.content}}"
      nodes:
        - id: inner
          type: call_llm
          args:
            model: mock
`,
			expectValid: false,
			errorMust:   []string{"_bad_output", "cannot start with '_'", "reserved"},
		},
	}
}

// --- CEL Expression Validation ---

func getCELTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:     "cel/valid_inputs_reference",
			category: "cel",
			yaml: `
name: test
entry: [step1]
inputs:
  query:
    type: string
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
    system_prompt: "{{inputs.query}}"
`,
			expectValid: true,
		},
		{
			name:     "cel/valid_nodes_reference",
			category: "cel",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
  - id: step2
    type: call_llm
    args:
      model: mock
    system_prompt: "{{nodes.step1.message.text}}"
edges:
  - from: step1
    default: step2
`,
			expectValid: true,
		},
		{
			name:     "cel/unknown_input",
			category: "cel",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
      system_prompt: "{{inputs.nonexistent}}"
`,
			expectValid: false,
			errorMust:   []string{"nonexistent"},
		},
		{
			name:     "cel/unknown_node",
			category: "cel",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
      system_prompt: "{{nodes.nonexistent.message}}"
`,
			expectValid: false,
			errorMust:   []string{"nonexistent"},
		},
		{
			name:     "cel/typo_singular_input",
			category: "cel",
			yaml: `
name: test
entry: [step1]
inputs:
  query:
    type: string
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
      system_prompt: "{{input.query}}"
`,
			expectValid: false,
			errorMust:   []string{"input"},
		},
		{
			name:     "cel/valid_edge_condition",
			category: "cel",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
  - id: step2
    type: call_llm
    args:
      model: mock
edges:
  - from: step1
    cases:
      - to: step2
        condition: "nodes.step1.message.text != ''"
`,
			expectValid: true,
		},
	}
}

// --- Cross-Workflow Validation ---

func getCrossWorkflowTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:     "cross_workflow/valid_all_inputs_provided",
			category: "cross_workflow",
			yaml: `
name: parent
entry: [child_step]
nodes:
  - id: child_step
    type: workflow
    ref: child-workflow
    args:
      query: "test"
      message: "hello"
`,
			expectValid: true,
			childWorkflows: map[string]string{
				"child-workflow": `
name: child-workflow
entry: [step1]
inputs:
  query:
    type: string
  message:
    type: string
nodes:
  - id: step1
    type: call_llm
`,
			},
		},
		{
			name:     "cross_workflow/missing_required_input",
			category: "cross_workflow",
			yaml: `
name: parent
entry: [child_step]
nodes:
  - id: child_step
    type: workflow
    ref: child-workflow
    args:
      optionalField: "value"
`,
			expectValid: false,
			errorMust:   []string{"query"},
			childWorkflows: map[string]string{
				"child-workflow": `
name: child-workflow
entry: [step1]
inputs:
  query:
    type: string
  optionalField:
    type: string
    default: "default"
nodes:
  - id: step1
    type: call_llm
`,
			},
		},
		{
			name:     "cross_workflow/cel_expression_as_input",
			category: "cross_workflow",
			yaml: `
name: parent
entry: [child_step]
inputs:
  parent_query:
    type: string
nodes:
  - id: child_step
    type: workflow
    ref: child-workflow
    args:
      query: "{{inputs.parent_query}}"
`,
			expectValid: true,
			childWorkflows: map[string]string{
				"child-workflow": `
name: child-workflow
entry: [step1]
inputs:
  query:
    type: string
nodes:
  - id: step1
    type: call_llm
`,
			},
		},
	}
}

// --- Edge Cases ---

func getEdgeCaseTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:     "edge_case/self_reference_in_condition",
			category: "edge_case",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
edges:
  - from: step1
    cases:
      - to: step1
        condition: "nodes.step1.message.text == 'retry'"
`,
			expectValid: false, // Self-edges are currently detected as cycles by the validator
			errorMust:   []string{"cycle"},
		},
		{
			name:     "edge_case/conditional_with_default",
			category: "edge_case",
			yaml: `
name: test
entry: [router]
nodes:
  - id: router
    type: call_llm
    args:
      model: mock
  - id: path_a
    type: call_llm
    args:
      model: mock
  - id: path_b
    type: call_llm
    args:
      model: mock
edges:
  - from: router
    cases:
      - to: path_a
        condition: "nodes.router.message.text == 'a'"
    default: path_b
`,
			expectValid: true,
		},
		{
			name:     "edge_case/valid_inline_workflow",
			category: "edge_case",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: workflow
    inline:
      name: inner
      entry: [inner_step]
      nodes:
        - id: inner_step
          type: call_llm
          args:
            model: mock
`,
			expectValid: true,
		},
		{
			name:     "edge_case/tools_config_basic",
			category: "edge_case",
			yaml: `
name: test
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
      tools_config:
        filter: [view, grep, bash]
`,
			expectValid: true,
		},
	}
}
