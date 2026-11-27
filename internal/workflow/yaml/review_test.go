package wfyaml

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reviewYAML = `
name: r
entry: [review]
nodes:
  - id: review
    type: review
    role: reviewer
    prompt: builtin://pipeline/review.md
    with:
      head: "{{ inputs.head }}"
      prior_findings: []
    max_turns: 30
  - id: note
    type: save_message
    role: user
    content: "flat args"
edges:
  - from: review
    default: note
`

func TestReviewNode_ParsesAndRoundTrips(t *testing.T) {
	wf, err := ParseWorkflow([]byte(reviewYAML))
	require.NoError(t, err)

	review := wf.GetNodes()[0]
	assert.Equal(t, "review", review.GetType())
	assert.Equal(t, "reviewer", review.GetRole())
	args := review.GetReview()
	require.NotNil(t, args)
	assert.Equal(t, "builtin://pipeline/review.md", args.GetPrompt().GetLiteral())
	assert.Equal(t, "{{ inputs.head }}", args.GetWith()["head"].GetStringValue())
	assert.Equal(t, int64(30), args.GetMaxTurns().GetLiteral())

	// A save_message node's flat `role:` is still its message's role, as it
	// always was, not the node's.
	note := wf.GetNodes()[1]
	assert.Empty(t, note.GetRole())
	assert.Equal(t, "user", note.GetSaveMessageNode().GetRole().GetLiteral())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.Equal(t, "reviewer", again.GetNodes()[0].GetRole())
	assert.Equal(t, "builtin://pipeline/review.md", again.GetNodes()[0].GetReview().GetPrompt().GetLiteral())
	assert.Empty(t, again.GetNodes()[1].GetRole())
	assert.Equal(t, "user", again.GetNodes()[1].GetSaveMessageNode().GetRole().GetLiteral())
}

// `role:` on any node that has no role arg is the node's role, so validation
// can refuse it there rather than the parser calling it an unknown key.
func TestRoleOnOtherNodesIsTheNodeRole(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`
name: r
entry: [a]
nodes:
  - id: a
    type: call_llm
    role: reviewer
    args:
      model: {tags: [fast]}
  - id: b
    type: workflow
    role: reviewer
    ref: builtin://agent
`))
	require.NoError(t, err)
	assert.Equal(t, "reviewer", wf.GetNodes()[0].GetRole())
	assert.Equal(t, "reviewer", wf.GetNodes()[1].GetRole())
	assert.Equal(t, "builtin://agent", wf.GetNodes()[1].GetWorkflow().GetRef().GetLiteral())
}
