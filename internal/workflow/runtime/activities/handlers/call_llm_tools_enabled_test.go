// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The regression these guard, which took every shipped agent's tools away.
//
// `filter` was renamed to `preloaded_tools`, and the builtin workflows were
// migrated. The tool-expansion body learned to read both names — but the gate
// deciding whether tools were enabled AT ALL kept reading only
// `tc.GetFilter()`. That is nil for every migrated workflow, so the gate said
// "disabled", the model was handed an empty tool list, and it stayed that way
// for every agent run.
//
// What it looked like is worth recording, because it named neither tools nor
// the gate. A model given no tools does not fail — it writes down the call it
// wanted to make: "`[Tool call: bash]`" followed by a JSON blob, as markdown
// prose, using a tool name this product does not even have ("bash"; ours is
// "shell") because with no tool list to work from it reached for whatever its
// training suggested. The UI then renders text as text, which looks like
// broken tool rendering, and the turn — carrying no tool_use — legitimately
// ends, which looks like a broken agent loop. Two wrong-looking subsystems,
// one nil check.

func TestToolsConfigEnablesTools(t *testing.T) {
	t.Parallel()

	list := func(items ...string) *reliantv1.CelStringList {
		return &reliantv1.CelStringList{
			Value: &reliantv1.CelStringList_Literal{
				Literal: &reliantv1.StringList{Values: items},
			},
		}
	}

	tests := []struct {
		name string
		tc   *reliantv1.ToolsConfig
		want bool
	}{
		{
			name: "no tools_config at all means no tools",
			tc:   nil,
			want: false,
		},
		{
			name: "a tools_config that declares nothing means no tools",
			tc:   &reliantv1.ToolsConfig{},
			want: false,
		},
		{
			// THE REGRESSION. Every builtin workflow looks like this.
			name: "preloaded_tools alone enables tools",
			tc:   &reliantv1.ToolsConfig{PreloadedTools: list("tag:default")},
			want: true,
		},
		{
			name: "the retired filter alias still enables tools",
			tc:   &reliantv1.ToolsConfig{Filter: list("tag:default")},
			want: true,
		},
		{
			// A node may preload nothing and expect the model to reach for
			// tools on demand; that still needs load_tool offered.
			name: "loadable_tools alone enables tools",
			tc:   &reliantv1.ToolsConfig{LoadableTools: list("*")},
			want: true,
		},
		{
			// Set-but-empty means "this node declares an empty toolset", which
			// expansion resolves to zero tools anyway. Only UNSET means
			// "not declared" — so this must not be confused with nil.
			name: "set-but-empty preloaded_tools is declared, not absent",
			tc:   &reliantv1.ToolsConfig{PreloadedTools: list()},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, toolsConfigEnablesTools(tt.tc))
		})
	}
}

// TestBuiltinWorkflowsEnableTools applies the REAL gate to the REAL shipped
// workflows, which is the only version of this test that would have caught the
// regression. A unit test on the gate in isolation passes whichever field name
// it happens to check; what broke was the pairing of this gate with what the
// shipped YAML actually says. So this reads the workflows we ship and asserts
// each one's agent can call something.
func TestBuiltinWorkflowsEnableTools(t *testing.T) {
	t.Parallel()

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
			require.NoError(t, err)
			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err, "builtin workflow must parse")

			for _, tc := range allCallLLMToolsConfigs(wf.GetNodes()) {
				assert.True(t, toolsConfigEnablesTools(tc),
					"a call_llm node in %s declares a tools_config that the gate reads as "+
						"'no tools'. If a tool-declaring field was added or renamed, teach "+
						"toolsConfigEnablesTools about it — otherwise this workflow's agent "+
						"is handed an empty tool list and will narrate tool calls as prose.",
					name)
			}
		})
		checked++
	}

	require.NotZero(t, checked, "expected to find builtin workflow YAML to check")
}

// allCallLLMToolsConfigs collects every call_llm tools_config in a workflow,
// descending into inline sub-workflows and loops. Every one is collected
// rather than just the first: a workflow can configure several agents, and it
// is the one nobody looked at that goes quiet.
func allCallLLMToolsConfigs(nodes []*reliantv1.Node) []*reliantv1.ToolsConfig {
	var found []*reliantv1.ToolsConfig
	for _, node := range nodes {
		if args := node.GetCallLlm(); args != nil && args.GetToolsConfig() != nil {
			found = append(found, args.GetToolsConfig())
		}
		if args := node.GetWorkflow(); args != nil && args.GetInline() != nil {
			found = append(found, allCallLLMToolsConfigs(args.GetInline().GetNodes())...)
		}
		if args := node.GetLoop(); args != nil && args.GetInline() != nil {
			found = append(found, allCallLLMToolsConfigs(args.GetInline().GetNodes())...)
		}
	}
	return found
}
