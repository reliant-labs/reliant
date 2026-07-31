// Copyright (c) 2025 Reliant Labs
package preset

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// TestAllBuiltinPresetsParse runs the real ParsePreset over every embedded
// builtin preset and asserts it loads without error. This is the loader path
// used at runtime (via loadPresetFromDB's builtin fallback), so a preset that
// fails ParsePreset — e.g. an unknown model-object key like "thinking" instead
// of "thinking_level" — surfaces to users as the misleading "preset not found",
// which is exactly how the ux.yaml regression slipped past the looser
// yaml.Unmarshal checks in builtin/presets/presets_test.go.
func TestAllBuiltinPresetsParse(t *testing.T) {
	entries, err := fs.ReadDir(builtin.BuiltinPresetsFS, "presets")
	if err != nil {
		t.Fatalf("read builtin presets dir: %v", err)
	}

	found := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		found++
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		t.Run(name, func(t *testing.T) {
			data, err := fs.ReadFile(builtin.BuiltinPresetsFS, "presets/"+entry.Name())
			if err != nil {
				t.Fatalf("read %s: %v", entry.Name(), err)
			}
			if _, err := ParsePreset(data, name); err != nil {
				t.Errorf("ParsePreset(%s) failed: %v", entry.Name(), err)
			}
		})
	}

	if found == 0 {
		t.Fatal("no builtin presets found to parse")
	}
}

func TestParsePreset(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		defaultName string
		wantName    string
		wantDesc    string
		wantTag     string
		wantParams  map[string]interface{}
		wantErr     bool
	}{
		{
			name: "basic preset with tag",
			yaml: `
name: careful
description: Slow and methodical
tag: agent
params:
  temperature: 0.2
  auto_approve: false
`,
			defaultName: "default",
			wantName:    "careful",
			wantDesc:    "Slow and methodical",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{"temperature": 0.2, "auto_approve": false},
			wantErr:     false,
		},
		{
			name: "preset without tag",
			yaml: `
name: legacy
description: No tag preset
params:
  model:
    id: claude-4.5-sonnet
`,
			defaultName: "fallback",
			wantName:    "legacy",
			wantDesc:    "No tag preset",
			wantTag:     "",
			wantParams:  map[string]interface{}{"model": map[string]interface{}{"id": "claude-4.5-sonnet"}},
			wantErr:     false,
		},
		{
			name: "uses default name when not specified",
			yaml: `
description: No name preset
tag: agent
params:
  model:
    id: claude-4.5-sonnet
`,
			defaultName: "fallback",
			wantName:    "fallback",
			wantDesc:    "No name preset",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{"model": map[string]interface{}{"id": "claude-4.5-sonnet"}},
			wantErr:     false,
		},
		{
			name: "empty params creates empty map",
			yaml: `
name: empty
tag: agent
`,
			defaultName: "default",
			wantName:    "empty",
			wantDesc:    "",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{},
			wantErr:     false,
		},
		{
			name:        "invalid yaml",
			yaml:        "not: valid: yaml: {{",
			defaultName: "default",
			wantErr:     true,
		},
		{
			name:        "no name and no default",
			yaml:        "params:\n  key: value",
			defaultName: "",
			wantErr:     true,
		},
		{
			name: "invalid model - string format not allowed",
			yaml: `
name: bad-model
tag: agent
params:
  model: claude-4.5-sonnet
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model name - API model instead of ModelID",
			yaml: `
name: bad-model
tag: agent
params:
  model:
    id: claude-sonnet-4-20250514
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model name - typo",
			yaml: `
name: typo-model
tag: agent
params:
  model:
    id: calude-4-sonnet
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "valid model name with id",
			yaml: `
name: good-model
tag: agent
params:
  model:
    id: claude-4.6-opus
`,
			defaultName: "default",
			wantName:    "good-model",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{"model": map[string]interface{}{"id": "claude-4.6-opus"}},
			wantErr:     false,
		},
		{
			name: "valid model with tags",
			yaml: `
name: tag-model
tag: agent
params:
  model:
    tags: [flagship]
`,
			defaultName: "default",
			wantName:    "tag-model",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"flagship"}}},
			wantErr:     false,
		},
		{
			name: "empty model id is allowed",
			yaml: `
name: empty-model
tag: agent
params:
  model:
    id: ""
`,
			defaultName: "default",
			wantName:    "empty-model",
			wantTag:     "agent",
			wantParams:  map[string]interface{}{"model": map[string]interface{}{"id": ""}},
			wantErr:     false,
		},
		// Model tag tests
		// Model tag string format is no longer supported - must use object format with tags array
		{
			name: "invalid model tag string - @smart (string format not allowed)",
			yaml: `
name: smart-model
tag: agent
params:
  model: "@smart"
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model tag string - @default (string format not allowed)",
			yaml: `
name: default-model
tag: agent
params:
  model: "@default"
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model tag string - @fast (string format not allowed)",
			yaml: `
name: fast-model
tag: agent
params:
  model: "@fast"
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model tag - @unknown",
			yaml: `
name: unknown-tag
tag: agent
params:
  model: "@unknown"
`,
			defaultName: "default",
			wantErr:     true,
		},
		{
			name: "invalid model tag - @SMART (case sensitive)",
			yaml: `
name: uppercase-tag
tag: agent
params:
  model: "@SMART"
`,
			defaultName: "default",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset, err := ParsePreset([]byte(tt.yaml), tt.defaultName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if preset.Name != tt.wantName {
				t.Errorf("name = %q, want %q", preset.Name, tt.wantName)
			}
			if preset.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", preset.Description, tt.wantDesc)
			}
			if preset.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", preset.Tag, tt.wantTag)
			}
			if len(preset.Params) != len(tt.wantParams) {
				t.Errorf("params len = %d, want %d", len(preset.Params), len(tt.wantParams))
			}
		})
	}
}

func TestApplyToInputs(t *testing.T) {
	t.Run("apply to top-level (no group)", func(t *testing.T) {
		modelSelector := map[string]interface{}{"id": "claude-sonnet"}
		preset := &Preset{
			Params: map[string]interface{}{
				"model":       modelSelector,
				"temperature": 0.3,
			},
		}

		inputs := map[string]interface{}{
			"message":     "hello",
			"temperature": 0.7, // Will be overridden
		}

		result := ApplyToInputs(preset, inputs, "")

		// Check original inputs are preserved
		if result["message"] != "hello" {
			t.Errorf("message = %v, want hello", result["message"])
		}

		// Check preset values are applied
		resultModel, ok := result["model"].(map[string]interface{})
		if !ok || resultModel["id"] != "claude-sonnet" {
			t.Errorf("model = %v, want {id: claude-sonnet}", result["model"])
		}

		// Check preset overrides existing values
		if result["temperature"] != 0.3 {
			t.Errorf("temperature = %v, want 0.3", result["temperature"])
		}

		// Check original map wasn't modified
		if inputs["model"] != nil {
			t.Errorf("original inputs was modified")
		}
	})

	t.Run("apply to group", func(t *testing.T) {
		modelSelector := map[string]interface{}{"id": "claude-opus"}
		preset := &Preset{
			Params: map[string]interface{}{
				"model":       modelSelector,
				"temperature": 0.5,
			},
		}

		inputs := map[string]interface{}{
			"mode": "auto",
		}

		result := ApplyToInputs(preset, inputs, "Agent A")

		// Check original inputs are preserved
		if result["mode"] != "auto" {
			t.Errorf("mode = %v, want auto", result["mode"])
		}

		// Check preset values are nested under group key
		agentA, ok := result["Agent A"].(map[string]interface{})
		if !ok {
			t.Fatalf("result[\"Agent A\"] = %T, want map", result["Agent A"])
		}
		resultModel, ok := agentA["model"].(map[string]interface{})
		if !ok || resultModel["id"] != "claude-opus" {
			t.Errorf("Agent A model = %v, want {id: claude-opus}", agentA["model"])
		}

		if agentA["temperature"] != 0.5 {
			t.Errorf("Agent A temperature = %v, want 0.5", agentA["temperature"])
		}
	})
}

func TestLoader_LoadProjectPresets(t *testing.T) {
	// Create temp directory with test presets
	tmpDir := t.TempDir()
	presetsDir := filepath.Join(tmpDir, ".reliant", "presets")
	if err := os.MkdirAll(presetsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write test preset with object format for model
	presetYAML := `
name: test-preset
description: Test preset
tag: agent
params:
  model:
    id: claude-4.5-sonnet
  temperature: 0.5
`
	if err := os.WriteFile(filepath.Join(presetsDir, "test-preset.yaml"), []byte(presetYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load presets
	loader := NewLoaderForProject(tmpDir)
	presets, err := loader.loadProjectPresets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}

	preset := presets[0]
	if preset.Name != "test-preset" {
		t.Errorf("name = %q, want test-preset", preset.Name)
	}
	if preset.Tag != "agent" {
		t.Errorf("tag = %q, want agent", preset.Tag)
	}
	if preset.Source != "project" {
		t.Errorf("source = %q, want project", preset.Source)
	}
	modelMap, ok := preset.Params["model"].(map[string]interface{})
	if !ok || modelMap["id"] != "claude-4.5-sonnet" {
		t.Errorf("model param = %v, want {id: claude-4.5-sonnet}", preset.Params["model"])
	}
}

func TestLoader_LoadProjectPresetsWithModelTag(t *testing.T) {
	// Create temp directory with test presets using model tags
	tmpDir := t.TempDir()
	presetsDir := filepath.Join(tmpDir, ".reliant", "presets")
	if err := os.MkdirAll(presetsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write test preset with model tags (object format, not string)
	presetYAML := `
name: tag-preset
description: Preset with model tag
tag: agent
params:
  model:
    tags: [flagship]
  temperature: 0.5
`
	if err := os.WriteFile(filepath.Join(presetsDir, "tag-preset.yaml"), []byte(presetYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load presets
	loader := NewLoaderForProject(tmpDir)
	presets, err := loader.loadProjectPresets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}

	preset := presets[0]
	if preset.Name != "tag-preset" {
		t.Errorf("name = %q, want tag-preset", preset.Name)
	}
	modelMap, ok := preset.Params["model"].(map[string]interface{})
	if !ok {
		t.Errorf("model param should be a map, got %T", preset.Params["model"])
	}
	tags, ok := modelMap["tags"].([]interface{})
	if !ok || len(tags) == 0 || tags[0] != "flagship" {
		t.Errorf("model tags = %v, want [flagship]", modelMap["tags"])
	}
}

func TestLoader_LoadAll(t *testing.T) {
	// Create temp directory - empty project presets
	tmpDir := t.TempDir()

	loader := NewLoaderForProject(tmpDir)
	presets, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get builtin presets (if any exist)
	// This test mainly verifies no error with missing project dir
	t.Logf("Loaded %d presets", len(presets))
}

func TestNewLoader(t *testing.T) {
	loader := NewLoader("")
	if loader.presetDir != filepath.Join(".reliant", "presets") {
		t.Errorf("default dir = %q, want .reliant/presets", loader.presetDir)
	}

	loader = NewLoader("/custom/path")
	if loader.presetDir != "/custom/path" {
		t.Errorf("custom dir = %q, want /custom/path", loader.presetDir)
	}
}

// Helper: build proto V2Workflow with inputs
func buildProtoWorkflow(presetTag string, inputs map[string]*reliantv1.Input) *reliantv1.Workflow {
	wf := &reliantv1.Workflow{
		Inputs: inputs,
	}
	if presetTag != "" {
		wf.Presets = &reliantv1.PresetsConfig{Tag: presetTag}
	}
	return wf
}

// Helper: build a model input
func protoModelInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type: "model",
		Config: &reliantv1.Input_ModelInput{
			ModelInput: &reliantv1.ModelInputConfig{},
		},
	}
}

// Helper: build a number input with default
func protoNumberInputWithDefault(def float64) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "number",
		Config: &reliantv1.Input_NumberInput{
			NumberInput: &reliantv1.NumberInputConfig{
				Default: &def,
			},
		},
	}
}

// Helper: build a number input without default
func protoNumberInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type: "number",
		Config: &reliantv1.Input_NumberInput{
			NumberInput: &reliantv1.NumberInputConfig{},
		},
	}
}

// Helper: build a string input
func protoStringInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{
			StringInput: &reliantv1.StringInputConfig{},
		},
	}
}

// Helper: build a string input with hidden UI
func protoHiddenStringInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{
			StringInput: &reliantv1.StringInputConfig{
				Base: &reliantv1.InputBase{Ui: "hidden"},
			},
		},
	}
}

// Helper: build an integer input
func protoIntegerInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type: "integer",
		Config: &reliantv1.Input_IntegerInput{
			IntegerInput: &reliantv1.IntegerInputConfig{},
		},
	}
}

// Helper: build a group input
func protoGroupInput(presetTag string, inputs map[string]*reliantv1.Input) *reliantv1.Input {
	config := &reliantv1.GroupInputConfig{
		Inputs: inputs,
	}
	if presetTag != "" {
		config.Presets = &reliantv1.PresetsConfig{Tag: presetTag}
	}
	return &reliantv1.Input{
		Type: "group",
		Config: &reliantv1.Input_GroupInput{
			GroupInput: config,
		},
	}
}

func TestValidatePreset(t *testing.T) {
	tests := []struct {
		name           string
		preset         *Preset
		workflow       *reliantv1.Workflow
		wantValid      bool
		wantTagMatched bool
		wantInvalid    []string
	}{
		{
			name: "valid - tag matches workflow with all params",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"model":       map[string]interface{}{"id": "claude-sonnet"},
					"temperature": 0.7,
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model":       protoModelInput(),
				"temperature": protoNumberInputWithDefault(1.0),
			}),
			wantValid:      true,
			wantTagMatched: true,
		},
		{
			name: "valid - tag matches group with all params",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"model":       map[string]interface{}{"id": "claude-sonnet"},
					"temperature": 0.7,
				},
			},
			workflow: buildProtoWorkflow("", map[string]*reliantv1.Input{
				"mode": protoStringInput(),
				"Agent A": protoGroupInput("agent", map[string]*reliantv1.Input{
					"model":       protoModelInput(),
					"temperature": protoNumberInputWithDefault(1.0),
				}),
			}),
			wantValid:      true,
			wantTagMatched: true,
		},
		{
			name: "valid - matches multiple groups with same tag",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"model": map[string]interface{}{"id": "claude-sonnet"},
				},
			},
			workflow: buildProtoWorkflow("", map[string]*reliantv1.Input{
				"Agent A": protoGroupInput("agent", map[string]*reliantv1.Input{
					"model":       protoModelInput(),
					"temperature": protoNumberInput(),
				}),
				"Agent B": protoGroupInput("agent", map[string]*reliantv1.Input{
					"model":       protoModelInput(),
					"temperature": protoNumberInput(),
				}),
			}),
			wantValid:      true,
			wantTagMatched: true,
		},
		{
			name: "valid - partial preset (doesn't cover all params)",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"temperature": 0.7,
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model":       protoModelInput(),
				"temperature": protoNumberInputWithDefault(1.0),
			}),
			wantValid:      true,
			wantTagMatched: true,
		},
		{
			name: "invalid - extra params not allowed (strict validation)",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"model":       map[string]interface{}{"id": "claude-sonnet"},
					"nonexistent": "value",
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
			wantValid:      false,
			wantTagMatched: true,
			wantInvalid:    []string{"nonexistent"},
		},
		{
			name: "invalid - no applicable params (all params non-existent)",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"nonexistent1": "value1",
					"nonexistent2": "value2",
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
			wantValid:      false,
			wantTagMatched: true,
		},
		{
			name: "invalid - tag doesn't match workflow or any group",
			preset: &Preset{
				Name: "test",
				Tag:  "orchestrator",
				Params: map[string]interface{}{
					"model": map[string]interface{}{"id": "claude-sonnet"},
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
				"Agent A": protoGroupInput("agent", map[string]*reliantv1.Input{
					"model": protoModelInput(),
				}),
			}),
			wantValid:      false,
			wantTagMatched: false,
		},
		{
			name: "invalid - empty preset tag doesn't match",
			preset: &Preset{
				Name: "test",
				Tag:  "",
				Params: map[string]interface{}{
					"model": map[string]interface{}{"id": "claude-sonnet"},
				},
			},
			workflow: buildProtoWorkflow("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
			wantValid:      false,
			wantTagMatched: false,
		},
		{
			name: "valid only for matching group - invalid param for other group",
			preset: &Preset{
				Name: "test",
				Tag:  "agent",
				Params: map[string]interface{}{
					"model": map[string]interface{}{"id": "claude-sonnet"},
				},
			},
			workflow: buildProtoWorkflow("", map[string]*reliantv1.Input{
				"Agent A": protoGroupInput("agent", map[string]*reliantv1.Input{
					"model": protoModelInput(),
				}),
				"Agent B": protoGroupInput("agent", map[string]*reliantv1.Input{
					"temperature": protoNumberInput(),
				}),
			}),
			wantValid:      true,
			wantTagMatched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidatePreset(tt.preset, tt.workflow)

			if result.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v. Errors: %v", result.Valid, tt.wantValid, result.Errors)
			}

			if result.TagMatched != tt.wantTagMatched {
				t.Errorf("TagMatched = %v, want %v", result.TagMatched, tt.wantTagMatched)
			}

			if len(tt.wantInvalid) > 0 {
				for _, param := range tt.wantInvalid {
					found := false
					for _, invalid := range result.InvalidParams {
						if invalid == param {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected %q in InvalidParams, got %v", param, result.InvalidParams)
					}
				}
			}
		})
	}
}

func TestGetRequiredParamsForGroup(t *testing.T) {
	workflow := buildProtoWorkflow("", map[string]*reliantv1.Input{
		"mode": protoStringInput(), // ungrouped, no default = required
		"Model": protoGroupInput("", map[string]*reliantv1.Input{
			"model":       protoModelInput(),                // no default = required
			"temperature": protoNumberInputWithDefault(1.0), // has default
			"api_key":     protoHiddenStringInput(),         // hidden, no default = required
		}),
	})

	// Model group should only return "model" (not temperature which has default, not api_key which is hidden)
	modelParams := GetRequiredParamsForGroup(workflow, "Model")
	if len(modelParams) != 1 || modelParams[0] != "model" {
		t.Errorf("Model group params = %v, want [model]", modelParams)
	}

	// Ungrouped should return "mode"
	ungroupedParams := GetRequiredParamsForGroup(workflow, "")
	if len(ungroupedParams) != 1 || ungroupedParams[0] != "mode" {
		t.Errorf("Ungrouped params = %v, want [mode]", ungroupedParams)
	}
}

func TestGetPresetsForTag(t *testing.T) {
	presets := []*Preset{
		{Name: "fast", Tag: "agent", Params: map[string]interface{}{"temperature": 0.9}},
		{Name: "careful", Tag: "agent", Params: map[string]interface{}{"temperature": 0.2}},
		{Name: "orchestrator", Tag: "orchestration", Params: map[string]interface{}{"max_workers": 3}},
	}

	agentPresets := GetPresetsForTag(presets, "agent")
	if len(agentPresets) != 2 {
		t.Errorf("expected 2 agent presets, got %d", len(agentPresets))
	}

	orchestratorPresets := GetPresetsForTag(presets, "orchestration")
	if len(orchestratorPresets) != 1 {
		t.Errorf("expected 1 orchestration preset, got %d", len(orchestratorPresets))
	}

	unknownPresets := GetPresetsForTag(presets, "unknown")
	if len(unknownPresets) != 0 {
		t.Errorf("expected 0 unknown presets, got %d", len(unknownPresets))
	}
}

func TestGetTagsFromWorkflow(t *testing.T) {
	t.Run("workflow with tag and groups with tags", func(t *testing.T) {
		workflow := buildProtoWorkflow("agent", map[string]*reliantv1.Input{
			"model": protoModelInput(),
			"Agent A": protoGroupInput("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
			"Agent B": protoGroupInput("agent", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
			"Orchestrator": protoGroupInput("orchestration", map[string]*reliantv1.Input{
				"max_workers": protoIntegerInput(),
			}),
		})

		tags := GetTagsFromWorkflow(workflow)

		// Should have "agent" tag with 3 targets: workflow ("") and two groups
		if len(tags["agent"]) != 3 {
			t.Errorf("expected 3 targets for agent tag, got %d: %v", len(tags["agent"]), tags["agent"])
		}

		// Should have "orchestration" tag with 1 target
		if len(tags["orchestration"]) != 1 {
			t.Errorf("expected 1 target for orchestration tag, got %d: %v", len(tags["orchestration"]), tags["orchestration"])
		}
	})

	t.Run("workflow without tags", func(t *testing.T) {
		workflow := buildProtoWorkflow("", map[string]*reliantv1.Input{
			"model": protoModelInput(),
			"Settings": protoGroupInput("", map[string]*reliantv1.Input{
				"model": protoModelInput(),
			}),
		})

		tags := GetTagsFromWorkflow(workflow)
		if len(tags) != 0 {
			t.Errorf("expected 0 tags, got %d: %v", len(tags), tags)
		}
	})
}

func TestGetGroups(t *testing.T) {
	t.Run("workflow with groups", func(t *testing.T) {
		workflow := buildProtoWorkflow("", map[string]*reliantv1.Input{
			"Agent A": protoGroupInput("", nil),
			"Agent B": protoGroupInput("", nil),
		})

		groups := GetGroups(workflow)
		if len(groups) != 2 {
			t.Errorf("expected 2 groups, got %d", len(groups))
		}
	})

	t.Run("workflow without groups", func(t *testing.T) {
		workflow := &reliantv1.Workflow{}

		groups := GetGroups(workflow)
		if groups != nil {
			t.Errorf("expected nil groups, got %v", groups)
		}
	})
}
