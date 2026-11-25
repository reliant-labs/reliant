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

// The reviewer session reaches every context derived from it, and only a
// reviewer session's model calls are narrowed.
func TestReviewerSession_ClampsEveryModelCallBelowIt(t *testing.T) {
	t.Parallel()
	root := NewExecutionContext("wf", "chat", "rv", "thread-root")
	session := reviewerSession(root)
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
