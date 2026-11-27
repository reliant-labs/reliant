// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// builtin://review-agent, the body of every review node, declares exactly the
// reviewer allowlist, with nothing loadable and no spawn.
func TestReviewAgentDeclaresTheReviewerAllowlist(t *testing.T) {
	t.Parallel()
	tc := findCallLLMToolsConfig(t, loadBuiltin(t, "review-agent.yaml"))

	got := append([]string(nil), model.CelStringListValue(tc.GetPreloadedTools())...)
	want := append([]string(nil), model.ReviewerTools...)
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got, "review-agent.yaml and model.ReviewerTools must name the same tools")

	require.True(t, model.CelStringListIsSet(tc.GetLoadableTools()), "loadable_tools must be declared: unset means every tool")
	assert.Empty(t, model.CelStringListValue(tc.GetLoadableTools()))
	assert.Empty(t, model.CelStringListValue(tc.GetSpawn()))
	assert.Empty(t, tc.GetTools(), "no bindings: nothing to widen or rebind")

	caps := tools.ResolveCapabilities(tools.CapabilityInputs{
		Permission: model.CelStringValue(tc.GetPermission()),
		Access:     tools.ResolveToolAccess(got, nil, nil),
	})
	for _, write := range []string{tools.ToolWrite, tools.ToolEdit, "shell", tools.ToolAgent, tools.ToolLoadTool, "github__pr_create", "github__issue_comment"} {
		assert.False(t, caps.Offers(write), "a reviewer is never offered %s", write)
		assert.False(t, caps.CanLoad(write), "a reviewer can never load %s", write)
	}
}

// Every GitHub tool a reviewer holds is a read: the manifest says it does
// not mutate.
func TestReviewerGitHubToolsAreReads(t *testing.T) {
	t.Parallel()
	mutates := map[string]bool{}
	for _, m := range catalog.MustBuiltin().Manifests() {
		for _, a := range m.GetActions() {
			mutates[manifest.ToolName(m, a)] = a.GetMutates()
		}
	}
	for _, name := range model.ReviewerTools {
		if m, isIntegration := mutates[name]; isIntegration {
			assert.False(t, m, "%s mutates, and a reviewer only reads", name)
		} else {
			assert.Contains(t, []string{tools.ToolView, tools.ToolCodeContext, tools.ToolSkill}, name,
				"%s is neither a known read-only tool nor an integration read", name)
		}
	}
}

// The verdict schema the model is handed names the same values the engine
// accepts (model.BuildReviewOutput).
func TestReviewAgentVerdictSchemaMatchesTheEngine(t *testing.T) {
	t.Parallel()
	wf := loadBuiltin(t, "review-agent.yaml")
	var schema map[string]interface{}
	for _, n := range wf.GetNodes() {
		if inline := n.GetLoop().GetInline(); inline != nil {
			for _, inner := range inline.GetNodes() {
				if rt := inner.GetCallLlm().GetResponseTool(); rt != nil {
					assert.Equal(t, model.ReviewResponseTool, model.CelStringValue(rt.GetName()))
					schema = rt.GetSchema().AsMap()
				}
			}
		}
	}
	require.NotNil(t, schema)
	props := schema["properties"].(map[string]interface{})
	enum := func(m map[string]interface{}, key string) []interface{} {
		return m[key].(map[string]interface{})["enum"].([]interface{})
	}
	assert.ElementsMatch(t, []interface{}{model.ReviewVerdictPass, model.ReviewVerdictChanges, model.ReviewVerdictClose}, enum(props, "verdict"))
	assert.ElementsMatch(t, []interface{}{model.ReviewRiskLow, model.ReviewRiskMedium, model.ReviewRiskHigh}, enum(props, "risk_tier"))
	finding := props["findings"].(map[string]interface{})["items"].(map[string]interface{})
	assert.ElementsMatch(t, []interface{}{model.ReviewSeverityBlocker, model.ReviewSeverityNit}, enum(finding["properties"].(map[string]interface{}), "severity"))
	assert.Equal(t, false, schema["additionalProperties"])
	assert.Equal(t, false, finding["additionalProperties"])

	assert.NotContains(t, wf.GetInputs(), "tools", "the reviewer's tools are not an input")
	assert.NotContains(t, wf.GetInputs(), "system_prompt")
}

// A builtin prompt is named <group>/<name>.md; nothing else resolves.
func TestPromptFileNames(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"README.md", "../embed.go", "/pipeline/review.md", "pipeline/../README.md", "pipeline/review.txt", "pipeline/missing.md", ""} {
		_, err := builtin.PromptFile(bad)
		assert.Error(t, err, bad)
	}
}
