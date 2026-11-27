// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

const reviewDefinition = `
name: rv
entry: [review]
nodes:
  - id: review
    type: review
    role: reviewer
    prompt: "Review it."
  - id: other
    type: workflow
    role: reviewer
    ref: builtin://agent
edges:
  - from: review
    default: other
`

// Every runtime load hands the executors the lowered definition: no
// executor ever sees a review node, and a role anywhere else is dropped.
func TestLoadWorkflow_LowersReviewNodes(t *testing.T) {
	t.Parallel()
	wf, err := wfyaml.ParseWorkflow([]byte(reviewDefinition))
	require.NoError(t, err)
	data, err := protojson.Marshal(wf)
	require.NoError(t, err)

	loaded, err := LoadWorkflow(data)
	require.NoError(t, err)
	assert.True(t, model.IsLoweredReviewNode(loaded.GetNodes()[0]))
	assert.Empty(t, loaded.GetNodes()[1].GetRole())

	resolved, err := ResolveAndParseWorkflow([]byte(reviewDefinition), map[string]interface{}{}, nil)
	require.NoError(t, err)
	assert.True(t, model.IsLoweredReviewNode(resolved.GetNodes()[0]), "the template path lowers too")
}

// loweredReviewNode is the review node of reviewDefinition as the runtime
// runs it, and a plain workflow node beside it.
func loweredReviewNode(t *testing.T) (review, other *reliantv1.Node) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(reviewDefinition))
	require.NoError(t, err)
	data, err := protojson.Marshal(wf)
	require.NoError(t, err)
	loaded, err := LoadWorkflow(data)
	require.NoError(t, err)
	return loaded.GetNodes()[0], loaded.GetNodes()[1]
}

// The reviewer session reaches every context derived from it, and only a
// reviewer session's model calls are narrowed.
func TestReviewerSession_ClampsEveryModelCallBelowIt(t *testing.T) {
	t.Parallel()
	review, _ := loweredReviewNode(t)
	root := NewExecutionContext("wf", "chat", "rv", "thread-root")
	session := workflowNodeContext(nil, &ChildWorkflowTracker{}, root, review, "review", model.ThreadModeNew, model.ReviewAgentRef, false)
	require.True(t, session.Reviewer)
	assert.False(t, root.Reviewer, "the caller's context is shared with siblings and stays unmarked")

	derived := []*ExecutionContext{
		session.ForChild("agent_loop", model.ThreadModeInherit, "review-agent", true),
		session.ForIteration(3, true),
		session.ForChildWorkflow("child", "x", model.ThreadModeNew, "agent", true),
		session.Clone(),
		session.withAmbientMemoryPolicy(&reliantv1.Node{}),
	}
	for i, ctx := range derived {
		assert.True(t, ctx.Reviewer, "derived context %d", i)
	}

	widened := func() *reliantv1.Node {
		return &reliantv1.Node{Id: "call_llm", Type: model.NodeTypeCallLLM, Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			ToolsConfig: &reliantv1.ToolsConfig{
				PreloadedTools: &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"view", "write", "shell"}}}},
			},
		}}}
	}

	inSession := widened()
	clampReviewerStep(derived[1], model.NodeTypeCallLLM, inSession)
	assert.Equal(t, []string{"view"}, model.CelStringListValue(inSession.GetCallLlm().GetToolsConfig().GetPreloadedTools()))

	outside := widened()
	clampReviewerStep(root, model.NodeTypeCallLLM, outside)
	assert.Equal(t, []string{"view", "write", "shell"}, model.CelStringListValue(outside.GetCallLlm().GetToolsConfig().GetPreloadedTools()),
		"a model call outside a reviewer session is untouched")
}

func TestReviewNodeOutputs_RecordsTheSession(t *testing.T) {
	t.Parallel()
	ctx := NewExecutionContext("wf", "chat", "rv", "thread-review")
	out, err := reviewNodeOutputs(map[string]interface{}{
		"response":  map[string]interface{}{"verdict": "close", "findings": []interface{}{}, "summary": "Superseded."},
		"completed": true,
	}, ctx)
	require.NoError(t, err)
	assert.Equal(t, "thread-review", out["session_id"])
	assert.Equal(t, model.RoleReviewer, out["role"])

	_, err = reviewNodeOutputs(map[string]interface{}{"response": nil, "completed": false}, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not submit a verdict")
}

// Every execution of a review node gets a thread of its own (owner,
// 2026-11-25): a second run of the node at the same position, the same node
// in another iteration of any enclosing loop, and the same node in another
// run of the chat. The derivation is a function of workflow state alone, so
// a fresh tracker (what a replay rebuilds) derives the same threads in the
// same order. A plain workflow node keeps ForChild's thread.
func TestReviewSession_FreshThreadPerExecution(t *testing.T) {
	t.Parallel()
	review, other := loweredReviewNode(t)
	root := NewExecutionContext("wf", "chat", "rv", "thread-root")
	outer := func(i int) *ExecutionContext { return root.ForIteration(i, true).WithLoop("rounds", i) }
	inner := func(i, j int) *ExecutionContext { return outer(i).ForIteration(j, true).WithLoop("fixes", j) }
	require.Equal(t, "rounds#1/fixes#0", inner(1, 0).LoopPath)

	derive := func(tracker *ChildWorkflowTracker) []string {
		var threads []string
		for _, parent := range []*ExecutionContext{root, root, outer(0), outer(1), inner(0, 0), inner(1, 0)} {
			ctx := workflowNodeContext(nil, tracker, parent, review, "rounds.review", model.ThreadModeNew, model.ReviewAgentRef, true)
			require.True(t, ctx.Reviewer)
			threads = append(threads, ctx.Thread)
		}
		return threads
	}
	threads := derive(&ChildWorkflowTracker{})
	seen := map[string]bool{root.Thread: true}
	for i, thread := range threads {
		assert.False(t, seen[thread], "execution %d reuses thread %s", i, thread)
		seen[thread] = true
	}
	assert.Equal(t, threads, derive(&ChildWorkflowTracker{}), "a replay derives the same threads")

	assert.NotEqual(t,
		reviewSessionThread("wf", "run-1", "review", "", 0),
		reviewSessionThread("wf", "run-2", "review", "", 0),
		"two runs of one chat share its workflow id, never a reviewer session")

	plain := workflowNodeContext(nil, &ChildWorkflowTracker{}, root, other, "other", model.ThreadModeNew, "builtin://agent", true)
	assert.Equal(t, root.ForChild("other", model.ThreadModeNew, "builtin://agent", true).Thread, plain.Thread)
	assert.False(t, plain.Reviewer)
}
