package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoutingSystemPrompt(t *testing.T) {
	t.Run("custom system prompt override", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref:         "builtin://agent",
				Description: "General purpose agent",
			},
		}
		result := buildRoutingSystemPrompt(candidates, "You are a custom router.")
		assert.Contains(t, result, "You are a custom router.")
		assert.NotContains(t, result, defaultRoutingSystemPrompt)
	})

	t.Run("default system prompt when custom is empty", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref:         "builtin://agent",
				Description: "General purpose agent",
			},
		}
		result := buildRoutingSystemPrompt(candidates, "")
		assert.Contains(t, result, defaultRoutingSystemPrompt)
	})

	t.Run("multiple candidates with descriptions and presets", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref:         "builtin://agent",
				Description: "General purpose agent",
				RawYAML:     "name: agent\ndescription: General purpose agent",
				Presets: []*preset.Preset{
					{Name: "general", Description: "General purpose preset"},
					{Name: "researcher", Description: "Research specialist"},
				},
			},
			{
				Ref: "builtin://code-review",
				Workflow: &reliantv1.Workflow{
					Name:        "code-review",
					Description: "Code review workflow",
				},
				Presets: []*preset.Preset{
					{Name: "thorough", Description: "Thorough review"},
				},
			},
		}
		result := buildRoutingSystemPrompt(candidates, "")

		// Workflow refs
		assert.Contains(t, result, "`builtin://agent`")
		assert.Contains(t, result, "`builtin://code-review`")

		// Descriptions
		assert.Contains(t, result, "General purpose agent")
		assert.Contains(t, result, "Code review workflow")

		// Preset names and descriptions
		assert.Contains(t, result, "`general`")
		assert.Contains(t, result, "General purpose preset")
		assert.Contains(t, result, "`researcher`")
		assert.Contains(t, result, "Research specialist")
		assert.Contains(t, result, "`thorough`")
		assert.Contains(t, result, "Thorough review")

		// Raw YAML block
		assert.Contains(t, result, "```yaml")
		assert.Contains(t, result, "name: agent")
	})

	t.Run("candidate with workflow inputs", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref: "builtin://agent",
				Workflow: &reliantv1.Workflow{
					Name:        "agent",
					Description: "General purpose agent",
					Inputs: map[string]*reliantv1.Input{
						"model": {
							Type: "model",
							Config: &reliantv1.Input_ModelInput{
								ModelInput: &reliantv1.ModelInputConfig{
									Base: &reliantv1.InputBase{Description: "LLM model to use"},
								},
							},
						},
						"task": {
							Type: "string",
							Config: &reliantv1.Input_StringInput{
								StringInput: &reliantv1.StringInputConfig{
									Base: &reliantv1.InputBase{Description: "Task description"},
								},
							},
						},
					},
				},
			},
		}
		result := buildRoutingSystemPrompt(candidates, "")

		// Input field names should appear
		assert.Contains(t, result, "`model`")
		assert.Contains(t, result, "`task`")
		// Input descriptions
		assert.Contains(t, result, "LLM model to use")
		assert.Contains(t, result, "Task description")
	})

	t.Run("candidate uses workflow description when override is empty", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref:         "builtin://agent",
				Description: "", // empty override
				Workflow: &reliantv1.Workflow{
					Name:        "agent",
					Description: "Workflow-level description",
				},
			},
		}
		result := buildRoutingSystemPrompt(candidates, "")
		assert.Contains(t, result, "Workflow-level description")
	})
}

func TestBuildRoutingResponseSchema(t *testing.T) {
	t.Run("schema contains workflow enum with all candidate refs", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref: "builtin://agent",
				Presets: []*preset.Preset{
					{Name: "general"},
				},
			},
			{
				Ref: "builtin://code-review",
				Presets: []*preset.Preset{
					{Name: "thorough"},
				},
			},
		}
		schema := buildRoutingResponseSchema(candidates)

		props, ok := schema["properties"].(map[string]interface{})
		require.True(t, ok)

		wfProp, ok := props["workflow"].(map[string]interface{})
		require.True(t, ok)

		wfEnum, ok := wfProp["enum"].([]interface{})
		require.True(t, ok)
		assert.Contains(t, wfEnum, "builtin://agent")
		assert.Contains(t, wfEnum, "builtin://code-review")
	})

	t.Run("schema contains deduplicated preset enum", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref: "builtin://agent",
				Presets: []*preset.Preset{
					{Name: "general"},
					{Name: "researcher"},
				},
			},
			{
				Ref: "builtin://code-review",
				Presets: []*preset.Preset{
					{Name: "general"}, // duplicate
					{Name: "thorough"},
				},
			},
		}
		schema := buildRoutingResponseSchema(candidates)

		props := schema["properties"].(map[string]interface{})
		presetProp := props["preset"].(map[string]interface{})
		presetEnum := presetProp["enum"].([]interface{})

		// Should be deduplicated and sorted
		assert.Len(t, presetEnum, 3)
		assert.Equal(t, []interface{}{"general", "researcher", "thorough"}, presetEnum)
	})

	t.Run("schema has required fields", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{
				Ref:     "builtin://agent",
				Presets: []*preset.Preset{{Name: "default"}},
			},
		}
		schema := buildRoutingResponseSchema(candidates)

		required, ok := schema["required"].([]interface{})
		require.True(t, ok)
		assert.Contains(t, required, "workflow")
		assert.Contains(t, required, "preset")
		assert.Contains(t, required, "prompt")
		assert.Contains(t, required, "reasoning")
	})

	t.Run("schema deduplicates workflow refs", func(t *testing.T) {
		candidates := []routerWorkflowInfo{
			{Ref: "builtin://agent", Presets: []*preset.Preset{{Name: "a"}}},
			{Ref: "builtin://agent", Presets: []*preset.Preset{{Name: "b"}}}, // duplicate ref
		}
		schema := buildRoutingResponseSchema(candidates)

		props := schema["properties"].(map[string]interface{})
		wfProp := props["workflow"].(map[string]interface{})
		wfEnum := wfProp["enum"].([]interface{})

		assert.Len(t, wfEnum, 1)
		assert.Equal(t, "builtin://agent", wfEnum[0])
	})
}

func TestGetInputDescription(t *testing.T) {
	t.Run("string input", func(t *testing.T) {
		input := &reliantv1.Input{
			Type: "string",
			Config: &reliantv1.Input_StringInput{
				StringInput: &reliantv1.StringInputConfig{
					Base: &reliantv1.InputBase{Description: "A string field"},
				},
			},
		}
		assert.Equal(t, "A string field", getInputDescription(input))
	})

	t.Run("enum input", func(t *testing.T) {
		input := &reliantv1.Input{
			Type: "enum",
			Config: &reliantv1.Input_EnumInput{
				EnumInput: &reliantv1.EnumInputConfig{
					Base: &reliantv1.InputBase{Description: "Pick one option"},
				},
			},
		}
		assert.Equal(t, "Pick one option", getInputDescription(input))
	})

	t.Run("model input", func(t *testing.T) {
		input := &reliantv1.Input{
			Type: "model",
			Config: &reliantv1.Input_ModelInput{
				ModelInput: &reliantv1.ModelInputConfig{
					Base: &reliantv1.InputBase{Description: "LLM model to use"},
				},
			},
		}
		assert.Equal(t, "LLM model to use", getInputDescription(input))
	})

	t.Run("nil input returns empty string", func(t *testing.T) {
		assert.Equal(t, "", getInputDescription(nil))
	})

	t.Run("input with no config returns empty string", func(t *testing.T) {
		input := &reliantv1.Input{Type: "string"}
		assert.Equal(t, "", getInputDescription(input))
	})
}

func TestFilterPresetsByAllowed(t *testing.T) {
	allPresets := []*preset.Preset{
		{Name: "general", Description: "General purpose"},
		{Name: "researcher", Description: "Research specialist"},
		{Name: "coder", Description: "Code specialist"},
	}

	t.Run("empty allowed list returns all presets", func(t *testing.T) {
		result := filterPresetsByAllowed(allPresets, nil)
		assert.Len(t, result, 3)
		assert.Equal(t, allPresets, result)

		result = filterPresetsByAllowed(allPresets, []string{})
		assert.Len(t, result, 3)
	})

	t.Run("non-empty allowed list filters correctly", func(t *testing.T) {
		result := filterPresetsByAllowed(allPresets, []string{"general", "coder"})
		assert.Len(t, result, 2)
		assert.Equal(t, "general", result[0].Name)
		assert.Equal(t, "coder", result[1].Name)
	})

	t.Run("no matches returns empty slice", func(t *testing.T) {
		result := filterPresetsByAllowed(allPresets, []string{"nonexistent"})
		assert.Empty(t, result)
	})

	t.Run("single match", func(t *testing.T) {
		result := filterPresetsByAllowed(allPresets, []string{"researcher"})
		assert.Len(t, result, 1)
		assert.Equal(t, "researcher", result[0].Name)
	})
}
