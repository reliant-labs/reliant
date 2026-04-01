package validation

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNodeBaseValidation_ThreadInject tests that CEL expressions in thread.inject are properly validated.
// This test should fail before the fix, as the recursive validation skips NodeBase.
func TestNodeBaseValidation_ThreadInject(t *testing.T) {
	workflowYAML := `
name: test-thread-inject-validation
entry: [step1]
nodes:
  - id: step1
    type: workflow
    ref: builtin://agent
    thread:
      inject:
        role: user
        # This CEL expression is invalid because 'non_existent' does not exist.
        content: "{{ non_existent.field }}"
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for invalid CEL in thread.inject")

	found := false
	for _, e := range errors {
		t.Logf("Error: %v - %s", e.Path, e.Message)
		if contains(e.Message, "undeclared reference to 'non_existent'") {
			found = true
			assertErrorPath(t, []string{"nodes", "[0]", "thread", "inject", "content"}, e.Path)
		}
	}
	assert.True(t, found, "expected error about undeclared reference in thread.inject")
}

// TestNodeBaseValidation_SaveMessage_Valid tests that valid CEL in save_message passes.
func TestNodeBaseValidation_SaveMessage_Valid(t *testing.T) {
	workflowYAML := `
name: test-save-message-validation-valid
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    save_message:
      # This is a valid use of the 'output' context variable.
      content: "{{ output.response_text }}"
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for valid save_message")
}

// TestNodeBaseValidation_SaveMessage_Invalid tests that invalid CEL in save_message is caught.
// This test should fail before the fix.
func TestNodeBaseValidation_SaveMessage_Invalid(t *testing.T) {
	workflowYAML := `
name: test-save-message-validation-invalid
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    save_message:
      # 'output' is available, but 'non_existent_field' is not a valid field.
      content: "{{ output.non_existent_field }}"
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for invalid CEL in save_message")

	found := false
	for _, e := range errors {
		t.Logf("Error: %v - %s", e.Path, e.Message)
		if contains(e.Message, "undefined field 'non_existent_field'") {
			found = true
			assertErrorPath(t, []string{"nodes", "[0]", "save_message", "content"}, e.Path)
		}
	}
	assert.True(t, found, "expected error about undefined field in save_message")
}

// TestNodeBaseValidation_SaveMessage_ToolCallsTypeMismatch tests that save_message.tool_calls
// correctly validates the return type. It should fail if a non-list type is returned.
func TestNodeBaseValidation_SaveMessage_ToolCallsTypeMismatch(t *testing.T) {
	workflowYAML := `
name: test-save-message-tool-calls-type-mismatch
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    save_message:
      # tool_calls must be a list, but here we're providing a string.
      tool_calls: "{{ output.response_text }}" 
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for type mismatch in save_message.tool_calls")

	found := false
	for _, e := range errors {
		t.Logf("Error: %v - %s", e.Path, e.Message)
		if contains(e.Message, "tool_calls must be a list") {
			found = true
			assertErrorPath(t, []string{"nodes", "[0]", "save_message", "tool_calls"}, e.Path)
		}
	}
	assert.True(t, found, "expected error about tool_calls requiring a list")
}

// TestNodeBaseValidation_SaveMessage_ToolCallsStaticTextError tests that providing static text
// for save_message.tool_calls results in an error, as it requires a CEL expression.
func TestNodeBaseValidation_SaveMessage_ToolCallsStaticTextError(t *testing.T) {
	workflowYAML := `
name: test-save-message-tool-calls-static-text
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    save_message:
      # tool_calls must be a CEL expression that evaluates to a list.
      tool_calls: "some static text"
`
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for static text in save_message.tool_calls")

	found := false
	for _, e := range errors {
		t.Logf("Error: %v - %s", e.Path, e.Message)
		if contains(e.Message, "must use a CEL expression") {
			found = true
			assertErrorPath(t, []string{"nodes", "[0]", "save_message", "tool_calls"}, e.Path)
		}
	}
	assert.True(t, found, "expected error about tool_calls requiring a CEL expression")
}
