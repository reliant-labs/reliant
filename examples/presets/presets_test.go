// Copyright (c) 2025 Reliant Labs

package presets

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// Preset represents the structure of a preset YAML file
type Preset struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Tag         string         `yaml:"tag"`
	Params      map[string]any `yaml:"params"`
}

// ValidAgentParams are the valid input parameter names for the agent workflow.
// These correspond to the inputs defined in agent.yaml.
var ValidAgentParams = map[string]bool{
	"mode":                 true,
	"model":                true,
	"temperature":          true,
	"thinking_level":       true,
	"tools":                true,
	"spawn_presets":        true,
	"system_prompt":        true,
	"max_turns":            true,
	"compaction_threshold": true,
	"planning_prompt":      true,
}

func TestPresetsLoad(t *testing.T) {
	entries, err := fs.ReadDir(builtin.BuiltinPresetsFS, "presets")
	require.NoError(t, err, "should be able to read presets directory")
	require.NotEmpty(t, entries, "presets directory should not be empty")

	var presetFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			presetFiles = append(presetFiles, entry.Name())
		}
	}

	require.NotEmpty(t, presetFiles, "should find at least one preset YAML file")
	t.Logf("Found %d preset files: %v", len(presetFiles), presetFiles)
}

func TestPresetsParseCorrectly(t *testing.T) {
	entries, err := fs.ReadDir(builtin.BuiltinPresetsFS, "presets")
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			content, err := fs.ReadFile(builtin.BuiltinPresetsFS, "presets/"+entry.Name())
			require.NoError(t, err, "should be able to read preset file")

			var preset Preset
			err = yaml.Unmarshal(content, &preset)
			require.NoError(t, err, "preset YAML should parse correctly")

			// Validate the struct was populated
			assert.NotEmpty(t, preset.Name, "preset must have a name")
			assert.NotEmpty(t, preset.Tag, "preset must have a tag")
			assert.NotNil(t, preset.Params, "preset must have params")
		})
	}
}

func TestPresetsHaveRequiredFields(t *testing.T) {
	presets := loadAllPresets(t)

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			// Required fields
			assert.NotEmpty(t, preset.Name, "name is required")
			assert.NotEmpty(t, preset.Tag, "tag is required")
			assert.NotNil(t, preset.Params, "params is required")

			// Name should match filename (without .yaml extension)
			expectedName := strings.TrimSuffix(name, ".yaml")
			assert.Equal(t, expectedName, preset.Name, "preset name should match filename")
		})
	}
}

func TestPresetsHaveValidTag(t *testing.T) {
	validTags := map[string]bool{
		"agent": true,
		// Add other valid tags as workflows are created
	}

	presets := loadAllPresets(t)

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			assert.True(t, validTags[preset.Tag],
				"preset tag %q is not valid (valid tags: %v)", preset.Tag, keys(validTags))
		})
	}
}

func TestPresetsHaveValidParams(t *testing.T) {
	presets := loadAllPresets(t)

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			// Get the valid params for this tag type
			validParams := getValidParamsForTag(preset.Tag)
			if validParams == nil {
				t.Skipf("no validation defined for tag %q", preset.Tag)
				return
			}

			// Check each param is valid for the target workflow
			for paramName := range preset.Params {
				assert.True(t, validParams[paramName],
					"param %q is not a valid input for workflow type %q", paramName, preset.Tag)
			}
		})
	}
}

func TestAgentPresetsHaveSystemPrompt(t *testing.T) {
	presets := loadAllPresets(t)

	for name, preset := range presets {
		if preset.Tag != "agent" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			systemPrompt, ok := preset.Params["system_prompt"]
			if !ok {
				// Some presets might intentionally not have a system prompt (e.g., using default)
				t.Logf("preset %q has no system_prompt (using default)", preset.Name)
				return
			}

			// If present, it should be a non-empty string
			sp, ok := systemPrompt.(string)
			require.True(t, ok, "system_prompt should be a string")
			assert.NotEmpty(t, sp, "system_prompt should not be empty if present")
		})
	}
}

func TestPresetNamesAreUnique(t *testing.T) {
	presets := loadAllPresets(t)

	names := make(map[string]string) // preset name -> filename
	for filename, preset := range presets {
		if existingFile, exists := names[preset.Name]; exists {
			t.Errorf("duplicate preset name %q found in %q and %q", preset.Name, existingFile, filename)
		}
		names[preset.Name] = filename
	}
}

func TestPresetToolsAreValid(t *testing.T) {
	presets := loadAllPresets(t)

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			tools, ok := preset.Params["tools"]
			if !ok {
				// Preset doesn't specify tools
				return
			}

			toolsList, ok := tools.([]any)
			if !ok {
				t.Errorf("tools should be a list, got %T", tools)
				return
			}

			for i, tool := range toolsList {
				toolStr, ok := tool.(string)
				require.True(t, ok, "tool at index %d should be a string", i)
				assert.NotEmpty(t, toolStr, "tool at index %d should not be empty", i)

				// Basic validation of tool format
				// Tools can be:
				// - Simple names: "view", "edit", "bash"
				// - Tag references: "tag:default", "tag:search", "tag:mcp"
				// - Spawn references: "spawn:..." (shouldn't be in tools list though)
				if strings.HasPrefix(toolStr, "spawn:") {
					t.Errorf("spawn references should not be in tools list: %q", toolStr)
				}
			}
		})
	}
}

func TestPresetSpawnPresetsAreValid(t *testing.T) {
	presets := loadAllPresets(t)

	// Build list of valid preset names
	validPresetNames := make(map[string]bool)
	for _, preset := range presets {
		validPresetNames[preset.Name] = true
	}

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			spawnPresets, ok := preset.Params["spawn_presets"]
			if !ok {
				// Preset doesn't specify spawn_presets
				return
			}

			spList, ok := spawnPresets.([]any)
			if !ok {
				t.Errorf("spawn_presets should be a list, got %T", spawnPresets)
				return
			}

			for i, sp := range spList {
				spStr, ok := sp.(string)
				require.True(t, ok, "spawn_preset at index %d should be a string", i)
				assert.True(t, validPresetNames[spStr],
					"spawn_preset %q at index %d is not a valid preset name", spStr, i)
			}
		})
	}
}

func TestPresetModelsAreValid(t *testing.T) {
	// Valid model ID prefixes (for explicit model selection)
	validModelPrefixes := []string{
		"claude-",
		"gpt-",
		"o1-",
		"gemini-",
		"vertex-",
		"grok-",
	}

	presets := loadAllPresets(t)

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			model, ok := preset.Params["model"]
			if !ok {
				// Preset uses default model
				return
			}

			// Model is now a ModelSelector object, not a string
			modelMap, ok := model.(map[string]interface{})
			require.True(t, ok, "model should be an object (ModelSelector), got %T", model)

			// Check for valid model selector: either id, tags, or provider
			hasID := false
			hasTags := false

			if id, ok := modelMap["id"].(string); ok && id != "" {
				hasID = true
				// Validate model ID prefix
				valid := false
				for _, prefix := range validModelPrefixes {
					if strings.HasPrefix(id, prefix) {
						valid = true
						break
					}
				}
				assert.True(t, valid, "model ID %q should start with a valid prefix (one of %v)", id, validModelPrefixes)
			}

			if tags, ok := modelMap["tags"].([]interface{}); ok && len(tags) > 0 {
				hasTags = true
				// Validate tags are strings (users can define their own tags)
				for _, tag := range tags {
					_, ok := tag.(string)
					require.True(t, ok, "tag should be a string")
				}
			}

			// Must have either id or tags
			assert.True(t, hasID || hasTags, "model selector must have either 'id' or 'tags'")
		})
	}
}

// loadAllPresets loads all preset YAML files and returns them as a map
func loadAllPresets(t *testing.T) map[string]Preset {
	t.Helper()

	entries, err := fs.ReadDir(builtin.BuiltinPresetsFS, "presets")
	require.NoError(t, err)

	presets := make(map[string]Preset)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		content, err := fs.ReadFile(builtin.BuiltinPresetsFS, "presets/"+entry.Name())
		require.NoError(t, err, "failed to read %s", entry.Name())

		var preset Preset
		err = yaml.Unmarshal(content, &preset)
		require.NoError(t, err, "failed to parse %s", entry.Name())

		presets[entry.Name()] = preset
	}

	return presets
}

// getValidParamsForTag returns the valid parameter names for a given workflow tag
func getValidParamsForTag(tag string) map[string]bool {
	switch tag {
	case "agent":
		return ValidAgentParams
	default:
		return nil
	}
}

// keys returns the keys of a map as a slice
func keys(m map[string]bool) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}
