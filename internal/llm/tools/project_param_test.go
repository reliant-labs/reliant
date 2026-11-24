// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// The list RoutesByProject reads is exactly the registry's built-in tools
// whose model-facing schema has `repo`: a new file or shell tool cannot be
// left unable to reach the chat's other projects, and an integration tool
// whose `repo` is a GitHub repository is never routed.
func TestProjectRoutedToolsAreTheRepoTakingTools(t *testing.T) {
	integration := map[string]bool{}
	for _, def := range GetToolRegistry() {
		for _, tag := range def.Tags {
			if tag == TagIntegration {
				integration[def.Name] = true
			}
		}
	}
	f := NewToolsFactory(&ToolsOptions{})
	var takesRepo []string
	for _, name := range f.ListAvailableTools() {
		if integration[name] {
			continue
		}
		tool := f.GetToolByName(name, nil)
		if tool != nil && hasParam(tool.ParamSchema(), "repo") {
			takesRepo = append(takesRepo, name)
		}
	}
	var routed []string
	for name := range projectRoutedTools {
		routed = append(routed, name)
	}
	sort.Strings(takesRepo)
	sort.Strings(routed)
	assert.Equal(t, takesRepo, routed)
}

func schemaJSON(t *testing.T, tool Tool) map[string]any {
	t.Helper()
	raw, err := json.Marshal(tool.ParamSchema())
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestWithProjectParam_AddsAnOptionalEnumOnlyToRepoTakingTools(t *testing.T) {
	f := NewToolsFactory(&ToolsOptions{})
	view := f.GetToolByName(ToolView, nil)
	require.NotNil(t, view)
	before, err := json.Marshal(view.ParamSchema())
	require.NoError(t, err)

	wrapped := WithProjectParam(view, []string{"app", "api"}, "app")
	props := schemaJSON(t, wrapped)["properties"].(map[string]any)
	project, ok := props[ProjectParam].(map[string]any)
	require.True(t, ok, "view gains `project`")
	assert.Equal(t, []any{"app", "api"}, project["enum"])
	assert.NotContains(t, schemaJSON(t, wrapped)["required"], ProjectParam)
	assert.Contains(t, props, "repo", "repo stays")

	after, err := json.Marshal(view.ParamSchema())
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after), "the tool's own schema is not changed")

	assert.Same(t, view, WithProjectParam(view, []string{"app"}, "app"), "one project: nothing to choose")
	plan := f.GetToolByName(ToolCreatePlan, nil)
	require.NotNil(t, plan)
	assert.Same(t, plan, WithProjectParam(plan, []string{"app", "api"}, "app"), "a tool without repo is unchanged")
}

func TestTakeProjectArg(t *testing.T) {
	value, rest, present, err := TakeProjectArg(`{"file_path":"a.go","project":"api","repo":"web"}`)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, "api", value)
	assert.JSONEq(t, `{"file_path":"a.go","repo":"web"}`, rest)

	in := `{"file_path": "a.go"}`
	_, rest, present, err = TakeProjectArg(in)
	require.NoError(t, err)
	assert.False(t, present)
	assert.Equal(t, in, rest, "input without project is passed byte for byte")

	_, rest, present, err = TakeProjectArg(`not json`)
	require.NoError(t, err)
	assert.False(t, present)
	assert.Equal(t, `not json`, rest)

	_, _, present, err = TakeProjectArg(`{"project":3}`)
	assert.True(t, present)
	assert.Error(t, err)
}

// The wrapper's permission check and run see the input without `project`, so
// the tool's strict decoder never rejects it.
func TestWithProjectParam_PermissionAndRunSeeNoProject(t *testing.T) {
	f := NewToolsFactory(&ToolsOptions{})
	view := f.GetToolByName(ToolView, nil)
	require.NotNil(t, view)
	wrapped := WithProjectParam(view, []string{"app", "api"}, "app")
	ctx := rctx.NewToolContext(t.Context(), "chat", "thread", nil, nil)

	_, err := view.RequiresPermission(ctx, ToolCall{Input: `{"file_path":"a.go","project":"api"}`})
	require.ErrorIs(t, err, ErrInvalidParameters, "the plain tool refuses project: why it must never reach it")
	_, err = wrapped.RequiresPermission(ctx, ToolCall{Input: `{"file_path":"a.go","project":"api"}`})
	require.NoError(t, err)
}
