package lint

import (
	"path/filepath"
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveInvalid_NamesTheNodeAndThePlace(t *testing.T) {
	t.Parallel()
	got := findingsOf(Check(loadFixture(t, filepath.Join("testdata", RuleObserveInvalid, "bad.yaml"))), RuleObserveInvalid)
	require.Len(t, got, 2, "%v", got)
	byNode := map[string]Finding{}
	for _, f := range got {
		byNode[f.Node] = f
	}
	assert.True(t, strings.HasPrefix(byNode["plan"].Message, "events.[0].attributes.reason: "), byNode["plan"].Message)
	assert.Contains(t, byNode["plan"].Message, "reason")
	assert.True(t, strings.HasPrefix(byNode[""].Message, "attributes.first: "), "the workflow's defaults are reported on the workflow: %s", byNode[""].Message)
	assert.Contains(t, byNode[""].Message, "`nodes`")
}

func TestObserveFindingLocation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path        []string
		node, where string
	}{
		{[]string{"wf", "nodes", "[1](plan)", "observe", "events", "[0]", "when"}, "plan", "events.[0].when"},
		{[]string{"nodes", "plan", "observe"}, "plan", ""},
		{[]string{"wf", "observe", "attributes", "x"}, "", "attributes.x"},
		{[]string{"nodes", "loop", "inline", "nodes", "body", "observe", "metrics", "[0]", "kind"}, "loop.body", "metrics.[0].kind"},
		{[]string{"wf", "on_failure", "nodes", "[0](notify)", "observe", "attributes", "a"}, "on_failure.notify", "attributes.a"},
		{[]string{"on_failure", "observe", "attributes", "a"}, "", "on_failure.observe.attributes.a"},
	} {
		node, where := observeFindingLocation(tc.path)
		assert.Equal(t, tc.node, node, "%v", tc.path)
		assert.Equal(t, tc.where, where, "%v", tc.path)
	}
}

func TestObserveDecisionUnrecorded(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, src string) []Finding {
		t.Helper()
		wf, err := wfyaml.ParseWorkflow([]byte(src))
		require.NoError(t, err)
		return findingsOf(Check(Input{Workflow: wf, Source: []byte(src)}), RuleObserveDecision)
	}

	t.Run("a run node named like a decision whose output is read", func(t *testing.T) {
		got := check(t, `
name: fixture
entry: [decide]
nodes:
- id: decide
  type: run
  timeout: 1m
  args: {command: 'echo take'}
- id: act
  type: save_message
  args: {role: assistant, content: '{{ nodes.decide.stdout }} {{ nodes.decide.exit_code }}'}
edges:
- {from: decide, default: act}
`)
		require.Len(t, got, 1, "%v", got)
		assert.Equal(t, "decide", got[0].Node)
		assert.Equal(t, SeverityWarning, got[0].Severity)
	})

	t.Run("a decision nothing reads is not flagged", func(t *testing.T) {
		assert.Empty(t, check(t, `
name: fixture
entry: [plan]
nodes:
- id: plan
  type: compute
  outputs: {skip: '{{ true }}', summary: '{{ "x" }}'}
- id: report
  type: save_message
  args: {role: assistant, content: '{{ nodes.plan.summary }}'}
edges:
- {from: plan, default: report}
`))
	})

	t.Run("a workflow default that records an event covers every node", func(t *testing.T) {
		assert.Empty(t, check(t, `
name: fixture
entry: [plan]
observe:
  events:
  - name: decision
    when: has(output.verdict)
    attributes: {value: '{{ output.verdict }}'}
nodes:
- id: plan
  type: compute
  outputs: {verdict: '{{ "pass" }}'}
- id: report
  type: save_message
  args: {role: assistant, content: '{{ nodes.plan.verdict }}'}
edges:
- {from: plan, default: report}
`))
	})

	t.Run("an inline body's node is named by its path, and lint:allow silences it", func(t *testing.T) {
		src := `
name: fixture
entry: [sweep]
nodes:
- id: sweep
  type: loop
  args:
    items: '{{ [1, 2] }}'
    inline:
      name: body
      entry: [route]
      nodes:
      # lint:allow observe-decision-unrecorded - the route is the loop's only output and the run view shows it
      - id: route
        type: compute
        outputs: {give_up: '{{ iter.item > 1 }}'}
      - id: say
        type: save_message
        args: {role: assistant, content: '{{ nodes.route.give_up }}'}
      edges:
      - {from: route, default: say}
`
		wf, err := wfyaml.ParseWorkflow([]byte(src))
		require.NoError(t, err)
		unsilenced := findingsOf(Check(Input{Workflow: wf}), RuleObserveDecision)
		require.Len(t, unsilenced, 1, "%v", unsilenced)
		assert.Equal(t, "sweep.route", unsilenced[0].Node)
		assert.Empty(t, check(t, src))
	})
}

func TestNamesADecision(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"cut": true, "model_review": false, "skip_reason": true, "give_up": true, "decide": true,
		"summary": false, "cutoff": false, "land-it": true, "Verdict": true, "labels": false,
	} {
		assert.Equal(t, want, namesADecision(name), name)
	}
}
