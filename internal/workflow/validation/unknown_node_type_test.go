package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStaticValidation_RejectsUnknownNodeType verifies that static analysis
// rejects workflows with unrecognized node types.
func TestStaticValidation_RejectsUnknownNodeType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		errContains string
	}{
		{
			name: "literal string null",
			yaml: `
name: test-null-type
entry: [bad_node]
nodes:
  - id: bad_node
    type: "null"
`,
			errContains: `unknown node type "null"`,
		},
		{
			name: "nonexistent type",
			yaml: `
name: test-nonexistent-type
entry: [bad_node]
nodes:
  - id: bad_node
    type: nonexistent
`,
			errContains: `unknown node type "nonexistent"`,
		},
		{
			name: "typo in type name",
			yaml: `
name: test-typo
entry: [bad_node]
nodes:
  - id: bad_node
    type: cal_llm
`,
			errContains: `unknown node type "cal_llm"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.yaml))
			require.NoError(t, err)

			result := StaticAnalysis(wf, nil)
			require.NotNil(t, result)
			assert.True(t, result.HasErrors(), "expected validation errors")

			errStr := result.Error()
			assert.True(t, strings.Contains(errStr, tt.errContains),
				"expected error containing %q, got: %s", tt.errContains, errStr)
		})
	}
}

// TestStaticValidation_AcceptsAllKnownNodeTypes verifies that every known
// node type passes the type check in validateNodeArgs (they may fail for
// other reasons like missing args, but NOT for unknown type).
func TestStaticValidation_AcceptsKnownNodeTypes(t *testing.T) {
	t.Parallel()
	// Minimal valid workflow for each known type
	workflows := map[string]string{
		"call_llm": `
name: test
entry: [n]
nodes:
  - id: n
    type: call_llm
    model:
      tags: [flagship]
`,
		"save_message": `
name: test
entry: [n]
nodes:
  - id: n
    type: save_message
    args:
      role: assistant
      content: "hello"
`,
		"approval": `
name: test
entry: [n]
nodes:
  - id: n
    type: approval
`,
		"run": `
name: test
entry: [n]
nodes:
  - id: n
    type: run
    command: "echo hello"
`,
	}

	for nodeType, yaml := range workflows {
		t.Run(nodeType, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(yaml))
			require.NoError(t, err)

			result := StaticAnalysis(wf, nil)
			if result != nil {
				for _, e := range result.Errors() {
					// There should be no "unknown node type" errors
					assert.False(t, strings.Contains(e.Message, "unknown node type"),
						"known type %q should not produce unknown type error: %s", nodeType, e.Message)
				}
			}
		})
	}
}

// TestStaticValidation_RejectsMissingType verifies that a node with no type
// field at all is rejected. This is the exact bug from the blog-content-pipeline
// workflow where `deai_loop` used `loop:` as a nested key instead of `type: loop`.
func TestStaticValidation_RejectsMissingType(t *testing.T) {
	t.Parallel()
	// Simulate a node that has no type field — this is what the YAML parser
	// produces when a node uses structural keys (like loop:) without type:
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test-missing-type
entry: [bad_node]
nodes:
  - id: bad_node
`))
	require.NoError(t, err)

	// Verify the parser produced a node with empty type
	require.Equal(t, "", wf.GetNodes()[0].GetType())

	result := StaticAnalysis(wf, nil)
	require.NotNil(t, result)
	assert.True(t, result.HasErrors(), "expected validation errors for missing type")

	errStr := result.Error()
	assert.True(t, strings.Contains(errStr, "missing a 'type' field"),
		"expected missing type error, got: %s", errStr)
}

// TestStaticValidation_UnknownTypeIncludesValidTypes verifies the error
// message includes the list of valid types for discoverability.
func TestStaticValidation_UnknownTypeIncludesValidTypes(t *testing.T) {
	t.Parallel()
	yaml := `
name: test
entry: [n]
nodes:
  - id: n
    type: bogus
`
	wf, err := wfyaml.ParseWorkflow([]byte(yaml))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	require.NotNil(t, result)
	require.True(t, result.HasErrors())

	errStr := result.Error()
	// The error should mention some known types to help the user
	for _, expected := range []string{"call_llm", "loop", "run"} {
		assert.True(t, strings.Contains(errStr, expected),
			"error should mention known type %q for discoverability, got: %s", expected, errStr)
	}
}
