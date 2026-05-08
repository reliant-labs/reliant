// Copyright (c) 2025 Reliant Labs

package presets

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/llm/models"
	toolspkg "github.com/reliant-labs/reliant/internal/llm/tools"
	skillscatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// Preset represents the structure of a preset YAML file
type Preset struct {
	Name              string         `yaml:"name"`
	Description       string         `yaml:"description"`
	Tag               string         `yaml:"tag"`
	Params            map[string]any `yaml:"params"`
	RecommendedSkills []string       `yaml:"recommended_skills,omitempty"`
}

// ValidAgentParams are the valid input parameter names for the agent workflow.
// These correspond to the inputs defined in agent.yaml.
var ValidAgentParams = map[string]bool{
	"mode":            true,
	"model":           true,
	"tools":           true,
	"spawn_presets":   true,
	"system_prompt":   true,
	"max_turns":       true,
	"planning_prompt": true,
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

func TestAgentPresetsNoSystemPrompt(t *testing.T) {
	// Builtin presets should NOT set system_prompt — methodology comes from
	// recommended_skills and the base prompt is injected by call_llm.
	// Exception: workflow_builder uses system_prompt for draft ID reference.
	allowedExceptions := map[string]bool{
		"workflow_builder.yaml": true,
	}

	presets := loadAllPresets(t)

	for name, preset := range presets {
		if preset.Tag != "agent" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			_, hasSystemPrompt := preset.Params["system_prompt"]
			if allowedExceptions[name] {
				// Exception presets may have system_prompt
				return
			}
			assert.False(t, hasSystemPrompt,
				"preset %q should not set system_prompt — use recommended_skills instead", preset.Name)
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
	knownToolNames := knownRegistryToolNames()
	knownToolTags := knownRegistryToolTags()

	for name, preset := range presets {
		t.Run(name, func(t *testing.T) {
			toolsRaw, ok := preset.Params["tools"]
			if !ok {
				// Preset doesn't specify tools
				return
			}

			toolsList, ok := toolsRaw.([]any)
			if !ok {
				t.Errorf("tools should be a list, got %T", toolsRaw)
				return
			}

			for i, tool := range toolsList {
				toolStr, ok := tool.(string)
				require.True(t, ok, "tool at index %d should be a string", i)
				assert.NotEmpty(t, toolStr, "tool at index %d should not be empty", i)

				// Tools can be:
				// - Simple names: "view", "edit", "bash"
				// - Tag references: "tag:default", "tag:search", "tag:mcp"
				// - External MCP names: "mcp__server__tool"
				if strings.HasPrefix(toolStr, "spawn:") {
					t.Errorf("spawn references should not be in tools list: %q", toolStr)
					continue
				}
				if strings.HasPrefix(toolStr, "mcp__reliant__") {
					t.Errorf("preset tools must use built-in tool names, not Claude Code transport aliases: %q", toolStr)
					continue
				}
				if strings.HasPrefix(toolStr, "mcp__") {
					continue
				}
				if strings.HasPrefix(toolStr, "tag:") {
					tag := strings.TrimPrefix(toolStr, "tag:")
					assert.True(t, knownToolTags[tag], "tool tag %q at index %d is not registered", tag, i)
					continue
				}

				assert.True(t, knownToolNames[toolStr], "tool %q at index %d is not registered", toolStr, i)
			}
		})
	}
}

func knownRegistryToolNames() map[string]bool {
	knownToolNames := make(map[string]bool)
	for _, tool := range toolspkg.GetToolRegistry() {
		knownToolNames[tool.Name] = true
	}
	return knownToolNames
}

func knownRegistryToolTags() map[string]bool {
	knownToolTags := make(map[string]bool)
	for _, tool := range toolspkg.GetToolRegistry() {
		for _, tag := range tool.Tags {
			knownToolTags[string(tag)] = true
		}
	}
	return knownToolTags
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

func TestAffectedPresetsResolveForCodex(t *testing.T) {
	presets := loadAllPresets(t)
	registry := models.MustGetRegistry()

	testCases := []struct {
		presetName string
		wantTag    string
	}{
		{presetName: "documentation.yaml", wantTag: models.TagModerate},
		{presetName: "refactor.yaml", wantTag: models.TagModerate},
		{presetName: "tester.yaml", wantTag: models.TagModerate},
		{presetName: "workflow_builder.yaml", wantTag: models.TagFlagship},
	}

	for _, tc := range testCases {
		preset, ok := presets[tc.presetName]
		require.True(t, ok, "expected preset %q to exist", tc.presetName)

		t.Run(tc.presetName, func(t *testing.T) {
			model, ok := preset.Params["model"].(map[string]interface{})
			require.True(t, ok, "preset %q should define a model selector", tc.presetName)
			assert.NotContains(t, model, "id", "preset %q should not hardcode a provider-specific model", tc.presetName)

			tagsRaw, ok := model["tags"].([]interface{})
			require.True(t, ok, "preset %q should define model tags", tc.presetName)
			require.Len(t, tagsRaw, 1, "preset %q should define exactly one model tag", tc.presetName)

			tag, ok := tagsRaw[0].(string)
			require.True(t, ok, "preset %q has non-string tag %T", tc.presetName, tagsRaw[0])
			assert.Equal(t, tc.wantTag, tag, "preset %q should preserve its model intent", tc.presetName)

			resolved, err := registry.Resolve(models.ModelSelector{Tags: []string{tag}}, []string{"codex"})
			require.NoError(t, err, "preset %q should resolve for Codex", tc.presetName)
			assert.NotEmpty(t, resolved.Definition.ID, "preset %q should resolve to a concrete model for Codex", tc.presetName)
		})
	}
}

func TestPresetRecommendedSkillsExist(t *testing.T) {
	// Build set of known builtin skill paths (including nested sub-skills)
	knownSkills := make(map[string]bool)
	fs.WalkDir(skillscatalog.BuiltinSkillsFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		// Check if this directory contains a SKILL.md
		if _, readErr := fs.ReadFile(skillscatalog.BuiltinSkillsFS, path+"/SKILL.md"); readErr == nil {
			// Convert "builtin/code-review/security-review" -> "code-review/security-review"
			skillPath := strings.TrimPrefix(path, "builtin/")
			knownSkills[skillPath] = true
		}
		return nil
	})
	require.NotEmpty(t, knownSkills, "should find builtin skills")

	presets := loadAllPresets(t)
	for name, preset := range presets {
		for _, skill := range preset.RecommendedSkills {
			t.Run(name+"/"+skill, func(t *testing.T) {
				assert.True(t, knownSkills[skill],
					"preset %q references recommended_skill %q which does not exist as a builtin skill directory",
					preset.Name, skill)
			})
		}
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
