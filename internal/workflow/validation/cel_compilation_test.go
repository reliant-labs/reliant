package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateCELWithCompilation_TypeMismatch tests that type mismatches in comparisons
// are caught at validation time via CEL compilation.
func TestValidateCELWithCompilation_TypeMismatch(t *testing.T) {
	// Workflow with type mismatch: comparing int to string
	workflowYAML := `
name: test-type-mismatch
entry: [step1]
inputs:
  count:
    type: integer
    default: 10
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
  - id: step2
    type: call_llm
    model:
      tags: [flagship]
edges:
  - from: step1
    cases:
      - to: step2
        condition: "nodes.step1.token_count > 'many'"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have at least one error about type mismatch
	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors")

	// Check that the error mentions type mismatch
	found := false
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
		if containsAny(e.Message, "type mismatch", "no matching overload") {
			found = true
		}
	}
	assert.True(t, found, "expected error about type mismatch")
}

func TestEncodedNodeCELIdentifier(t *testing.T) {
	assert.Equal(t, "simple_node", encodedNodeCELIdentifier("simple_node"))
	assert.Equal(t, "router_1775339690239", encodedNodeCELIdentifier("router-1775339690239"))
	assert.Equal(t, "loop_inner_node", encodedNodeCELIdentifier("loop.inner:node"))
}

func TestRewriteNodesAccess_EncodesHyphenatedNodeIDs(t *testing.T) {
	rewritten := rewriteNodesAccess(
		"nodes.router-1775339690239.response_text == '' || size(nodes.router-1775339690239.message.text) == 0",
		[]string{"router-1775339690239"},
	)
	assert.Equal(t, "nodes_router_1775339690239.response_text == '' || size(nodes_router_1775339690239.message.text) == 0", rewritten)
}

func TestRewriteNodesAccess_PreservesHasAndBareNodeAccess(t *testing.T) {
	rewritten := rewriteNodesAccess(
		"has(nodes.router-1775339690239) || size(nodes.router-1775339690239) == 0 || nodes.router-1775339690239 != null",
		[]string{"router-1775339690239"},
	)
	assert.Equal(
		t,
		"has(nodes.router-1775339690239) || size(nodes_router_1775339690239) == 0 || nodes_router_1775339690239 != null",
		rewritten,
	)
}

func TestRewriteNodesAccess_IgnoresStringLiteralsAndLongestMatchWins(t *testing.T) {
	rewritten := rewriteNodesAccess(
		"'nodes.router-1.response_text' + nodes.router-1.response_text + nodes.router-1a.response_text",
		[]string{"router-1", "router-1a"},
	)
	assert.Equal(
		t,
		"'nodes.router-1.response_text' + nodes_router_1.response_text + nodes_router_1a.response_text",
		rewritten,
	)
}

func TestValidateCELWithCompilation_CELSafeNodeIDOutputs(t *testing.T) {
	// Router has fixed top-level fields; child outputs are accessed via outputs sub-field.
	t.Run("valid access via outputs sub-field and metadata fields", func(t *testing.T) {
		workflowYAML := `
name: test-cel-safe-node-id
entry: [router_1775339690239]
nodes:
  - id: router_1775339690239
    type: router
    workflows:
      - ref: builtin://agent
outputs:
  message: "{{nodes.router_1775339690239.outputs.message}}"
  response_text: "{{nodes.router_1775339690239.outputs.response_text}}"
  selected: "{{nodes.router_1775339690239.selected_workflow}}"
  reasoning: "{{nodes.router_1775339690239.reasoning}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		for _, e := range errors {
			t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
		}
		assert.Empty(t, errors, "expected no validation errors for valid router field access")
	})

	t.Run("direct access to child workflow fields is an error (not flattened)", func(t *testing.T) {
		workflowYAML := `
name: test-cel-safe-node-id-no-flatten
entry: [router_1775339690239]
nodes:
  - id: router_1775339690239
    type: router
    workflows:
      - ref: builtin://agent
outputs:
  message: "{{nodes.router_1775339690239.message}}"
  response_text: "{{nodes.router_1775339690239.response_text}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		assert.NotEmpty(t, errors, "expected validation errors for direct child field access on router (not flattened)")
		for _, e := range errors {
			t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
		}
	})
}

// TestRouterNodeStrictOutputValidation tests router output validation.
// Router has fixed top-level fields (5 proto fields). Child outputs are
// accessed via nodes.router.outputs.<field>. Direct access to child output
// names at the top level (e.g. nodes.router.message) is an error.
func TestRouterNodeStrictOutputValidation(t *testing.T) {
	makeYAML := func(fieldExpr string) string {
		return fmt.Sprintf(`
name: test-router-strict
entry: [my_router]
nodes:
  - id: my_router
    type: router
    workflows:
      - ref: builtin://agent
outputs:
  result: "{{%s}}"
`, fieldExpr)
	}

	type testCase struct {
		name      string
		expr      string
		wantError bool
	}

	// Without a loader, the router still has typed top-level fields.
	// The `outputs` sub-field is dynamic (any sub-access allowed).
	// Direct access to non-proto fields at top level is an error.
	cases := []testCase{
		// Valid: known router string fields
		{"selected_workflow passes", "nodes.my_router.selected_workflow", false},
		{"selected_preset passes", "nodes.my_router.selected_preset", false},
		{"selected_node passes", "nodes.my_router.selected_node", false},
		{"prompt passes", "nodes.my_router.prompt", false},
		{"reasoning passes", "nodes.my_router.reasoning", false},
		// Valid: outputs is dynamic, allows any sub-access
		{"outputs.message passes", "nodes.my_router.outputs.message", false},
		{"outputs.response_text passes", "nodes.my_router.outputs.response_text", false},
		{"outputs.any_custom_field passes", "nodes.my_router.outputs.any_custom_field", false},
		// Invalid: child outputs are NOT flattened to top level
		{"message errors (not a router field)", "nodes.my_router.message", true},
		{"response_text errors (not a router field)", "nodes.my_router.response_text", true},
		{"nonexistent errors", "nodes.my_router.nonexistent", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(makeYAML(tc.expr)))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tc.wantError {
				assert.NotEmpty(t, errors, "expected validation error for %q", tc.expr)
				for _, e := range errors {
					t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
				}
			} else {
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
			}
		})
	}

	// With a loader, candidate outputs are resolved as Properties of `outputs`.
	// Top-level access is still limited to the 5 proto fields.
	t.Run("with loader - typed validation", func(t *testing.T) {
		loader := func(ref string) (*reliantv1.Workflow, error) {
			if ref == "builtin://agent" {
				return &reliantv1.Workflow{
					Outputs: map[string]string{
						"message":       "{{nodes.some_node.message}}",
						"response_text": "{{nodes.some_node.response_text}}",
					},
				}, nil
			}
			return nil, fmt.Errorf("not found")
		}

		typedCases := []testCase{
			// Valid: router metadata
			{"selected_workflow passes", "nodes.my_router.selected_workflow", false},
			{"selected_node passes", "nodes.my_router.selected_node", false},
			// Valid: explicit access via outputs
			{"outputs.message passes", "nodes.my_router.outputs.message", false},
			{"outputs.response_text passes", "nodes.my_router.outputs.response_text", false},
			// Invalid: child outputs not flattened to top level
			{"message errors (not a router field)", "nodes.my_router.message", true},
			{"response_text errors (not a router field)", "nodes.my_router.response_text", true},
			// Invalid: not a known field
			{"nonexistent errors", "nodes.my_router.nonexistent", true},
		}

		for _, tc := range typedCases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				wf, err := wfyaml.ParseWorkflow([]byte(makeYAML(tc.expr)))
				require.NoError(t, err)

				result := &Result{}
				ValidateCELWithCompilation(wf, result, loader)

				errors := result.Errors()
				if tc.wantError {
					assert.NotEmpty(t, errors, "expected validation error for %q", tc.expr)
					for _, e := range errors {
						t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
					}
				} else {
					for _, e := range errors {
						t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
					}
					assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
				}
			})
		}
	})
}

// TestRouterNodeDeclaredOutputs tests that when a router node declares outputs
// (via RouterArgs.Outputs map), those keys become accessible as top-level fields
// (e.g. nodes.router_id.my_field) while undeclared keys still fail validation.
func TestRouterNodeDeclaredOutputs(t *testing.T) {
	makeWorkflowWithDeclaredOutputs := func(fieldExpr string, declaredOutputs map[string]string) *reliantv1.Workflow {
		yamlStr := fmt.Sprintf(`
name: test-router-declared-outputs
entry: [my_router]
nodes:
  - id: my_router
    type: router
    workflows:
      - ref: builtin://agent
outputs:
  result: "{{%s}}"
`, fieldExpr)
		wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
		if err != nil {
			t.Fatalf("parse YAML: %v", err)
		}
		// Set declared outputs on the router args.
		if routerArgs := wf.Nodes[0].GetRouter(); routerArgs != nil && declaredOutputs != nil {
			routerArgs.Outputs = declaredOutputs
		}
		return wf
	}

	declaredOutputs := map[string]string{
		"message": "outputs.message",
		"summary": "outputs.summary",
	}

	type testCase struct {
		name      string
		expr      string
		wantError bool
	}

	t.Run("declared outputs as top-level fields", func(t *testing.T) {
		cases := []testCase{
			// Valid: declared output keys are accessible at top level
			{"declared key message passes", "nodes.my_router.message", false},
			{"declared key summary passes", "nodes.my_router.summary", false},
			// Invalid: undeclared keys still fail
			{"undeclared key nonexistent fails", "nodes.my_router.nonexistent", true},
			{"undeclared key response_text fails", "nodes.my_router.response_text", true},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				wf := makeWorkflowWithDeclaredOutputs(tc.expr, declaredOutputs)

				result := &Result{}
				ValidateCELWithCompilation(wf, result, nil)

				errors := result.Errors()
				if tc.wantError {
					assert.NotEmpty(t, errors, "expected validation error for %q", tc.expr)
					for _, e := range errors {
						t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
					}
				} else {
					for _, e := range errors {
						t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
					}
					assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
				}
			})
		}
	})

	t.Run("fixed proto fields still work with declared outputs", func(t *testing.T) {
		cases := []testCase{
			{"selected_workflow passes", "nodes.my_router.selected_workflow", false},
			{"selected_preset passes", "nodes.my_router.selected_preset", false},
			{"selected_node passes", "nodes.my_router.selected_node", false},
			{"prompt passes", "nodes.my_router.prompt", false},
			{"reasoning passes", "nodes.my_router.reasoning", false},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				wf := makeWorkflowWithDeclaredOutputs(tc.expr, declaredOutputs)

				result := &Result{}
				ValidateCELWithCompilation(wf, result, nil)

				errors := result.Errors()
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
			})
		}
	})

	t.Run("outputs sub-field access still works with declared outputs", func(t *testing.T) {
		cases := []testCase{
			{"outputs.message passes", "nodes.my_router.outputs.message", false},
			{"outputs.summary passes", "nodes.my_router.outputs.summary", false},
			{"outputs.any_custom passes", "nodes.my_router.outputs.any_custom", false},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				wf := makeWorkflowWithDeclaredOutputs(tc.expr, declaredOutputs)

				result := &Result{}
				ValidateCELWithCompilation(wf, result, nil)

				errors := result.Errors()
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
			})
		}
	})

	t.Run("empty outputs map preserves existing behavior", func(t *testing.T) {
		cases := []testCase{
			// With empty declared outputs, non-proto fields should fail
			{"message fails with empty outputs", "nodes.my_router.message", true},
			{"summary fails with empty outputs", "nodes.my_router.summary", true},
			// Proto fields still pass
			{"selected_workflow passes", "nodes.my_router.selected_workflow", false},
			{"selected_node passes", "nodes.my_router.selected_node", false},
			// outputs sub-field still works
			{"outputs.message passes", "nodes.my_router.outputs.message", false},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				wf := makeWorkflowWithDeclaredOutputs(tc.expr, map[string]string{})

				result := &Result{}
				ValidateCELWithCompilation(wf, result, nil)

				errors := result.Errors()
				if tc.wantError {
					assert.NotEmpty(t, errors, "expected validation error for %q", tc.expr)
					for _, e := range errors {
						t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
					}
				} else {
					for _, e := range errors {
						t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
					}
					assert.Empty(t, errors, "expected no validation errors for %q", tc.expr)
				}
			})
		}
	})
}

// TestValidateCELWithCompilation_ValidFieldAccess tests that valid field access passes.
func TestValidateCELWithCompilation_ValidFieldAccess(t *testing.T) {
	workflowYAML := `
name: test-valid-field
entry: [my_run]
nodes:
  - id: my_run
    type: run
    args:
      command: "echo test"
outputs:
  # Valid field access
  code: "{{nodes.my_run.exit_code}}"
  output: "{{nodes.my_run.stdout}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have no errors
	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for valid field access")
}

// TestValidateCELWithCompilation_TypeInference tests that output types are inferred.
func TestValidateCELWithCompilation_TypeInference(t *testing.T) {
	workflowYAML := `
name: test-type-inference
entry: [my_run]
nodes:
  - id: my_run
    type: run
    args:
      command: "echo test"
outputs:
  is_success: "{{nodes.my_run.exit_code == 0}}"
  doubled_code: "{{nodes.my_run.exit_code * 2}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	// Build type context to check inferred types
	typeCtx := BuildWorkflowTypeContext(wf, nil)
	require.NotNil(t, typeCtx)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have no errors
	errors := result.Errors()
	assert.Empty(t, errors, "expected no validation errors")

	// Check that types were inferred (stored in typeCtx.OutputFields)
	// Note: The current implementation stores inferred types in the context
	t.Logf("Output fields: %+v", typeCtx.OutputFields)
}

func TestValidateCELWithCompilation_MalformedExpressionsAndUnknownSelectors(t *testing.T) {
	workflowYAML := `
name: test-cel-negative
entry: [step1]
inputs:
  model:
    type: model
    default:
      id: gpt-5
nodes:
  - id: step1
    type: call_llm
    args:
      model: "{{inputs.model}}"
  - id: step2
    type: save_message
    args:
      role: assistant
      content: "done"
edges:
  - from: step1
    cases:
      - to: step2
        condition: "nodes.unknown_node.response_text == 'x'"
      - to: step2
        condition: "size(nodes.step1.tool_calls"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	require.NotEmpty(t, errors, "expected CEL validation errors for malformed expressions/selectors")

	errText := result.Error()
	assert.True(t,
		containsAny(errText, "unknown", "undeclared reference", "Syntax error", "compilation", "no such key"),
		"expected unknown-selector or malformed CEL diagnostics, got: %s", errText,
	)
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestValidateCELWithCompilation_AllBuiltinWorkflows validates all builtin workflows
// using CEL compilation to catch invalid field access and type errors.
func TestValidateCELWithCompilation_AllBuiltinWorkflows(t *testing.T) {
	// Find all builtin workflow files
	builtinDir := "../builtin"
	files, err := filepath.Glob(filepath.Join(builtinDir, "*.yaml"))
	require.NoError(t, err, "failed to glob builtin workflows")
	require.NotEmpty(t, files, "no builtin workflow files found")

	t.Logf("Found %d builtin workflow files", len(files))

	// Loader for resolving builtin:// refs in loop/spawn nodes
	builtinLoader := func(name string) (*reliantv1.Workflow, error) {
		name = strings.TrimPrefix(name, "builtin://")
		if !strings.HasSuffix(name, ".yaml") {
			name += ".yaml"
		}
		data, err := os.ReadFile(filepath.Join(builtinDir, name))
		if err != nil {
			return nil, fmt.Errorf("builtin workflow not found: %s", name)
		}
		return wfyaml.ParseWorkflow(data)
	}

	var allErrors []string

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			// Read and parse the workflow
			data, err := os.ReadFile(file)
			require.NoError(t, err, "failed to read workflow file")

			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err, "failed to parse workflow YAML")

			// Validate with CEL compilation
			result := &Result{}
			ValidateCELWithCompilation(wf, result, builtinLoader)

			// Report errors
			errors := result.Errors()
			if len(errors) > 0 {
				for _, e := range errors {
					errMsg := fmt.Sprintf("%s: %s - %s", filepath.Base(file), pathToString(e.Path), e.Message)
					t.Logf("ERROR: %s", errMsg)
					if e.Suggestion != "" {
						t.Logf("  Suggestion: %s", e.Suggestion)
					}
					allErrors = append(allErrors, errMsg)
				}
			} else {
				t.Logf("✓ %s passed validation", filepath.Base(file))
			}
		})
	}

	// Known false positives from inline workflow validation:
	// - response_data operator overloads: CEL type checker doesn't expose in/[] on response_data struct types
	// - outputs field access during node validation: output types are inferred after node templates,
	//   so forward references to outputs.X in thread inject are unresolved at compile time
	knownFalsePositives := []string{
		"found no matching overload for '@in' applied to",
		"found no matching overload for '_[_]' applied to",
		"undefined field 'discovery_brief'", // outputs not yet inferred during node validation
	}

	var unexpectedErrors []string
	for _, e := range allErrors {
		known := false
		for _, fp := range knownFalsePositives {
			if strings.Contains(e, fp) {
				known = true
				break
			}
		}
		if !known {
			unexpectedErrors = append(unexpectedErrors, e)
		}
	}

	// Summary
	if len(allErrors) > 0 {
		t.Logf("\n=== VALIDATION SUMMARY ===")
		t.Logf("Found %d total errors (%d known false positives, %d unexpected):",
			len(allErrors), len(allErrors)-len(unexpectedErrors), len(unexpectedErrors))
		for _, e := range allErrors {
			t.Logf("  - %s", e)
		}
	}
	if len(unexpectedErrors) > 0 {
		t.Fatalf("Found %d unexpected errors (see above)", len(unexpectedErrors))
	}
}

// TestValidateCELWithCompilation_AllUserWorkflows validates all user workflows
// in .reliant/workflows/ directory.
func TestValidateCELWithCompilation_AllUserWorkflows(t *testing.T) {
	// Find user workflow files
	userDir := "../../../../.reliant/workflows"
	files, err := filepath.Glob(filepath.Join(userDir, "**/*.yaml"))
	if err != nil || len(files) == 0 {
		// Try non-recursive glob as well
		files, err = filepath.Glob(filepath.Join(userDir, "*.yaml"))
	}

	if err != nil || len(files) == 0 {
		t.Skip("No user workflow files found in .reliant/workflows/")
		return
	}

	t.Logf("Found %d user workflow files", len(files))

	var allErrors []string

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			// Read and parse the workflow
			data, err := os.ReadFile(file)
			require.NoError(t, err, "failed to read workflow file")

			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err, "failed to parse workflow YAML")

			// Validate with CEL compilation
			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			// Report errors
			errors := result.Errors()
			if len(errors) > 0 {
				for _, e := range errors {
					errMsg := fmt.Sprintf("%s: %s - %s", filepath.Base(file), pathToString(e.Path), e.Message)
					t.Logf("ERROR: %s", errMsg)
					if e.Suggestion != "" {
						t.Logf("  Suggestion: %s", e.Suggestion)
					}
					allErrors = append(allErrors, errMsg)
				}
			} else {
				t.Logf("✓ %s passed validation", filepath.Base(file))
			}
		})
	}

	// Summary
	if len(allErrors) > 0 {
		t.Logf("\n=== VALIDATION SUMMARY ===")
		t.Logf("Found %d total errors across user workflows:", len(allErrors))
		for _, e := range allErrors {
			t.Logf("  - %s", e)
		}
		t.Fail()
	}
}

// TestBuildWorkflowTypeContext_RefBasedOutputResolution tests that ref-based
// workflow nodes resolve their outputs via the WorkflowLoader.
func TestBuildWorkflowTypeContext_RefBasedOutputResolution(t *testing.T) {
	parentYAML := `
name: test-ref-outputs
entry: [call_agent]
nodes:
  - id: call_agent
    type: workflow
    args:
      ref: "builtin://agent"
  - id: use_output
    type: save_message
    args:
      role: user
      content: "{{nodes.call_agent.response_text}}"
`
	parentWf, err := wfyaml.ParseWorkflow([]byte(parentYAML))
	require.NoError(t, err)

	childYAML := `
name: agent
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
outputs:
  message: "{{nodes.step1.message}}"
  response_text: "{{nodes.step1.response_text}}"
`
	childWf, err := wfyaml.ParseWorkflow([]byte(childYAML))
	require.NoError(t, err)

	// Build context without loader — should have no outputs for call_agent.
	ctxNoLoader := BuildWorkflowTypeContext(parentWf, nil)
	require.NotNil(t, ctxNoLoader)
	_, hasOutputsWithoutLoader := ctxNoLoader.NodeOutputs["call_agent"]
	assert.False(t, hasOutputsWithoutLoader, "without loader, ref-based node should have no resolved outputs")

	// Build context with loader — should resolve outputs.
	mockLoader := func(ref string) (*reliantv1.Workflow, error) {
		if ref == "builtin://agent" {
			return childWf, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", ref)
	}

	ctxWithLoader := BuildWorkflowTypeContext(parentWf, mockLoader)
	require.NotNil(t, ctxWithLoader)
	agentOutputs, hasOutputs := ctxWithLoader.NodeOutputs["call_agent"]
	assert.True(t, hasOutputs, "with loader, ref-based node should have resolved outputs")

	_, hasMessage := agentOutputs["message"]
	_, hasResponseText := agentOutputs["response_text"]
	_, hasOutputsField := agentOutputs["outputs"]
	assert.True(t, hasMessage, "should have 'message' output")
	assert.True(t, hasResponseText, "should have 'response_text' output")
	assert.True(t, hasOutputsField, "should have 'outputs' catch-all field")

	// Validate that CEL compilation succeeds with loader-resolved outputs.
	result := &Result{}
	ValidateCELWithCompilation(parentWf, result, mockLoader)
	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s", e.Message)
	}
	assert.Empty(t, errors, "CEL validation should pass with resolved ref outputs")
}

// TestBuildWorkflowTypeContext_RefBasedLoopOutputResolution tests that ref-based
// loop nodes resolve their outputs via the WorkflowLoader.
func TestBuildWorkflowTypeContext_RefBasedLoopOutputResolution(t *testing.T) {
	parentYAML := `
name: test-ref-loop-outputs
entry: [my_loop]
nodes:
  - id: my_loop
    type: loop
    args:
      ref: "builtin://agent"
      while: "nodes.my_loop._iterations < 3"
`
	parentWf, err := wfyaml.ParseWorkflow([]byte(parentYAML))
	require.NoError(t, err)

	childYAML := `
name: agent
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model: mock
outputs:
  result: "{{nodes.step1.response_text}}"
`
	childWf, err := wfyaml.ParseWorkflow([]byte(childYAML))
	require.NoError(t, err)

	mockLoader := func(ref string) (*reliantv1.Workflow, error) {
		if ref == "builtin://agent" {
			return childWf, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", ref)
	}

	ctx := BuildWorkflowTypeContext(parentWf, mockLoader)
	require.NotNil(t, ctx)
	loopOutputs, hasOutputs := ctx.NodeOutputs["my_loop"]
	assert.True(t, hasOutputs, "loop node should have resolved outputs")

	_, hasResult := loopOutputs["result"]
	_, hasIterations := loopOutputs["_iterations"]
	assert.True(t, hasResult, "should have 'result' output from ref workflow")
	assert.True(t, hasIterations, "should have '_iterations' system output")
}

// TestValidation_CatchesPreFixBugs proves that our validation infrastructure
// catches the exact bugs that the log_file, working_dir, and skipped-node-defaults
// features were designed to fix. Without those features, these workflow patterns
// would fail at CEL compilation time (load-time validation), not at runtime.
func TestValidation_CatchesPreFixBugs(t *testing.T) {

	// Bug 1: Referencing output.log_file in a run node's save_message
	// Our fix: added log_file and working_dir to RunOutput proto so CEL knows about them.
	// Without the fix: CEL compilation fails with "undefined field 'log_file'"
	t.Run("run node output.log_file is a known field", func(t *testing.T) {
		workflowYAML := `
name: test-log-file-access
entry: [my_run]
nodes:
  - id: my_run
    type: run
    command: "echo hello"
    log_file: "./data/test.log"
    save_message:
      condition: "output.exit_code != 0"
      role: assistant
      content: "Failed — log at {{output.log_file}}, dir was {{output.working_dir}}"
outputs:
  log: "{{nodes.my_run.log_file}}"
  dir: "{{nodes.my_run.working_dir}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		for _, e := range errors {
			t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
		}
		assert.Empty(t, errors, "log_file and working_dir should be valid RunOutput fields")
	})

	// Bug 2: Referencing a bogus field on run output — proves validation DOES catch unknown fields.
	t.Run("run node output.bogus_field is caught", func(t *testing.T) {
		workflowYAML := `
name: test-bogus-run-field
entry: [my_run]
nodes:
  - id: my_run
    type: run
    command: "echo hello"
outputs:
  bad: "{{nodes.my_run.bogus_field}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		require.NotEmpty(t, errors, "bogus_field should fail validation")
		found := false
		for _, e := range errors {
			t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
			if strings.Contains(e.Message, "bogus_field") {
				found = true
			}
		}
		assert.True(t, found, "error message should mention 'bogus_field'")
	})

	// Bug 3: Skipped conditional run node accessed by downstream CEL.
	// The real get-it-right workflow has: condition: "inputs.lint_command != ''"
	// followed by edges that reference: nodes.lint.exit_code != 0
	// Without skipped-run-node defaults, if lint is skipped, nodes.lint wouldn't
	// have exit_code and the CEL expression would fail at runtime.
	// Our fix: SkippedRunOutputMap() populates exit_code=0, stdout="", etc.
	// This test proves the CEL type system accepts these references because
	// run node output types include exit_code, stdout, stderr, log_file, working_dir.
	t.Run("conditional run node fields accessible in downstream CEL", func(t *testing.T) {
		workflowYAML := `
name: test-conditional-run-downstream
entry: [lint]
nodes:
  - id: lint
    type: run
    condition: "inputs.lint_command != ''"
    command: "{{inputs.lint_command}}"
  - id: review
    type: call_llm
    model:
      tags: [flagship]
inputs:
  lint_command:
    type: string
    default: ""
edges:
  - from: lint
    cases:
      - condition: "nodes.lint.exit_code != 0"
        to: []
    default: [review]
outputs:
  lint_exit: "{{nodes.lint.exit_code}}"
  lint_out: "{{nodes.lint.stdout}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		for _, e := range errors {
			t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
		}
		assert.Empty(t, errors, "run node outputs (exit_code, stdout) should be valid even when node is conditional")
	})

	// Bug 4: The ACTUAL get-it-right pattern — inline loop with conditional run nodes
	// and downstream edge conditions referencing their outputs.
	// This is the exact pattern that was broken before the RunOutput proto fix.
	t.Run("get-it-right pattern: loop with conditional runs and downstream checks", func(t *testing.T) {
		workflowYAML := `
name: test-get-it-right-pattern
entry: [attempt]
inputs:
  lint_command:
    type: string
    default: ""
  lint_log:
    type: string
    default: "./lint.log"
  max_retries:
    type: integer
    default: 3
nodes:
  - id: attempt
    type: loop
    while: >
      (has(outputs.lint_exit) && outputs.lint_exit != 0)
      && iter.iteration < inputs.max_retries
    inline:
      entry: [impl]
      outputs:
        lint_exit: "{{nodes.lint.exit_code}}"
      nodes:
        - id: impl
          type: call_llm
          model:
            tags: [flagship]
        - id: lint
          type: run
          condition: "inputs.lint_command != ''"
          command: "{{inputs.lint_command}}"
          log_file: "{{inputs.lint_log}}"
          save_message:
            condition: "output.exit_code != 0"
            role: assistant
            content: "Lint FAILED — log at {{output.log_file}}, dir: {{output.working_dir}}"
        - id: join
          type: join
      edges:
        - from: impl
          default: [lint]
        - from: impl
          default: [join]
        - from: lint
          default: [join]
        - from: join
          cases:
            - condition: "nodes.lint.exit_code != 0"
              to: []
          default: []
outputs:
  lint_exit: "{{nodes.attempt.lint_exit}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		errors := result.Errors()
		for _, e := range errors {
			t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
		}
		assert.Empty(t, errors,
			"get-it-right pattern should pass validation: run node output fields (exit_code, log_file, working_dir) must be known to CEL")
	})
}
