// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWarnConditionalNodeAccess_DirectAccess tests that a warning IS generated
// when directly accessing outputs from a conditional node without null safety.
func TestWarnConditionalNodeAccess_DirectAccess(t *testing.T) {
	workflowYAML := `
name: test-conditional-access
entry: [step1]
inputs:
  enabled:
    type: boolean
    default: true
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: check_condition
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
  - id: use_result
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: "Result was: {{nodes.check_condition.message.content}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have a warning about conditional access
	warnings := result.Warnings()
	require.NotEmpty(t, warnings, "expected warning about conditional node access")

	found := false
	for _, w := range warnings {
		if w.Category == CategoryConditionalAccess &&
			strings.Contains(w.Message, "check_condition") &&
			strings.Contains(w.Message, "may be skipped") {
			found = true
			t.Logf("Found expected warning: %s", w.Message)
		}
	}
	assert.True(t, found, "expected warning about conditional node 'check_condition'")
}

// TestWarnConditionalNodeAccess_UnconditionalNode tests that NO warning is generated
// when accessing outputs from an unconditional node.
func TestWarnConditionalNodeAccess_UnconditionalNode(t *testing.T) {
	workflowYAML := `
name: test-unconditional-access
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: step2
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: "Result was: {{nodes.step1.message.content}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access
	warnings := result.ByCategory(CategoryConditionalAccess)
	assert.Empty(t, warnings, "expected no warnings for unconditional node access")
}

// TestWarnConditionalNodeAccess_OptionalChaining tests that NO warning is generated
// when using optional chaining (nodes.?id.field) to access conditional node outputs.
func TestWarnConditionalNodeAccess_OptionalChaining(t *testing.T) {
	workflowYAML := `
name: test-optional-chaining
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
  - id: use_result
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: "Result was: {{nodes.?conditional_step.message.content}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access
	warnings := result.ByCategory(CategoryConditionalAccess)
	for _, w := range warnings {
		t.Logf("Unexpected warning: %s", w.Message)
	}
	assert.Empty(t, warnings, "expected no warnings when using optional chaining")
}

// TestWarnConditionalNodeAccess_HasCheck tests that NO warning is generated
// when using has() to check for conditional node outputs.
func TestWarnConditionalNodeAccess_HasCheck(t *testing.T) {
	workflowYAML := `
name: test-has-check
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
edges:
  - from: step1
    cases:
      - condition: "has(nodes.conditional_step)"
        to: [conditional_step]
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access
	warnings := result.ByCategory(CategoryConditionalAccess)
	for _, w := range warnings {
		t.Logf("Unexpected warning: %s", w.Message)
	}
	assert.Empty(t, warnings, "expected no warnings when using has() check")
}

// TestWarnConditionalNodeAccess_NullCheck tests that NO warning is generated
// when using null comparison to check conditional node outputs.
func TestWarnConditionalNodeAccess_NullCheck(t *testing.T) {
	workflowYAML := `
name: test-null-check
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
edges:
  - from: step1
    cases:
      - condition: "nodes.conditional_step != null"
        to: [conditional_step]
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access
	warnings := result.ByCategory(CategoryConditionalAccess)
	for _, w := range warnings {
		t.Logf("Unexpected warning: %s", w.Message)
	}
	assert.Empty(t, warnings, "expected no warnings when using null check")
}

// TestWarnConditionalNodeAccess_ReverseNullCheck tests that NO warning is generated
// when using reverse null comparison (null != nodes.id).
func TestWarnConditionalNodeAccess_ReverseNullCheck(t *testing.T) {
	workflowYAML := `
name: test-reverse-null-check
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
edges:
  - from: step1
    cases:
      - condition: "null != nodes.conditional_step"
        to: [conditional_step]
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access
	warnings := result.ByCategory(CategoryConditionalAccess)
	for _, w := range warnings {
		t.Logf("Unexpected warning: %s", w.Message)
	}
	assert.Empty(t, warnings, "expected no warnings when using reverse null check")
}

// TestWarnConditionalNodeAccess_JoinNode tests that JoinNode conditions don't trigger warnings.
// JoinNode uses condition differently (for join mode: all/any), not for conditional execution.
func TestWarnConditionalNodeAccess_JoinNode(t *testing.T) {
	workflowYAML := `
name: test-join-node
entry: [step1]
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: step2
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: join_step
    type: join
    condition: "all"
  - id: after_join
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: "Joined: {{nodes.join_step}}"
edges:
  - from: step1
    to: [join_step]
  - from: step2
    to: [join_step]
  - from: join_step
    to: [after_join]
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have no warnings about conditional access for join nodes
	warnings := result.ByCategory(CategoryConditionalAccess)
	for _, w := range warnings {
		t.Logf("Warning: %s", w.Message)
		// Join node should not trigger a warning
		assert.NotContains(t, w.Message, "join_step", "join node should not trigger conditional access warning")
	}
}

// TestWarnConditionalNodeAccess_EdgeCondition tests warnings in edge conditions.
func TestWarnConditionalNodeAccess_EdgeCondition(t *testing.T) {
	workflowYAML := `
name: test-edge-condition
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
  - id: step3
    type: call_llm
    args:
      model:
        tags: [flagship]
edges:
  - from: step1
    cases:
      - condition: "nodes.conditional_step.token_count > 100"
        to: [step3]
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have a warning about conditional access in edge condition
	warnings := result.ByCategory(CategoryConditionalAccess)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "conditional_step") {
			found = true
			t.Logf("Found expected warning: %s at path %v", w.Message, w.Path)
		}
	}
	assert.True(t, found, "expected warning about conditional node in edge condition")
}

// TestWarnConditionalNodeAccess_OutputExpression tests warnings in output expressions.
func TestWarnConditionalNodeAccess_OutputExpression(t *testing.T) {
	workflowYAML := `
name: test-output-expression
entry: [step1]
inputs:
  enabled:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "{{inputs.enabled}}"
    args:
      model:
        tags: [flagship]
outputs:
  result: "{{nodes.conditional_step.message.content}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have a warning about conditional access in output expression
	warnings := result.ByCategory(CategoryConditionalAccess)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "conditional_step") &&
			strings.Contains(strings.Join(w.Path, "."), "outputs") {
			found = true
			t.Logf("Found expected warning: %s at path %v", w.Message, w.Path)
		}
	}
	assert.True(t, found, "expected warning about conditional node in output expression")
}

// TestWarnConditionalNodeAccess_MultipleConditionalNodes tests warnings when
// multiple conditional nodes are accessed unsafely.
func TestWarnConditionalNodeAccess_MultipleConditionalNodes(t *testing.T) {
	workflowYAML := `
name: test-multiple-conditional
entry: [step1]
inputs:
  enabled1:
    type: boolean
  enabled2:
    type: boolean
nodes:
  - id: step1
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: cond1
    type: call_llm
    condition: "{{inputs.enabled1}}"
    args:
      model:
        tags: [flagship]
  - id: cond2
    type: call_llm
    condition: "{{inputs.enabled2}}"
    args:
      model:
        tags: [flagship]
  - id: use_both
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: "{{nodes.cond1.message.content}} and {{nodes.cond2.message.content}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := NewResult()
	validateCEL(wf, result)

	// Should have warnings about both conditional nodes
	warnings := result.ByCategory(CategoryConditionalAccess)
	foundCond1 := false
	foundCond2 := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "cond1") {
			foundCond1 = true
		}
		if strings.Contains(w.Message, "cond2") {
			foundCond2 = true
		}
	}
	assert.True(t, foundCond1, "expected warning about 'cond1'")
	assert.True(t, foundCond2, "expected warning about 'cond2'")
}

// TestIsConditionalAccessSafe has been replaced by AST-based tests in conditional_access_ast_test.go
// The old regex-based isConditionalAccessSafe function was removed in favor of AST-based detection.
