// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const optionalNodeSelectSrc = `
name: optional_select
entry: [review]
nodes:
  - id: review
    type: workflow
    ref: builtin://some-child
  - id: decide
    type: compute
    outputs:
      pass: "{{ %s }}"
edges:
  - from: review
    default: decide
`

var optionalNodeSelectExprs = []string{
	"nodes.review.?response.?verdict.orValue('') == 'ok'",
	"nodes.?review.?response.?verdict.orValue('') == 'ok'",
	"nodes.?review.?response.orValue(null) != null",
	"nodes.?review.response.verdict.orValue('') == 'ok'",
	"has(nodes.review) && nodes.review.response.verdict == 'ok'",
}

// Bug report fd809b45: `nodes.?review.?response` against a workflow node
// whose child outputs are not resolved (no loader, or a ref the scenario
// runner black-boxes) failed to compile ("undefined field 'response'") while
// `nodes.review.?response` compiled. Both spellings read the same node, so
// they type-check the same: a node whose outputs are only known at run time
// is dyn either way.
func TestOptionalNodeSelect_TypeChecksLikePlainSelect(t *testing.T) {
	for _, expr := range optionalNodeSelectExprs {
		t.Run(expr, func(t *testing.T) {
			errs := computeFindings(t, fmt.Sprintf(optionalNodeSelectSrc, expr))
			assert.Empty(t, errs, "%v", messages(errs))
		})
	}
}

// With the child resolved, every spelling is checked against its outputs:
// a declared output compiles, an undeclared one is an error either way.
func TestOptionalNodeSelect_ResolvedChildIsTyped(t *testing.T) {
	loader := func(ref string) (*reliantv1.Workflow, error) {
		return wfyaml.ParseWorkflow([]byte(`
name: some-child
entry: [answer]
nodes:
  - id: answer
    type: compute
    outputs:
      verdict: "{{ 'ok' }}"
outputs:
  response: "{{ {'verdict': nodes.answer.verdict} }}"
`))
	}
	check := func(t *testing.T, expr string) []*Error {
		wf, err := wfyaml.ParseWorkflow([]byte(fmt.Sprintf(optionalNodeSelectSrc, expr)))
		require.NoError(t, err)
		return StaticAnalysisWithOptions(wf, &ValidationOptions{WorkflowLoader: loader}).Errors()
	}
	for _, expr := range optionalNodeSelectExprs {
		t.Run(expr, func(t *testing.T) {
			errs := check(t, expr)
			assert.Empty(t, errs, "%v", messages(errs))
		})
	}
	for _, expr := range []string{
		"nodes.review.?nope.orValue('') == 'ok'",
		"nodes.?review.?nope.orValue('') == 'ok'",
	} {
		t.Run(expr, func(t *testing.T) {
			assert.True(t, hasFinding(check(t, expr), "nope"), "an output the child does not declare is an error")
		})
	}
}
