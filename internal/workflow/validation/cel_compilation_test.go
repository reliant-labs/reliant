package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
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
		t.Logf("Found %d total errors across all workflows:", len(allErrors))
		for _, e := range allErrors {
			t.Logf("  - %s", e)
		}
		t.Fail()
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
