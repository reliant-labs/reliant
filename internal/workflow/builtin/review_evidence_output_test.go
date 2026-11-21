// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// evalExpr evaluates a pure template expression to its value.
func evalExpr(t *testing.T, template string, nodes map[string]interface{}) interface{} {
	t.Helper()
	v, err := wfcel.EvaluateTemplate(template, &wfcel.EdgeEvalContext{
		Nodes:    nodes,
		Iter:     &model.IterContext{},
		Workflow: &model.WorkflowContext{ID: "wf", Name: "fixture"},
	})
	require.NoError(t, err, template)
	return v
}

// TestOneShotExposesTheReviewersInspectionEvidence: forge-one-shot's review
// schema requires inspection_evidence (the live URL, the pages snapshotted,
// the checks run), but the run returned only its project root, so nobody
// could see what a pass rested on (north-star AW-37). get-it-right now passes
// the reviewer's whole verdict up as review_response, and forge-one-shot
// exposes its inspection_evidence and grade.
func TestOneShotExposesTheReviewersInspectionEvidence(t *testing.T) {
	t.Parallel()
	attempt := mapAt(t, nodeByID(t, loadWorkflowYAML(t, "get-it-right.yaml"), "attempt"), "inline")
	loopOut, ok := mapAt(t, attempt, "outputs")["review_response"].(string)
	require.True(t, ok, "get-it-right's attempt loop must output review_response")

	evidence := map[string]interface{}{"live_url": "https://5173-d.example.test", "pages_inspected": []interface{}{map[string]interface{}{"path": "/", "observed": "tasks render"}}}
	verdict := map[string]interface{}{"grade": "pass", "strategy": "pass", "feedback": "ok", "inspection_evidence": evidence}
	assert.Equal(t, verdict, evalExpr(t, loopOut, map[string]interface{}{"review": map[string]interface{}{"response": verdict}}))
	assert.Empty(t, evalExpr(t, loopOut, map[string]interface{}{}), "no review yet is an empty verdict, not an error")

	oneShot := mapAt(t, loadWorkflowYAML(t, "forge-one-shot.yaml"), "outputs")
	out, ok := oneShot["inspection_evidence"].(string)
	require.True(t, ok, "forge-one-shot must output inspection_evidence")
	assert.Equal(t, evidence, evalExpr(t, out, map[string]interface{}{"build_app": map[string]interface{}{"review_response": verdict}}))
	assert.Empty(t, evalExpr(t, out, map[string]interface{}{"build_app": map[string]interface{}{"review_response": map[string]interface{}{}}}))
}
