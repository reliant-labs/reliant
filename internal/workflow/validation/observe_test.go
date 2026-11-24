package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observeFindings validates wf and returns its observe findings as
// "path: message", after checking that none of them is an error: an observe
// finding never blocks a run.
func observeFindings(t *testing.T, wf *reliantv1.Workflow) []string {
	t.Helper()
	result := StaticAnalysis(wf, nil)
	for _, e := range result.Errors() {
		assert.NotEqual(t, CategoryObserve, e.Category, "an observe finding is never an error: %s", e.Error())
		assert.NotContains(t, strings.Join(e.Path, "."), observeFieldKey, "a finding inside observe is never an error: %s", e.Error())
	}
	var out []string
	for _, e := range ObserveFindings(result) {
		assert.Equal(t, SeverityWarning, e.Severity, e.Error())
		out = append(out, strings.Join(e.Path, ".")+": "+e.Message)
	}
	return out
}

func observeFindingsYAML(t *testing.T, src string) []string {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return observeFindings(t, wf)
}

func TestObserve_ValidBlocksHaveNoFindings(t *testing.T) {
	t.Parallel()
	got := observeFindingsYAML(t, `
name: observed
inputs:
  dry_run: {type: boolean, default: false}
observe:
  metrics:
    - name: node.duration
      kind: histogram
      unit: ms
      value: "{{ node.duration_ms }}"
      when: node.type == 'run'
      attributes: { node_type: "{{ node.type }}", exit: "{{ output.?exit_code.orValue(-1) }}" }
entry: [plan]
nodes:
  - id: plan
    type: compute
    outputs:
      cut: "{{ !inputs.dry_run }}"
      hold: "{{ inputs.dry_run ? 'dry_run' : '' }}"
      prs: "{{ [1, 2] }}"
    observe:
      attributes: { path: "{{ node.path }}", attempt: "{{ node.attempt }}" }
      events:
        - name: decision
          when: "!output.cut"
          attributes:
            name: cut
            value: "{{ output.cut ? 'cut' : 'no_cut' }}"
            reason: "{{ output.hold }}"
        - name: pr
          each: "{{ output.prs }}"
          attributes: { n: "{{ item }}", run: "{{ workflow.run_id }}" }
      metrics:
        - name: rc_train.ticks
          kind: counter
          attributes: { decision: "{{ output.cut ? 'cut' : 'no_cut' }}" }
  - id: land
    type: run
    command: echo ok
    observe:
      attributes: { exit: "{{ output.exit_code }}" }
edges:
  - from: plan
    default: land
`)
	assert.Empty(t, got)
}

func TestObserve_ExpressionProblemsAreWarnings(t *testing.T) {
	t.Parallel()
	got := observeFindingsYAML(t, `
name: observed
entry: [first]
observe:
  attributes: { sibling: "{{ nodes.first.cut }}" }
nodes:
  - id: first
    type: compute
    outputs:
      cut: "{{ true }}"
  - id: plan
    type: compute
    outputs:
      cut: "{{ true }}"
    observe:
      attributes:
        typo: "{{ output.cutt }}"
        sibling: "{{ nodes.first.cut }}"
        node_typo: "{{ node.duration }}"
        no_item: "{{ item }}"
      events:
        - name: decision
          when: "{{ output.cut }}"
        - name: counted
          when: "1 + 1"
      metrics:
        - name: m
          kind: gauge
          value: "{{ undefined_name }}"
edges:
  - from: first
    default: plan
`)
	expect := []struct{ path, msg string }{
		{"nodes.[1](plan).observe.attributes.typo", "cutt"},
		{"nodes.[1](plan).observe.attributes.sibling", "references `nodes`"},
		{"nodes.[1](plan).observe.attributes.node_typo", "duration"},
		{"nodes.[1](plan).observe.attributes.no_item", "item"},
		{"nodes.[1](plan).observe.events.[0].when", "{{"},
		{"nodes.[1](plan).observe.events.[1].when", "bool"},
		{"nodes.[1](plan).observe.metrics.[0].value", "undefined_name"},
		{"observe.attributes.sibling", "references `nodes`"},
	}
	for _, want := range expect {
		found := false
		for _, g := range got {
			if strings.HasPrefix(g, "observed."+want.path+":") && strings.Contains(g, want.msg) {
				found = true
				break
			}
		}
		assert.True(t, found, "want a warning at %s mentioning %q; got:\n%s", want.path, want.msg, strings.Join(got, "\n"))
	}
}

// The YAML parser refuses a bad shape, but a definition built as a proto (the
// builder) reaches validation with whatever it holds: there the shape is a
// warning too, so a stored definition is never refused for it at run time.
func TestObserve_ShapeOfAProtoDefinitionIsAWarning(t *testing.T) {
	t.Parallel()
	wf := &reliantv1.Workflow{Name: "proto", Entry: []string{"n"}, Nodes: []*reliantv1.Node{{
		Id: "n", Type: model.NodeTypeCompute,
		Args: &reliantv1.Node_Compute{Compute: &reliantv1.ComputeArgs{}},
		Observe: &reliantv1.NodeObserve{
			Attributes: map[string]string{"Bad Key": "x"},
			Metrics:    []*reliantv1.ObserveMetric{{Name: "m", Kind: "summary"}},
		},
	}}}
	got := observeFindings(t, wf)
	assert.Contains(t, strings.Join(got, "\n"), "observe.attributes.Bad Key: attribute key")
	assert.Contains(t, strings.Join(got, "\n"), "observe.metrics.[0].kind: kind \"summary\"")
}

func TestObserve_MergedBlockOverItsRequestCapIsAWarning(t *testing.T) {
	t.Parallel()
	long := "{{ '" + strings.Repeat("x", 300) + "' }}"
	defaults := &reliantv1.NodeObserve{Attributes: map[string]string{}}
	node := &reliantv1.NodeObserve{Attributes: map[string]string{}}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		defaults.Attributes["d_"+k] = long
		node.Attributes["n_"+k] = long
	}
	wf := &reliantv1.Workflow{Name: "big", Entry: []string{"n"}, Observe: defaults, Nodes: []*reliantv1.Node{{
		Id: "n", Type: model.NodeTypeCompute,
		Args:    &reliantv1.Node_Compute{Compute: &reliantv1.ComputeArgs{}},
		Observe: node,
	}}}
	got := observeFindings(t, wf)
	assert.Contains(t, strings.Join(got, "\n"), "merged with the workflow's")

	wf.Observe = nil
	assert.Empty(t, observeFindings(t, wf), "each half alone fits")
}

func TestObserve_MergeOrder(t *testing.T) {
	t.Parallel()
	defaults := &reliantv1.NodeObserve{
		Attributes: map[string]string{"a": "d", "b": "d"},
		Events:     []*reliantv1.ObserveEvent{{Name: "first"}},
		Metrics:    []*reliantv1.ObserveMetric{{Name: "shared", Kind: "counter"}, {Name: "default_only", Kind: "counter"}},
	}
	node := &reliantv1.NodeObserve{
		Attributes: map[string]string{"b": "n"},
		Events:     []*reliantv1.ObserveEvent{{Name: "second"}},
		Metrics:    []*reliantv1.ObserveMetric{{Name: "shared", Kind: "gauge", Value: "1"}},
	}
	merged := model.MergeObserve(defaults, node)
	assert.Equal(t, map[string]string{"a": "d", "b": "n"}, merged.GetAttributes(), "attributes by key, the node wins")
	assert.Equal(t, "first", merged.GetEvents()[0].GetName(), "events: defaults first")
	assert.Equal(t, "second", merged.GetEvents()[1].GetName())
	require.Len(t, merged.GetMetrics(), 2)
	assert.Equal(t, "default_only", merged.GetMetrics()[0].GetName())
	assert.Equal(t, "gauge", merged.GetMetrics()[1].GetKind(), "metrics by name, the node wins")
	assert.Equal(t, "d", defaults.GetAttributes()["b"], "the inputs are not modified")
	assert.Nil(t, model.MergeObserve(nil, nil))
}
