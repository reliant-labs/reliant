// Copyright (c) 2026 Reliant Labs
package trigger

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

const gatedCardOrg = "55949942-3ee9-4cfc-bb7c-817bdba75186"

// gatedCardSummary is the approval card of a workflow whose agent declares
// tools, with gate deciding entitlement.
func gatedCardSummary(t *testing.T, gate featuregate.Gate, toolsConfig string) string {
	t.Helper()
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	root, err := wfyaml.ParseWorkflow([]byte(`name: gated-card
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
` + toolsConfig))
	require.NoError(t, err)
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo, Triggers: &MockTriggers{}, FiringReads: &MockFiringReads{}, Registrations: &MockRegistrationReads{}, Projects: projectaccesstest.For(repo), Gate: gate}))
	snap, err := svc.activationSnapshot(context.Background(), "owner", &pb.TriggerDefinition{Workflow: "project://gated-card"}, root, func() string { return gatedCardOrg })
	require.NoError(t, err)
	return strings.Join(snap.Summary, "\n")
}

// The approval card names a feature-gated tool only to an owner entitled to
// its feature in the activation's org; to anyone else a tag or "*" does not
// expand to it, exactly as if it did not exist. No real tool is gated yet, so
// websearch is gated for the test (tools.GateToolForTest: not parallel).
func TestActivationCardNamesGatedToolsOnlyToEntitledOwners(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	const byTag = "        preloaded_tools: [tag:web]\n        loadable_tools: [view]\n"

	entitled := &featuregate.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, user, org string) (bool, error) {
		return f == featuregate.GTM && user == "owner" && org == gatedCardOrg, nil
	}}
	assert.Contains(t, gatedCardSummary(t, entitled, byTag), tools.ToolWebSearch)
	assert.Equal(t, 1, entitled.CallCount("Allowed"), "asked once per feature")

	for name, gate := range map[string]featuregate.Gate{
		"not entitled": &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return false, nil }},
		"no gate":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			summary := gatedCardSummary(t, gate, byTag)
			assert.NotContains(t, summary, tools.ToolWebSearch)
			assert.Contains(t, summary, tools.ToolFetch, "the rest of the tag is still listed")
			assert.Equal(t, strings.ReplaceAll(summary, tools.ToolWebSearch, ""), summary)
		})
	}

	t.Run("a declaration that reaches no gated tool never asks", func(t *testing.T) {
		gate := &featuregate.MockGate{}
		gatedCardSummary(t, gate, "        preloaded_tools: [fetch]\n        loadable_tools: [view]\n")
		assert.Zero(t, gate.CallCount("Allowed"))
	})

	t.Run("a tool bound by name reads as an unknown one", func(t *testing.T) {
		const bound = "        preloaded_tools: [fetch]\n        loadable_tools: []\n        tools:\n          websearch: {query: x}\n"
		assert.NotContains(t, gatedCardSummary(t, nil, bound), "Fixed tool binding: "+tools.ToolWebSearch)
		entitled := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return true, nil }}
		assert.Contains(t, gatedCardSummary(t, entitled, bound), "Fixed tool binding: "+tools.ToolWebSearch)
	})
}

// No invoke_tool node can run a gated tool (none is node-exposed), so the
// card discloses one as it discloses any unknown tool.
func TestNodeDisclosureTreatsGatedToolsAsUnknown(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	node := func(tool string) *pb.Node {
		return &pb.Node{Id: "n", Type: "invoke_tool", Args: &pb.Node_InvokeTool{InvokeTool: &pb.InvokeToolArgs{Tool: &pb.CelString{Value: &pb.CelString_Literal{Literal: tool}}}}}
	}
	gated := strings.Join(nodePrivilegeDisclosure(node(tools.ToolWebSearch), nil, "", nil), "\n")
	unknown := strings.Join(nodePrivilegeDisclosure(node("zz_no_such_tool"), nil, "", nil), "\n")
	assert.Equal(t, unknown, gated)
}
