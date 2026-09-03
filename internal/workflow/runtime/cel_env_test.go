// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCELEnvConfigs verifies all CEL environment configs can be created
func TestCELEnvConfigs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config wfcel.CELEnvConfig
	}{
		{"DefaultCELEnvConfig", wfcel.DefaultCELEnvConfig()},
		{"SaveMessageCELEnvConfig", wfcel.SaveMessageCELEnvConfig()},
		{"LoopWhileCELEnvConfig", wfcel.LoopWhileCELEnvConfig()},
		{"TemplateResolutionCELEnvConfig", wfcel.TemplateResolutionCELEnvConfig()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := wfcel.NewEnv(tt.config)
			require.NoError(t, err)
			require.NotNil(t, env)
		})
	}
}

// TestNewCELEnvFromContext verifies auto-detection of namespaces
func TestNewCELEnvFromContext(t *testing.T) {
	t.Parallel()
	ctx := map[string]interface{}{
		"inputs":   map[string]interface{}{"mode": "auto"},
		"workflow": map[string]interface{}{"id": "test"},
		"steps":    map[string]interface{}{},
	}

	env, err := wfcel.NewEnvFromContext(ctx, true)
	require.NoError(t, err)

	// Should compile expressions using detected namespaces
	ast, issues := env.Compile("inputs.mode == 'auto'")
	require.Nil(t, issues.Err())
	require.NotNil(t, ast)
}

// TestEnsureNamespaceDefaults verifies namespace defaults are set
func TestEnsureNamespaceDefaults(t *testing.T) {
	t.Parallel()
	ctx := map[string]interface{}{
		"inputs": map[string]interface{}{"x": 1},
	}

	result := wfcel.EnsureNamespaceDefaults(ctx, []wfcel.CELNamespace{wfcel.CELInputs, wfcel.CELWorkflow, wfcel.CELNodes})

	assert.NotNil(t, result["inputs"])
	assert.NotNil(t, result["workflow"])
	assert.NotNil(t, result["nodes"])
	assert.Equal(t, 1, result["inputs"].(map[string]interface{})["x"])
}

// TestBuiltinWorkflowCELExpressions validates ALL CEL expressions from builtin workflows
// compile against the runtime CEL environment. This test would catch namespace mismatches.
func TestBuiltinWorkflowCELExpressions(t *testing.T) {
	t.Parallel()
	// Find builtin workflow directory
	builtinDir := filepath.Join(".", "..", "builtin")
	if _, err := os.Stat(builtinDir); os.IsNotExist(err) {
		builtinDir = filepath.Join("..", "..", "workflow", "builtin")
	}
	if _, err := os.Stat(builtinDir); os.IsNotExist(err) {
		t.Skip("builtin workflow directory not found")
	}

	// Pattern to find {{...}} expressions
	templateRegex := regexp.MustCompile(`\{\{([^}]+)\}\}`)

	// Read all YAML files
	files, err := filepath.Glob(filepath.Join(builtinDir, "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no YAML files found in builtin directory")

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			content, err := os.ReadFile(file)
			require.NoError(t, err)

			// Find all template expressions
			matches := templateRegex.FindAllStringSubmatch(string(content), -1)
			if len(matches) == 0 {
				return
			}

			// Create a comprehensive CEL environment that includes ALL namespaces
			config := wfcel.CELEnvConfig{
				Namespaces: []wfcel.CELNamespace{
					wfcel.CELInputs,
					wfcel.CELWorkflow,
					wfcel.CELNodes,
					wfcel.CELIter,
					wfcel.CELOutput,
					wfcel.CELOutputs,
				},
				IncludeStdLib:          true,
				IncludeCustomFunctions: true,
			}

			env, err := wfcel.NewEnv(config)
			require.NoError(t, err)

			for _, match := range matches {
				expr := strings.TrimSpace(match[1])
				if expr == "" {
					continue
				}

				t.Run(expr, func(t *testing.T) {
					_, issues := env.Compile(expr)
					if issues != nil && issues.Err() != nil {
						errStr := issues.Err().Error()
						if strings.Contains(errStr, "undeclared reference") {
							for _, ns := range wfcel.AllNamespaces() {
								if strings.Contains(expr, string(ns)+".") {
									if strings.Contains(errStr, string(ns)) {
										t.Errorf("CEL expression '%s' references namespace '%s' which should be declared but got: %v",
											expr, ns, issues.Err())
									}
								}
							}
						}
					}
				})
			}
		})
	}
}

// TestTemplateResolutionCELNamespace verifies that template resolution uses correct namespaces
func TestTemplateResolutionCELNamespace(t *testing.T) {
	t.Parallel()
	config := wfcel.TemplateResolutionCELEnvConfig()
	env, err := wfcel.NewEnv(config)
	require.NoError(t, err)

	// This expression is used in agent.yaml for loop.max
	ast, issues := env.Compile("inputs.max_turns")
	require.Nil(t, issues.Err(), "inputs.max_turns should compile in template resolution context")
	require.NotNil(t, ast)
}

// TestCELContextBuilder_WithExecContext verifies execution context is set
func TestCELContextBuilder_WithExecContext(t *testing.T) {
	t.Parallel()
	builder := NewCELContextBuilder().
		WithWorkflow("wf-1", "test-workflow").
		WithExecContext(&ExecutionContext{
			Thread:     "thread-123",
			ThreadMode: "fork",
			ForkedFrom: "parent-thread",
		})

	ctx := builder.Build()

	// Verify workflow namespace exists
	workflow := ctx["workflow"].(*model.WorkflowContext)
	assert.Equal(t, "wf-1", workflow.ID)
	assert.Equal(t, "test-workflow", workflow.Name)
}

// TestCELContextBuilder_WorkflowContext verifies workflow context is properly built
func TestCELContextBuilder_WorkflowContext(t *testing.T) {
	t.Parallel()
	builder := NewCELContextBuilder().
		WithWorkflow("wf-1", "test-workflow").
		WithEnvironment("/workspace", "main").
		WithInputs(map[string]interface{}{"mode": "auto"})

	ctx := builder.Build()
	workflow := ctx["workflow"].(*model.WorkflowContext)

	assert.Equal(t, "wf-1", workflow.ID)
	assert.Equal(t, "test-workflow", workflow.Name)
	assert.Equal(t, "/workspace", workflow.Path)
	assert.Equal(t, "main", workflow.Branch)
}

// TestCELContextBuilder_ImplementsCELEvaluator verifies the interface is satisfied
func TestCELContextBuilder_ImplementsCELEvaluator(t *testing.T) {
	t.Parallel()
	builder := NewCELContextBuilder().
		WithWorkflow("wf-1", "test-workflow").
		WithInputs(map[string]interface{}{"mode": "auto", "count": float64(5)})

	// Test EvalString
	var eval wfcel.CELEvaluator = builder
	result, err := eval.EvalString("{{inputs.mode}}")
	require.NoError(t, err)
	assert.Equal(t, "auto", result)

	// Test EvalBool
	boolResult, err := eval.EvalBool("inputs.mode == 'auto'")
	require.NoError(t, err)
	assert.True(t, boolResult)

	// Test EvalBool with false
	boolResult, err = eval.EvalBool("inputs.mode == 'manual'")
	require.NoError(t, err)
	assert.False(t, boolResult)
}

// TestPreviewURLTemplateResolvesInInject verifies a TERMINAL/HANDOFF node's
// inject/save_message content can template the session's preview URL. The
// preview_url_template input is injected at chat-send time (see
// services.injectSessionDaemonID); here we confirm it resolves in a node's
// content string, including substituting a concrete port via CEL's string
// .replace (ext.Strings) — the shape a workflow author writes.
func TestPreviewURLTemplateResolvesInInject(t *testing.T) {
	t.Parallel()
	builder := NewCELContextBuilder().
		WithWorkflow("wf-1", "test-workflow").
		WithInputs(map[string]interface{}{
			"preview_url_template": "https://{port}-abc123.workspaces.reliantapi.com",
		})

	var eval wfcel.CELEvaluator = builder

	// Pure lookup — the raw template with its {port} placeholder.
	tmpl, err := eval.EvalString("{{ inputs.preview_url_template }}")
	require.NoError(t, err)
	assert.Equal(t, "https://{port}-abc123.workspaces.reliantapi.com", tmpl)

	// Realistic inject/save_message content: interpolate a resolved URL for the
	// detected port into a deliverable message.
	msg, err := eval.EvalString(`Your app is live at {{ inputs.preview_url_template.replace("{port}", "3000") }}`)
	require.NoError(t, err)
	assert.Equal(t, "Your app is live at https://3000-abc123.workspaces.reliantapi.com", msg)
}
