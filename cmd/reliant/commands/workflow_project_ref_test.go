// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #623: the CLI and the app disagreed about project workflows. These
// tests pin the CLI half of each disagreement; the app half is pinned beside
// the code it exercises (handlers.TestLoadWorkflowActivity_ProjectRef*,
// daemonruntime.TestProjectLayout_*), and workflowref's tests pin the rule
// itself.

// writeWorkflowsDir lays files out under a fresh .reliant/workflows directory
// and returns its path.
func writeWorkflowsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".reliant", "workflows")
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return dir
}

// pipelineWorkflow is named differently from the file it lives in, which is
// the whole of bug 1: the app addresses it as project://blog-content-pipeline.
const pipelineWorkflow = `name: blog-content-pipeline
apiVersion: "1.0"
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: {tags: [fast]}
outputs:
  response_text: "{{nodes.draft.response_text}}"
`

const contentNextWorkflow = `name: content-next
apiVersion: "1.0"
entry: [draft_blog]
nodes:
  - id: draft_blog
    type: workflow
    ref: project://blog-content-pipeline
`

// Bug 1: project://X resolves by the workflow's name:, never by file name.
func TestScenarioRun_ProjectRefResolvesByNameNotFileName(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{
		"blog.yaml":         pipelineWorkflow,
		"content-next.yaml": contentNextWorkflow,
	})

	r, err := loadScenarioRunner(workflowWithScenarios{
		WorkflowFile: filepath.Join(dir, "content-next.yaml"),
		WorkflowName: "content-next",
		Source:       "project",
	})
	require.NoError(t, err)

	res := r.Run(&wfscenario.Scenario{
		Name: "opens_the_pipeline",
		Events: []wfscenario.SimulatedEvent{{
			Node:   "draft_blog.draft",
			Output: map[string]interface{}{"response_text": "drafted"},
		}},
		Expect: &wfscenario.Expectation{
			Outcome: wfscenario.OutcomeCompleted,
			Reached: []string{"draft_blog", "draft_blog.draft"},
		},
	})
	assert.Equal(t, wfscenario.StatusPassed, res.Status,
		"project://blog-content-pipeline must open blog.yaml (named blog-content-pipeline): %v", res.Mismatches)
}

// Bug 2: validate follows project:// refs transitively and names the chain.
func TestValidate_FollowsProjectRefsTransitively(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{
		"a.yaml": `name: a
apiVersion: "1.0"
entry: [to_b]
nodes:
  - id: to_b
    type: workflow
    ref: project://b
`,
		"b.yaml": `name: b
apiVersion: "1.0"
entry: [to_c]
nodes:
  - id: to_c
    type: workflow
    ref: project://c
`,
	})

	res := validateWorkflowFile(filepath.Join(dir, "a.yaml"), dir)
	require.False(t, res.Valid, "a.yaml reaches a missing workflow through b; it must not validate")
	joined := strings.Join(res.Errors, "\n")
	assert.Contains(t, joined, "a.yaml → project://b → project://c", "the error names the whole chain")
	assert.Contains(t, joined, `no project workflow is named "c"`)
}

// Bug 2, cycle half: a ref cycle is reported, not followed forever.
func TestValidate_ReportsRefCycle(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{
		"a.yaml": `name: a
apiVersion: "1.0"
entry: [to_b]
nodes:
  - id: to_b
    type: workflow
    ref: project://b
`,
		"b.yaml": `name: b
apiVersion: "1.0"
entry: [to_a]
nodes:
  - id: to_a
    type: workflow
    ref: project://a
`,
	})

	res := validateWorkflowFile(filepath.Join(dir, "a.yaml"), dir)
	require.False(t, res.Valid)
	joined := strings.Join(res.Errors, "\n")
	assert.Contains(t, joined, "a.yaml → project://b → project://a")
	assert.Contains(t, joined, "cycle")
}

// Bug 3: the CLI reads scenarios from <slug>/scenarios/, the layout the app
// indexes, keyed by the workflow's name: rather than its file name.
func TestScenarioDiscovery_ReadsSlugScenariosDir(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{
		"blog.yaml":         pipelineWorkflow,
		"content-next.yaml": contentNextWorkflow,
		"content-next/scenarios/opens_the_pipeline.yaml": `name: opens_the_pipeline
events:
  - node: draft_blog.draft
    output: {response_text: drafted}
expect:
  outcome: completed
  reached: [draft_blog, draft_blog.draft]
`,
		"blog-content-pipeline/scenarios/drafts.yaml": `name: drafts
events:
  - node: draft
    output: {response_text: drafted}
expect:
  outcome: completed
`,
	})

	workflows, err := discoverWorkflowsWithScenarios([]string{dir}, "", false)
	require.NoError(t, err)

	found := map[string][]string{}
	for _, wf := range workflows {
		for _, sc := range wf.Scenarios {
			found[wf.WorkflowName] = append(found[wf.WorkflowName], sc.Name)
		}
	}
	assert.Equal(t, map[string][]string{
		"content-next":          {"opens_the_pipeline"},
		"blog-content-pipeline": {"drafts"},
	}, found)

	require.NoError(t, runWorkflowScenarios(nil, []string{dir}, "", false, false, true, false, ""),
		"both scenarios pass when run from the CLI")
}

// Bug 3, migration half: the retired CLI layout is an error that names where
// the files belong, never a silent skip.
func TestScenarioDiscovery_OldLayoutNamesNewLocation(t *testing.T) {
	scenario := `name: drafts
events:
  - node: draft
    output: {response_text: drafted}
`
	for name, oldPath := range map[string]string{
		"scenarios dir keyed by file name": "scenarios/blog/drafts.yaml",
		"co-located _scenarios file":       "blog_scenarios.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeWorkflowsDir(t, map[string]string{
				"blog.yaml": pipelineWorkflow,
				oldPath:     scenario,
			})
			_, err := discoverWorkflowsWithScenarios([]string{dir}, "", false)
			require.Error(t, err, "%s is the retired layout", oldPath)
			assert.Contains(t, err.Error(), oldPath)
			assert.Contains(t, err.Error(), "blog-content-pipeline/scenarios/",
				"the error names the new location, keyed by the workflow's name:")
		})
	}
}
