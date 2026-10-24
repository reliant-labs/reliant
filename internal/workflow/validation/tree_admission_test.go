package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticAnalysisValidatesReferencedChildStructure(t *testing.T) {
	child, err := wfyaml.ParseWorkflow([]byte("name: child\nentry: [missing]\nnodes:\n  - {id: a, type: approval, args: {title: ok}}\n"))
	require.NoError(t, err)
	root := refChainWorkflow(t, "root", "project://child")
	result := StaticAnalysis(root, func(ref string) (*reliantv1.Workflow, error) {
		require.Equal(t, "project://child", ref)
		return child, nil
	})
	require.True(t, result.HasErrors(), "a child's invalid graph must block its parent")
	assert.Contains(t, result.Error(), "missing")
	assert.Contains(t, result.Error(), "child")
}

func TestStaticAnalysisChildAdvisoriesBelongToChild(t *testing.T) {
	child, err := wfyaml.ParseWorkflow([]byte(`name: child
entry: [a]
nodes:
  - {id: a, type: run, command: "true"}
outputs:
  result: "{{nodes.a}}"
`))
	require.NoError(t, err)
	// A child's findings must not be interpreted as node locations on the
	// parent canvas. Blocking errors are propagated, advisory warnings aren't.
	childResult := StaticAnalysis(child, nil)
	root := refChainWorkflow(t, "root", "project://child")
	parentResult := StaticAnalysis(root, func(string) (*reliantv1.Workflow, error) { return child, nil })
	assert.False(t, parentResult.HasErrors(), parentResult.Error())
	for _, warning := range childResult.Warnings() {
		for _, propagated := range parentResult.Warnings() {
			assert.NotEqual(t, warning.Error(), propagated.Error())
		}
	}
}

func TestStaticAnalysisCachesDefinitionPerCanonicalRef(t *testing.T) {
	root := refChainWorkflow(t, "root", "project://child")
	child := refChainWorkflow(t, "child", "project://root")
	calls := 0
	result := StaticAnalysisWithOptions(root, &ValidationOptions{
		CanonicalWorkflowRef: "project://root",
		WorkflowLoader: func(ref string) (*reliantv1.Workflow, error) {
			calls++
			require.LessOrEqual(t, calls, 2, "cyclic definitions must not cause unbounded validation")
			if ref == "project://child" {
				return child, nil
			}
			return root, nil
		},
	})
	assert.True(t, result.HasErrors(), "ref cycles remain invalid")
	assert.Contains(t, result.Error(), "cycle")
	assert.Equal(t, 1, calls, "root and alias are the same already-loaded definition")
}
