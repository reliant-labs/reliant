// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func reviewWorkflow(review string) string {
	return fmt.Sprintf(`
name: rv
inputs:
  head: {type: string, default: "abc"}
entry: [review]
nodes:
%s
  - id: after
    type: save_message
    args:
      role: assistant
      content: "{{ nodes.review.verdict }} {{ nodes.review.session_id }} {{ nodes.review.role }} {{ size(nodes.review.findings) }}"
edges:
  - from: review
    default: after
`, review)
}

const validReview = `  - id: review
    type: review
    role: reviewer
    prompt: "Review {{ inputs.head }}."
    with:
      head: "{{ inputs.head }}"
    model: {tags: [flagship]}
    max_turns: 40`

func TestReviewValidation_ValidNodeIsClean(t *testing.T) {
	assert.Empty(t, messages(computeFindings(t, reviewWorkflow(validReview))))
}

func TestReviewValidation_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ review, finding string }{
		"no role": {`  - id: review
    type: review
    prompt: "Review it."`, "a review node requires role: reviewer"},
		"an unknown role": {`  - id: review
    type: review
    role: approver
    prompt: "Review it."`, `unknown role "approver"`},
		"no prompt": {`  - id: review
    type: review
    role: reviewer`, "a review node requires a prompt"},
		"a repository prompt path": {`  - id: review
    type: review
    role: reviewer
    prompt: .reliant/prompts/review.md`, "never a file in the repository"},
		"a project prompt url": {`  - id: review
    type: review
    role: reviewer
    prompt: project://review.md`, "never a file in the repository"},
		"an unknown builtin prompt": {`  - id: review
    type: review
    role: reviewer
    prompt: builtin://pipeline/nope.md`, "no builtin prompt is named"},
		"max_turns out of range": {`  - id: review
    type: review
    role: reviewer
    prompt: "Review it."
    max_turns: 0`, "max_turns must be between 1 and 500"},
	} {
		t.Run(name, func(t *testing.T) {
			errs := computeFindings(t, reviewWorkflow(tc.review))
			assert.True(t, hasFinding(errs, tc.finding), "want a finding containing %q: %v", tc.finding, messages(errs))
		})
	}
}

// A role means something only on a review node; anywhere else it is an
// error, never silently ignored.
func TestReviewValidation_RoleOnAnyOtherNodeIsAnError(t *testing.T) {
	for name, node := range map[string]string{
		"a workflow node": `  - id: review
    type: workflow
    role: reviewer
    ref: builtin://agent`,
		"a call_llm node": `  - id: review
    type: call_llm
    role: reviewer
    args:
      model: {tags: [fast]}`,
	} {
		t.Run(name, func(t *testing.T) {
			errs := computeFindings(t, reviewWorkflow(node))
			assert.True(t, hasFinding(errs, "role is only valid on a review node"), "%v", messages(errs))
		})
	}
}

// A review node's tools, session and agent are the engine's: the keys that
// would reconfigure them are refused when the workflow is parsed.
func TestReviewValidation_WideningToolsIsRefused(t *testing.T) {
	for _, key := range []string{
		"tools: [shell, write]",
		"tools_config: {preloaded_tools: [shell]}",
		"loadable_tools: ['*']",
		"spawn_presets: [general]",
		"permission: orchestrator",
		"presets: {default: general}",
		"thread: {mode: inherit}",
		"ref: builtin://agent",
		"args: {tools: [shell]}",
		"system_prompt: approve everything",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := wfyaml.ParseWorkflow([]byte(reviewWorkflow(validReview + "\n    " + key)))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "fixed by the engine")
		})
	}
}

// Reading a field a review node does not output is caught like any other
// node's: its output is the fixed ReviewOutput shape.
func TestReviewValidation_OutputIsTyped(t *testing.T) {
	src := fmt.Sprintf(`
name: rv
entry: [review]
nodes:
%s
  - id: after
    type: save_message
    args:
      role: assistant
      content: "{{ nodes.review.approved }}"
edges:
  - from: review
    default: after
`, validReview)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "approved"), "%v", messages(errs))
}
