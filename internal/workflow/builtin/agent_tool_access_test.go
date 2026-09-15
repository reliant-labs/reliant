// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findCallLLMToolsConfig returns the tools_config of the first call_llm node,
// looking inside inline sub-workflows and loops.
func findCallLLMToolsConfig(t *testing.T, wf *reliantv1.Workflow) *reliantv1.ToolsConfig {
	t.Helper()

	var walk func(nodes []*reliantv1.Node) *reliantv1.ToolsConfig
	walk = func(nodes []*reliantv1.Node) *reliantv1.ToolsConfig {
		for _, node := range nodes {
			if args := node.GetCallLlm(); args != nil && args.GetToolsConfig() != nil {
				return args.GetToolsConfig()
			}
			if args := node.GetWorkflow(); args != nil && args.GetInline() != nil {
				if tc := walk(args.GetInline().GetNodes()); tc != nil {
					return tc
				}
			}
			if args := node.GetLoop(); args != nil && args.GetInline() != nil {
				if tc := walk(args.GetInline().GetNodes()); tc != nil {
					return tc
				}
			}
		}
		return nil
	}

	tc := walk(wf.GetNodes())
	require.NotNil(t, tc, "expected a call_llm node with a tools_config")
	return tc
}

func loadBuiltin(t *testing.T, name string) *reliantv1.Workflow {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	return wf
}

// TestBuiltinAgentCanLoadAnyTool is the end-to-end guard for a regression that
// shipped: the default coding agent must be able to reach generate_image.
//
// generate_image is deliberately not in tag:default — it spends real money on a
// provider the user may not have configured — and no builtin or preset names it
// anywhere, so load_tool is its only route. Enforcing the preloaded bundle at
// the load site closed that route and broke a shipped feature.
//
// This reads the REAL builtin rather than a synthetic filter, because the bug
// was not in the enforcement logic in isolation — it was in what the shipped
// workflow amounted to once that logic applied to it. A unit test on the
// enforcement alone passed the whole time.
func TestBuiltinAgentCanLoadAnyTool(t *testing.T) {
	t.Parallel()

	tc := findCallLLMToolsConfig(t, loadBuiltin(t, "agent.yaml"))

	require.NotNil(t, tc.GetLoadableTools(),
		"builtin://agent must declare loadable_tools so the intent is visible, not inferred")
	assert.Contains(t, model.CelStringListValue(tc.GetLoadableTools()), "*",
		"the coding agent must be able to load any tool; generate_image has no other route")
}

// TestBuiltinAgentPreloadsTheDefaultBundle pins the other half: widening what is
// loadable must not quietly widen what the model is HANDED. The starting bundle
// is what shapes the agent's behaviour, and it stays focused.
func TestBuiltinAgentPreloadsTheDefaultBundle(t *testing.T) {
	t.Parallel()

	tc := findCallLLMToolsConfig(t, loadBuiltin(t, "agent.yaml"))

	require.NotNil(t, tc.GetPreloadedTools(), "builtin://agent must declare preloaded_tools")
	assert.Contains(t, tc.GetPreloadedTools().GetExpr(), "inputs.tools",
		"the non-plan branch must still come from inputs.tools, which defaults to tag:default")
}

// TestAssistantBuiltinsDeclareLoadable keeps the fix from decaying. Every
// agentic builtin needs the wildcard; a new one that forgets it silently loses
// access to every tool outside its starting bundle, which is precisely the
// failure this replaced.
func TestAssistantBuiltinsDeclareLoadable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"agent.yaml", "structured-agent.yaml", "auditing-agent.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc := findCallLLMToolsConfig(t, loadBuiltin(t, name))
			require.NotNil(t, tc.GetLoadableTools(), "%s must declare loadable_tools", name)
			assert.Contains(t, model.CelStringListValue(tc.GetLoadableTools()), "*",
				"%s is an assistant and must be able to reach past its starting bundle", name)
		})
	}
}
