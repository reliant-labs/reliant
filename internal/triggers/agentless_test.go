package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func TestWorkflowRunsAgent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		src  string
		want bool
	}{
		"run and action steps only": {`name: chain
entry: [a]
nodes:
  - {id: a, type: run, args: {command: "true"}}
`, false},
		"a call_llm": {`name: agent
entry: [a]
nodes:
  - {id: a, type: call_llm, args: {model: {tags: [fast]}}}
`, true},
		"a child workflow": {`name: parent
entry: [a]
nodes:
  - {id: a, type: workflow, ref: builtin://agent}
`, true},
		"a call_llm inside an inline loop": {`name: looped
entry: [l]
nodes:
  - id: l
    type: loop
    while: "iter.iteration < 2"
    inline:
      entry: [c]
      nodes:
        - {id: c, type: call_llm, args: {model: {tags: [fast]}}}
`, true},
		"an inline loop of run steps": {`name: looped-run
entry: [l]
nodes:
  - id: l
    type: loop
    while: "iter.iteration < 2"
    inline:
      entry: [r]
      nodes:
        - {id: r, type: run, args: {command: "true"}}
`, false},
	}
	for name, tc := range cases {
		wf, err := wfyaml.ParseWorkflow([]byte(tc.src))
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, WorkflowRunsAgent(wf), name)
	}
}
