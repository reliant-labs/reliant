package runtime

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/assert"
)

// TestNativeTypeValidation verifies that native types enable compile-time field validation.
// Invalid field access like workflow.typo, iter.previous should fail at compile time.
func TestNativeTypeValidation(t *testing.T) {
	config := wfcel.DefaultCELEnvConfig()
	env, err := wfcel.NewEnv(config)
	assert.NoError(t, err)

	testCases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		// Valid field access - typed namespaces (using model.WorkflowContext and model.IterContext)
		{"workflow.id", "workflow.id", false},
		{"workflow.name", "workflow.name", false},
		{"workflow.path", "workflow.path", false},
		{"workflow.branch", "workflow.branch", false},
		{"workflow.mode", "workflow.mode", false},
		{"iter.iteration", "iter.iteration", false},

		// Invalid field access - should fail at compile time
		{"workflow.typo - undefined field", "workflow.typo", true},
		{"workflow.chat_id - not exposed", "workflow.chat_id", true},
		{"iter.previous - was never valid", "iter.previous", true},
		{"iter.index - removed in proto migration", "iter.index", true},

		// Dynamic namespaces - any field allowed
		{"inputs.anything - dynamic", "inputs.anything", false},
		{"nodes.call_llm.exit_code - dynamic", "nodes.call_llm.exit_code", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := env.Compile(tc.expr)
			hasErr := issues != nil && issues.Err() != nil

			if tc.wantErr {
				assert.True(t, hasErr, "Expected compile error for %q", tc.expr)
				if hasErr {
					t.Logf("Correctly rejected %q: %v", tc.expr, issues.Err())
				}
			} else {
				assert.False(t, hasErr, "Expected %q to compile successfully, got: %v", tc.expr, issues.Err())
			}
		})
	}
}
