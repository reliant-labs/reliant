// Copyright (c) 2025 Reliant Labs
// Package preset provides types and loading for workflow presets.
//
// Presets are reusable bundles of workflow parameter values stored in
// .reliant/presets/ directories or embedded as builtins.
//
// A preset is valid for a workflow if:
// 1. The preset's tag matches the workflow's tag or a group's tag
// 2. ALL params in the preset exist in the matched target's inputs
//
// Presets can be partial - they don't need to cover all params in their target.
// However, all params that ARE specified must exist in the target. This ensures
// typos and stale params are caught early rather than silently ignored.
//
// forge:exclude-contract
//
// Registry/lookup-table package: the exported vars are populated once at init
// by the packages that register into them, then read. A getter returns the
// same map or slice header, so it moves the mutation surface without
// narrowing it.
package preset

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/validation"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"gopkg.in/yaml.v3"
)

// Preset represents a reusable bundle of workflow parameter values.
// Presets are stored in .reliant/presets/ as YAML files.
//
// Example:
//
//	name: careful
//	tag: agent
//	description: Slow, methodical approach with approval gates
//	params:
//	  model: claude-sonnet-4-20250514
//	  temperature: 0.2
type Preset struct {
	// Name is the preset identifier (also the filename without .yaml)
	Name string `yaml:"name" json:"name"`

	// Description is a human-readable description shown in the preset picker
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Tag declares which type of inputs this preset targets.
	// The preset matches workflows/groups with the same tag.
	// e.g., tag: "agent" matches workflows with tag: "agent" or groups with tag: "agent"
	Tag string `yaml:"tag,omitempty" json:"tag,omitempty"`

	// Params is a map of parameter name to value.
	// These are applied to the workflow inputs when the preset is selected.
	//
	// Recommended skills live here under the "skills" key (a []string), the same
	// unified skill param the call_llm node reads. Keeping them in Params means
	// they flow through ApplyToInputs + mergePresetParams like every other input,
	// so a child spawned from a chat no longer inherits the parent chat's skills.
	Params map[string]interface{} `yaml:"params" json:"params"`

	// Source indicates where the preset came from (builtin or project)
	Source string `yaml:"-" json:"source,omitempty"`
}

// InvalidPreset represents a preset that failed to load or validate.
type InvalidPreset struct {
	// Name/slug of the preset (from filename)
	Name string
	// Source of the preset: "builtin" or "project"
	Source string
	// Path to the preset file
	Path string
	// Validation/loading errors
	Errors []string
}

// LoadResult contains both valid presets and invalid presets from loading.
type LoadResult struct {
	Valid   []*Preset
	Invalid []*InvalidPreset
}

// Loader loads presets from a directory.
type Loader struct {
	// presetDir is the base directory to load presets from
	presetDir string
}

// NewLoader creates a new preset loader for the given directory.
// If dir is empty, it defaults to ".reliant/presets" relative to cwd.
func NewLoader(dir string) *Loader {
	if dir == "" {
		dir = filepath.Join(".reliant", "presets")
	}
	return &Loader{presetDir: dir}
}

// NewLoaderForProject creates a preset loader for a specific project directory.
func NewLoaderForProject(projectDir string) *Loader {
	return &Loader{
		presetDir: filepath.Join(projectDir, ".reliant", "presets"),
	}
}

// Load loads a single preset by name.
// Checks project presets first, then falls back to builtin presets.
func (l *Loader) Load(name string) (*Preset, error) {
	// Try project preset first
	filename := name + ".yaml"
	path := filepath.Join(l.presetDir, filename)

	data, err := os.ReadFile(path)
	if err == nil {
		preset, err := ParsePreset(data, name)
		if err != nil {
			return nil, err
		}
		preset.Source = "project"
		return preset, nil
	}

	// Fall back to builtin preset
	builtinPath := "presets/" + filename
	data, err = builtin.BuiltinPresetsFS.ReadFile(builtinPath)
	if err == nil {
		preset, err := ParsePreset(data, name)
		if err != nil {
			return nil, err
		}
		preset.Source = "builtin"
		return preset, nil
	}

	return nil, fmt.Errorf("preset not found: %s", name)
}

// LoadAll loads all presets from both project directory and builtins.
// Project presets override builtin presets with the same name.
// Invalid presets are silently skipped - use LoadAllWithErrors to get error info.
func (l *Loader) LoadAll() ([]*Preset, error) {
	result := l.LoadAllWithErrors()
	return result.Valid, nil
}

// LoadAllWithErrors loads all presets and returns both valid and invalid presets.
// Project presets override builtin presets with the same name.
func (l *Loader) LoadAllWithErrors() *LoadResult {
	presetMap := make(map[string]*Preset)
	invalidMap := make(map[string]*InvalidPreset)

	// Load builtin presets first
	builtinResult := loadBuiltinPresetsWithErrors()
	for _, p := range builtinResult.Valid {
		presetMap[p.Name] = p
	}
	for _, inv := range builtinResult.Invalid {
		invalidMap[inv.Name] = inv
	}

	// Load project presets (override builtins)
	projectResult := l.loadProjectPresetsWithErrors()
	for _, p := range projectResult.Valid {
		presetMap[p.Name] = p
		// If project preset overrides a broken builtin, remove from invalid
		delete(invalidMap, p.Name)
	}
	for _, inv := range projectResult.Invalid {
		invalidMap[inv.Name] = inv
	}

	// Convert maps to slices
	valid := make([]*Preset, 0, len(presetMap))
	for _, p := range presetMap {
		valid = append(valid, p)
	}

	invalid := make([]*InvalidPreset, 0, len(invalidMap))
	for _, inv := range invalidMap {
		invalid = append(invalid, inv)
	}

	return &LoadResult{Valid: valid, Invalid: invalid}
}

// loadBuiltinPresetsWithErrors loads all embedded builtin presets,
// returning both valid and invalid presets.
func loadBuiltinPresetsWithErrors() *LoadResult {
	result := &LoadResult{
		Valid:   make([]*Preset, 0),
		Invalid: make([]*InvalidPreset, 0),
	}

	entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	if err != nil {
		return result // No builtin presets dir
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		path := "presets/" + entry.Name()
		data, err := builtin.BuiltinPresetsFS.ReadFile(path)
		if err != nil {
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			result.Invalid = append(result.Invalid, &InvalidPreset{
				Name:   name,
				Source: "builtin",
				Path:   path,
				Errors: []string{fmt.Sprintf("failed to read file: %v", err)},
			})
			continue
		}

		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		preset, err := ParsePreset(data, name)
		if err != nil {
			result.Invalid = append(result.Invalid, &InvalidPreset{
				Name:   name,
				Source: "builtin",
				Path:   path,
				Errors: []string{err.Error()},
			})
			continue
		}
		preset.Source = "builtin"
		result.Valid = append(result.Valid, preset)
	}

	return result
}

// loadProjectPresets loads presets from the project's .reliant/presets directory.
// Invalid presets cause the function to return an error.
func (l *Loader) loadProjectPresets() ([]*Preset, error) {
	result := l.loadProjectPresetsWithErrors()
	if len(result.Invalid) > 0 {
		return result.Valid, fmt.Errorf("failed to load %s: %s", result.Invalid[0].Name, result.Invalid[0].Errors[0])
	}
	return result.Valid, nil
}

// loadProjectPresetsWithErrors loads presets from the project's .reliant/presets directory,
// returning both valid and invalid presets.
func (l *Loader) loadProjectPresetsWithErrors() *LoadResult {
	result := &LoadResult{
		Valid:   make([]*Preset, 0),
		Invalid: make([]*InvalidPreset, 0),
	}

	// Check if directory exists
	info, err := os.Stat(l.presetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result // No presets dir, return empty
		}
		return result
	}
	if !info.IsDir() {
		return result
	}

	// Walk the directory
	_ = filepath.WalkDir(l.presetDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // Skip entries with walk errors
		}

		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".yaml") && !strings.HasSuffix(d.Name(), ".yml") {
			return nil
		}

		name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))

		data, err := os.ReadFile(path)
		if err != nil {
			result.Invalid = append(result.Invalid, &InvalidPreset{
				Name:   name,
				Source: "project",
				Path:   path,
				Errors: []string{fmt.Sprintf("failed to read file: %v", err)},
			})
			return nil
		}

		preset, err := ParsePreset(data, name)
		if err != nil {
			result.Invalid = append(result.Invalid, &InvalidPreset{
				Name:   name,
				Source: "project",
				Path:   path,
				Errors: []string{err.Error()},
			})
			return nil
		}
		preset.Source = "project"

		result.Valid = append(result.Valid, preset)
		return nil
	})

	return result
}

// LoadForWorkflow loads all presets that are compatible with the given workflow.
// Returns valid presets and any validation errors encountered.
//
// A preset is valid for a workflow/group if:
// 1. The preset's tag matches the workflow's tag (for top-level inputs) or a group's tag
// 2. ALL params in the preset exist in the matching target's inputs
func (l *Loader) LoadForWorkflow(workflow *reliantv1.Workflow) ([]*Preset, []*validation.Error) {
	allPresets, err := l.LoadAll()
	if err != nil {
		return nil, []*validation.Error{{Message: fmt.Sprintf("failed to load presets: %v", err)}}
	}

	var validPresets []*Preset
	var validationErrors []*validation.Error

	for _, p := range allPresets {
		result := ValidatePreset(p, workflow)
		if result.Valid {
			validPresets = append(validPresets, p)
		} else if result.TagMatched && len(result.Errors) > 0 {
			for _, errMsg := range result.Errors {
				validationErrors = append(validationErrors, &validation.Error{Message: errMsg})
			}
		}
	}

	return validPresets, validationErrors
}

// ParsePreset parses a YAML preset definition.
// The name is used as the preset name (overrides any name in YAML).
func ParsePreset(data []byte, name string) (*Preset, error) {
	var preset Preset
	if err := yaml.Unmarshal(data, &preset); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if preset.Name == "" {
		preset.Name = name
	}

	if preset.Name == "" {
		return nil, fmt.Errorf("preset name is required")
	}

	// Legacy fold: recommended skills used to live in a top-level
	// `recommended_skills:` list. They now live in params["skills"] (the unified
	// skill param). Fold any legacy field into params["skills"] so presets
	// authored/stored before the unification keep working during the deprecation
	// window. An explicit params["skills"] wins if both are present.
	foldLegacyRecommendedSkills(data, &preset)

	// Validate model param if present
	if err := validateModelParam(&preset); err != nil {
		return nil, err
	}

	return &preset, nil
}

// foldLegacyRecommendedSkills migrates a deprecated top-level `recommended_skills`
// list into params["skills"] (as a []interface{} of strings, matching how YAML
// parses a native params.skills list and what structpb.NewValue accepts). No-op
// when the legacy field is absent or params["skills"] is already set.
func foldLegacyRecommendedSkills(data []byte, p *Preset) {
	var legacy struct {
		RecommendedSkills []string `yaml:"recommended_skills"`
	}
	if err := yaml.Unmarshal(data, &legacy); err != nil || len(legacy.RecommendedSkills) == 0 {
		return
	}
	if p.Params == nil {
		p.Params = make(map[string]interface{})
	}
	if _, ok := p.Params["skills"]; ok {
		return // explicit params.skills takes precedence over the legacy field
	}
	skills := make([]interface{}, len(legacy.RecommendedSkills))
	for i, s := range legacy.RecommendedSkills {
		skills[i] = s
	}
	p.Params["skills"] = skills
}

// validateModelParam validates a model param in a preset.
func validateModelParam(preset *Preset) error {
	modelRaw, ok := preset.Params["model"]
	if !ok {
		return nil // No model param
	}

	// Model must be a map (object format) or nil - string format is not allowed.
	switch v := modelRaw.(type) {
	case string:
		return fmt.Errorf("preset %q: model must be an object (e.g., {id: model-name} or {tags: [fast]}), string format %q is not allowed", preset.Name, v)
	case map[string]interface{}:
		// Validate allowed keys in model object
		allowedKeys := map[string]bool{
			"id": true, "tags": true, "providers": true,
			"temperature": true, "thinking_level": true, "compaction_threshold": true,
		}
		for key := range v {
			if !allowedKeys[key] {
				return fmt.Errorf("preset %q: unknown key %q in model object (allowed: id, tags, providers, temperature, thinking_level, compaction_threshold)", preset.Name, key)
			}
		}
		// Validate model ID if present
		id, ok := v["id"].(string)
		if !ok || id == "" {
			return nil // No id or empty id - skip validation
		}
		// Check if the model exists in the registry
		registry := models.MustGetRegistry()
		if _, found := registry.GetDefinition(id); !found {
			return fmt.Errorf("preset %q: invalid model %q - see docs/models.md for available models", preset.Name, id)
		}
		return nil
	case nil:
		return nil // Explicitly nil is OK
	default:
		return fmt.Errorf("preset %q: invalid model param type %T (expected model selector object)", preset.Name, modelRaw)
	}
}

// ValidationResult contains the results of preset validation.
type ValidationResult struct {
	// Valid is true if the preset passes all validation checks
	Valid bool

	// TagMatched is true if the preset's tag matched at least one target
	// (workflow tag or a group tag). Used to determine if errors should be reported.
	TagMatched bool

	// MatchedTargets lists what the preset matched against:
	// - "" (empty string) means it matched workflow.Tag (top-level inputs)
	// - "GroupName" means it matched a group's tag
	MatchedTargets []string

	// Errors contains any validation errors
	Errors []string

	// InvalidParams lists params that don't exist in the matched target's inputs
	InvalidParams []string
}

// ValidatePreset performs validation of a preset against a workflow.
// A preset is valid if:
// 1. Its tag matches the workflow's tag OR at least one group's tag
// 2. ALL params in the preset exist in the matched target's inputs
//
// Presets can be partial - they don't need to cover all params in their target.
// However, all params that ARE specified must exist in the target. This ensures
// typos and stale params are caught early rather than silently ignored.
func ValidatePreset(preset *Preset, workflow *reliantv1.Workflow) *ValidationResult {
	result := &ValidationResult{
		Valid:          false,
		TagMatched:     false,
		MatchedTargets: make([]string, 0),
		Errors:         make([]string, 0),
		InvalidParams:  make([]string, 0),
	}

	// Collect all potential targets where this preset could apply
	type target struct {
		name   string                      // "" for workflow, group name for groups
		tag    string                      // tag to match against
		inputs map[string]*reliantv1.Input // inputs to validate params against
	}
	targets := make([]target, 0)

	// Build map of non-group inputs for workflow-level tag matching
	nonGroupInputs := make(map[string]*reliantv1.Input)
	for name, input := range workflow.GetInputs() {
		if input != nil && !model.IsGroupInput(input) {
			nonGroupInputs[name] = input
		}
	}

	// Check workflow-level tag (for top-level non-group inputs)
	if workflow.GetPresets().GetTag() != "" && len(nonGroupInputs) > 0 {
		targets = append(targets, target{
			name:   "",
			tag:    workflow.GetPresets().GetTag(),
			inputs: nonGroupInputs,
		})
	}

	// Check group tags (inputs with type: group)
	for groupName, input := range workflow.GetInputs() {
		if !model.IsGroupInput(input) {
			continue
		}
		cfg, ok := input.GetConfig().(*reliantv1.Input_GroupInput)
		if !ok || cfg.GroupInput == nil {
			continue
		}
		presets := cfg.GroupInput.GetPresets()
		if presets.GetTag() != "" && len(cfg.GroupInput.GetInputs()) > 0 {
			targets = append(targets, target{
				name:   groupName,
				tag:    presets.GetTag(),
				inputs: cfg.GroupInput.GetInputs(),
			})
		}
	}

	// Find targets where the tag matches
	for _, t := range targets {
		if t.tag != preset.Tag {
			continue
		}

		result.TagMatched = true

		// Check that ALL preset params exist in this target's inputs.
		allParamsValid := true
		for paramName := range preset.Params {
			if _, exists := t.inputs[paramName]; !exists {
				allParamsValid = false
				result.InvalidParams = append(result.InvalidParams, paramName)
				result.Errors = append(result.Errors, fmt.Sprintf("param %q does not exist in target %q", paramName, t.name))
			}
		}

		if len(preset.Params) == 0 || allParamsValid {
			result.MatchedTargets = append(result.MatchedTargets, t.name)
		}
	}

	// Preset is valid if it matched at least one target
	if len(result.MatchedTargets) > 0 {
		result.Valid = true
	}

	return result
}

// GetRequiredParamsForGroup returns all required (no-default, non-hidden) params in a group.
// For ungrouped params, pass group="".
func GetRequiredParamsForGroup(workflow *reliantv1.Workflow, group string) []string {
	result := make([]string, 0)

	var targetInputs map[string]*reliantv1.Input
	if group == "" {
		// Get non-group top-level inputs
		targetInputs = make(map[string]*reliantv1.Input)
		for name, input := range workflow.GetInputs() {
			if input != nil && !model.IsGroupInput(input) {
				targetInputs[name] = input
			}
		}
	} else {
		// Get inputs from a specific group
		if input, ok := workflow.GetInputs()[group]; ok && model.IsGroupInput(input) {
			targetInputs = model.GetGroupInputs(input)
		}
	}

	if targetInputs == nil {
		return result
	}

	for name, input := range targetInputs {
		if model.GetInputDefault(input) != nil {
			continue
		}
		if isProtoInputHidden(input) {
			continue
		}
		result = append(result, name)
	}
	return result
}

// isProtoInputHidden checks if a proto V2Input has ui: hidden set.
func isProtoInputHidden(input *reliantv1.Input) bool {
	if input == nil {
		return false
	}
	// Check the InputBase.ui field across all input config types
	switch cfg := input.GetConfig().(type) {
	case *reliantv1.Input_StringInput:
		return cfg.StringInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_NumberInput:
		return cfg.NumberInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_IntegerInput:
		return cfg.IntegerInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_BooleanInput:
		return cfg.BooleanInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_EnumInput:
		return cfg.EnumInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_ModelInput:
		return cfg.ModelInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_MessageInput:
		return cfg.MessageInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_AttachmentsInput:
		return cfg.AttachmentsInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_ToolsInput:
		return cfg.ToolsInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_ArrayInput:
		return cfg.ArrayInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_ObjectInput:
		return cfg.ObjectInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_AnyInput:
		return cfg.AnyInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_GroupInput:
		return cfg.GroupInput.GetBase().GetUi() == "hidden"
	case *reliantv1.Input_PresetInput:
		return cfg.PresetInput.GetBase().GetUi() == "hidden"
	}
	return false
}

// GetGroups returns all group names in a workflow (inputs with type: group).
// Returns nil if the workflow has no groups.
func GetGroups(workflow *reliantv1.Workflow) []string {
	var result []string
	for name, input := range workflow.GetInputs() {
		if model.IsGroupInput(input) {
			result = append(result, name)
		}
	}
	return result
}

// ApplyToInputs applies preset params to a workflow input map.
// Returns a new map with preset values applied (existing values are preserved
// if the preset doesn't override them). Inputs are always nested: group params
// go under result[groupName], never as flat keys like "agent.model".
//
// The groupName parameter specifies which group to apply the preset to:
// - "" (empty string) applies to top-level workflow inputs
// - "agent" applies to the "agent" group: result["agent"] = { "model": "sonnet", ... }
func ApplyToInputs(preset *Preset, inputs map[string]interface{}, groupName string) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy existing inputs
	for k, v := range inputs {
		result[k] = v
	}

	if groupName == "" || groupName == "default" {
		// Top-level / default group: apply directly
		for k, v := range preset.Params {
			result[k] = v
		}
		return result
	}

	// Grouped: apply under result[groupName] as a nested map
	existing, _ := result[groupName].(map[string]interface{})
	groupMap := make(map[string]interface{})
	for k, v := range existing {
		groupMap[k] = v
	}
	for k, v := range preset.Params {
		groupMap[k] = v
	}
	result[groupName] = groupMap

	return result
}

// NormalizeModelParams converts any string model values in preset params to
// map[string]interface{}{"id": string} objects. This ensures presets stored
// before model type enforcement are normalized at load time.
func NormalizeModelParams(p *Preset) {
	if p == nil || p.Params == nil {
		return
	}
	if modelRaw, ok := p.Params["model"]; ok {
		if s, ok := modelRaw.(string); ok && s != "" {
			p.Params["model"] = map[string]interface{}{"id": s}
		}
	}
}

// GetPresetsForTag returns all presets that match a specific tag.
func GetPresetsForTag(presets []*Preset, tag string) []*Preset {
	result := make([]*Preset, 0)
	for _, p := range presets {
		if p.Tag == tag {
			result = append(result, p)
		}
	}
	return result
}

// GetTagsFromWorkflow returns all unique tags from a workflow.
// Returns a map of tag -> list of target names ("" for workflow, group names for groups).
func GetTagsFromWorkflow(workflow *reliantv1.Workflow) map[string][]string {
	result := make(map[string][]string)

	// Workflow-level tag
	if workflow.GetPresets().GetTag() != "" {
		tag := workflow.GetPresets().GetTag()
		result[tag] = append(result[tag], "")
	}

	// Group tags (from inputs with type: group)
	for groupName, input := range workflow.GetInputs() {
		if !model.IsGroupInput(input) {
			continue
		}
		cfg, ok := input.GetConfig().(*reliantv1.Input_GroupInput)
		if !ok || cfg.GroupInput == nil {
			continue
		}
		presets := cfg.GroupInput.GetPresets()
		if presets.GetTag() != "" {
			tag := presets.GetTag()
			result[tag] = append(result[tag], groupName)
		}
	}

	return result
}

// Save writes a preset to a YAML file in the presets directory.
func (l *Loader) Save(preset *Preset) error {
	slug := strings.ToLower(strings.ReplaceAll(preset.Name, " ", "-"))
	filename := slug + ".yaml"
	path := filepath.Join(l.presetDir, filename)

	data, err := yaml.Marshal(preset)
	if err != nil {
		return fmt.Errorf("failed to marshal preset: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write preset file: %w", err)
	}

	return nil
}

// Delete removes a preset file from the presets directory.
func (l *Loader) Delete(name string) error {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	filename := slug + ".yaml"
	path := filepath.Join(l.presetDir, filename)

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete preset file: %w", err)
	}

	return nil
}

// GetFilePath returns the file path for a preset by name.
func (l *Loader) GetFilePath(name string) string {
	// Try exact name first
	filename := name + ".yaml"
	path := filepath.Join(l.presetDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path
	}

	// Try slug version
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	filename = slug + ".yaml"
	path = filepath.Join(l.presetDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path
	}

	return ""
}
