// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
)

// A single value for a list input is read as a one-item list only when the
// item type is declared and the value is a valid item; every other value is
// left for validation to refuse.
func TestCoerceSingleToList(t *testing.T) {
	wf := typedWorkflow(t, `  repos: {type: github.repository, multi: true, required: true, min_items: 1}
  one: {type: github.repository, default: acme/web}
  modes: {type: enum, multi: true, enum: [fast, slow], default: [fast]}
  mode: {type: enum, enum: [fast, slow], default: fast}
  helpers: {type: preset, tags: [agent], multi: true, default: []}
  tools: {type: tools, default: []}
  labels: {type: array, default: []}
  agent:
    type: group
    inputs:
      repos: {type: github.repository, multi: true, default: []}
`, "")
	gh := valuetypestest.GitHub()

	tests := []struct {
		name    string
		values  map[string]any
		want    map[string]any
		coerced []ListCoercion
	}{
		{
			name:    "a repository for a list of repositories",
			values:  map[string]any{"repos": "reliant-labs/control-plane"},
			want:    map[string]any{"repos": []any{"reliant-labs/control-plane"}},
			coerced: []ListCoercion{{Input: "repos", Type: "github.repository"}},
		},
		{
			name:   "a list is left as it is",
			values: map[string]any{"repos": []any{"acme/web", "acme/api"}},
			want:   map[string]any{"repos": []any{"acme/web", "acme/api"}},
		},
		{
			name:   "a value that is not a valid item is not wrapped",
			values: map[string]any{"repos": "not a repository"},
			want:   map[string]any{"repos": "not a repository"},
		},
		{
			name:   "a single-valued input is never a list",
			values: map[string]any{"one": "acme/web", "mode": "fast"},
			want:   map[string]any{"one": "acme/web", "mode": "fast"},
		},
		{
			name:    "an allowed enum value for a multi enum",
			values:  map[string]any{"modes": "slow"},
			want:    map[string]any{"modes": []any{"slow"}},
			coerced: []ListCoercion{{Input: "modes", Type: "enum"}},
		},
		{
			name:   "an enum value the input does not allow",
			values: map[string]any{"modes": "medium"},
			want:   map[string]any{"modes": "medium"},
		},
		{
			name:    "a preset and a tool",
			values:  map[string]any{"helpers": "general", "tools": "view"},
			want:    map[string]any{"helpers": []any{"general"}, "tools": []any{"view"}},
			coerced: []ListCoercion{{Input: "helpers", Type: "preset"}, {Input: "tools", Type: "tools"}},
		},
		{
			name:   "an untyped array declares no item to read one as",
			values: map[string]any{"labels": "bug"},
			want:   map[string]any{"labels": "bug"},
		},
		{
			name:   "an empty string is no item",
			values: map[string]any{"tools": "  "},
			want:   map[string]any{"tools": "  "},
		},
		{
			name:    "an input inside a group",
			values:  map[string]any{"agent": map[string]any{"repos": "acme/web"}},
			want:    map[string]any{"agent": map[string]any{"repos": []any{"acme/web"}}},
			coerced: []ListCoercion{{Input: "agent.repos", Type: "github.repository"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CoerceSingleToList(tt.values, wf.GetInputs(), gh)
			assert.Equal(t, tt.coerced, got)
			assert.Equal(t, tt.want, tt.values)
		})
	}

	t.Run("what it wraps then validates", func(t *testing.T) {
		values := map[string]any{"repos": "reliant-labs/control-plane", "modes": "slow"}
		require.Len(t, CoerceSingleToList(values, wf.GetInputs(), gh), 2)
		assert.Empty(t, ValidateInputsWith(wf, values, gh).Errors())
	})
}
