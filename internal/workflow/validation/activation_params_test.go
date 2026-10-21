// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
)

const activationInputs = `  repos: {type: github.repository, multi: true, required: true, min_items: 1, max_items: 2}
  team: {type: string}
  mode: {type: string, default: platform}
  trusted: {type: github.user, multi: true, default: []}
  issue_number: {type: integer}
  review_model: {type: model, default: {tags: [powerful]}}
`

func issuesByInput(issues []ParamIssue) map[string]string {
	out := map[string]string{}
	for _, issue := range issues {
		out[issue.Input] = issue.Message
	}
	return out
}

func TestValidateActivationParams(t *testing.T) {
	wf := typedWorkflow(t, activationInputs, "")
	declared := wf.GetInputs()
	// The trigger sets issue_number from each event.
	mapped := map[string]string{"issue_number": "{{ trigger.payload.data.issue.number }}"}
	check := func(params map[string]any) map[string]string {
		return issuesByInput(ValidateActivationParamsWith(declared, mapped, params, valuetypestest.GitHub()))
	}

	t.Run("a required input nobody supplied is reported, the event-mapped one is not", func(t *testing.T) {
		got := check(nil)
		assert.Equal(t, map[string]string{"repos": "repos is required", "team": "team is required"}, got,
			"issue_number is mapped from the event; mode, trusted and review_model have defaults or are not param inputs")
	})
	t.Run("null counts as missing", func(t *testing.T) {
		assert.Contains(t, check(map[string]any{"repos": nil, "team": "x"}), "repos")
	})
	t.Run("max_items is enforced", func(t *testing.T) {
		got := check(map[string]any{"repos": []any{"a/b", "a/c", "a/d"}, "team": "x"})
		assert.Contains(t, got["repos"], "at most 2")
	})
	t.Run("min_items is enforced", func(t *testing.T) {
		got := check(map[string]any{"repos": []any{}, "team": "x"})
		assert.Contains(t, got["repos"], "at least 1")
	})
	t.Run("a value of the wrong shape is reported against its input", func(t *testing.T) {
		got := check(map[string]any{"repos": []any{"not-a-repo"}, "team": "x"})
		assert.Contains(t, got["repos"], "not a valid github.repository")
	})
	t.Run("a complete activation has no issues", func(t *testing.T) {
		assert.Empty(t, check(map[string]any{"repos": []any{"acme/web"}, "team": "x"}))
	})
	t.Run("an input the workflow does not declare is reported", func(t *testing.T) {
		got := check(map[string]any{"repos": []any{"acme/web"}, "team": "x", "nope": 1})
		assert.Contains(t, got["nope"], "is not an input of this workflow")
	})
	t.Run("a param of an event-mapped input is ignored, as at fire time", func(t *testing.T) {
		got := check(map[string]any{"repos": []any{"acme/web"}, "team": "x", "issue_number": "not an int"})
		assert.Empty(t, got, "the event's value always wins, so the stored one cannot be wrong")
	})
	t.Run("issues come in input-name order", func(t *testing.T) {
		issues := ValidateActivationParamsWith(declared, mapped, nil, valuetypestest.GitHub())
		require.Len(t, issues, 2)
		assert.Equal(t, []string{"repos", "team"}, []string{issues[0].Input, issues[1].Input})
	})
}

func TestAnEmptySelectionOfARequiredMultiInputNeedsAValue(t *testing.T) {
	wf := typedWorkflow(t, "  repos: {type: github.repository, multi: true, required: true}\n", "")
	got := issuesByInput(ValidateActivationParamsWith(wf.GetInputs(), nil, map[string]any{"repos": []any{}}, valuetypestest.GitHub()))
	assert.Equal(t, map[string]string{"repos": "repos needs at least 1 value"}, got, "an empty selection would watch nothing")
}

func TestOnlyANoDefaultOrRequiredInputIsRequired(t *testing.T) {
	wf := typedWorkflow(t, "  a: {type: string, default: x}\n  b: {type: string}\n  c: {type: string, required: true}\n", "")
	got := issuesByInput(ValidateActivationParams(wf.GetInputs(), nil, nil))
	assert.Equal(t, map[string]string{"b": "b is required", "c": "c is required"}, got)
}
