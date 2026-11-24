package wfcel

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The observe scope is save_message's plus `node`, and `item` only under
// each:. Validation compiles observe expressions against exactly these
// namespaces, so the evaluator must bind the same ones.
func TestObserveContext_Scope(t *testing.T) {
	t.Parallel()
	ctx := &ObserveContext{
		Output: map[string]interface{}{"cut": false, "hold": "main_unchanged"},
		Inputs: map[string]interface{}{"dry_run": false},
		Node:   &model.ObservedNode{ID: "plan", Type: "compute", Path: "plan", Attempt: 1, DurationMs: 42},
	}
	assert.ElementsMatch(t, []CELNamespace{CELOutput, CELInputs, CELTrigger, CELWorkflow, CELIter, CELNode}, ctx.Namespaces())

	got, err := EvaluateTemplate("{{ output.cut ? 'cut' : output.hold }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "main_unchanged", got)
	got, err = EvaluateTemplate("{{ node.duration_ms + node.attempt }}", ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 43, got, "a pure template keeps its native type")
	_, err = EvaluateTemplate("{{ item }}", ctx)
	assert.Error(t, err, "item exists only under each:")
	_, err = EvaluateTemplate("{{ nodes.plan.cut }}", ctx)
	assert.Error(t, err, "nodes is not in observe's scope")

	each := *ctx
	each.InEach, each.Item = true, map[string]interface{}{"number": 7}
	assert.Contains(t, each.Namespaces(), CELItem)
	got, err = EvaluateTemplate("{{ item.number }}", &each)
	require.NoError(t, err)
	assert.EqualValues(t, 7, got)

	bare := &ObserveContext{}
	got, err = EvaluateTemplate("{{ node.id == '' && node.attempt == 0 }}", bare)
	require.NoError(t, err)
	assert.Equal(t, true, got, "an unset node reads as zero values")
}
