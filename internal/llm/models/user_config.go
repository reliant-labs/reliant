// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// LoadUserModelsConfig loads a UserModelsConfig from a YAML file path.
// Returns nil with no error if the file doesn't exist.
func LoadUserModelsConfig(path string) (*UserModelsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read user models config: %w", err)
	}

	var cfg UserModelsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse user models config: %w", err)
	}

	return &cfg, nil
}

// LoadUserModelsConfigFromBytes parses a UserModelsConfig from a YAML byte slice.
func LoadUserModelsConfigFromBytes(data []byte) (*UserModelsConfig, error) {
	var cfg UserModelsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse user models config: %w", err)
	}
	return &cfg, nil
}

// MergeUserConfig applies user configuration to the registry:
//  1. Adds custom models
//  2. Prepends the user's tag entries to the built-in tag lists
//
// Local models are never merged here: they belong to a user's daemons and are
// synthesized per request (internal/llm/localmodels), not registered.
//
// Note: This modifies the registry in place. Clone first if you need to preserve
// the original.
func (r *ModelRegistry) MergeUserConfig(cfg *UserModelsConfig) error {
	if cfg == nil {
		return nil
	}
	if err := r.addCustomModels(cfg.Custom); err != nil {
		return err
	}
	// Models are all known by now, so a tag entry naming a custom model validates.
	return r.applyUserTags(cfg.Tags)
}

// addModel appends a model and re-points the id index. Appending can move
// the backing array, so every pointer is rebuilt rather than just the new one.
func (r *ModelRegistry) addModel(model ModelDefinition) {
	r.models = append(r.models, model)
	for i := range r.models {
		r.byID[r.models[i].ID] = &r.models[i]
	}
}

// addCustomModels adds user-defined models to the registry.
func (r *ModelRegistry) addCustomModels(custom []ModelDefinition) error {
	for _, model := range custom {
		// Validate required fields
		if model.ID == "" {
			return fmt.Errorf("custom model missing required 'id' field")
		}
		if len(model.Providers) == 0 {
			return fmt.Errorf("custom model %s has no providers", model.ID)
		}

		// Set defaults for missing optional fields
		if model.Name == "" {
			model.Name = model.ID
		}
		if model.Visibility == "" {
			model.Visibility = VisibilityUser
		}

		// Check for duplicate ID
		if _, exists := r.byID[model.ID]; exists {
			return fmt.Errorf("custom model ID conflicts with existing model: %s", model.ID)
		}
		r.addModel(model)
	}

	return nil
}

// applyUserTags puts the user's entries at the FRONT of each tag's list and
// keeps the built-in entries behind them as the fallback. A model the user
// lists takes the user's position and effort; its built-in entry is dropped,
// since a second, later position for the same model could never win.
func (r *ModelRegistry) applyUserTags(userTags map[string][]TagEntry) error {
	for tag, userEntries := range userTags {
		listed := make(map[string]bool, len(userEntries))
		for _, entry := range userEntries {
			listed[entry.Model] = true
		}
		merged := slices.Clone(userEntries)
		for _, entry := range r.tags[tag] {
			if !listed[entry.Model] {
				merged = append(merged, entry)
			}
		}
		if err := r.setTag(tag, merged); err != nil {
			return fmt.Errorf("user config: %w", err)
		}
	}
	return nil
}

// CreateRegistryWithUserConfig creates a new registry with user configuration applied:
//  1. Parses the embedded YAML
//  2. Clones the registry
//  3. Applies user configuration
//
// Returns an unmodified registry if cfg is nil.
func CreateRegistryWithUserConfig(cfg *UserModelsConfig) (*ModelRegistry, error) {
	// ParseRegistry (not GetRegistry) so the global singleton is untouched.
	baseReg, err := ParseRegistry()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return baseReg, nil
	}
	userReg := baseReg.Clone()
	if err := userReg.MergeUserConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to apply user config: %w", err)
	}
	return userReg, nil
}

// ValidateUserConfig validates a user configuration without applying it.
// Returns a list of warnings and an error if validation fails.
func ValidateUserConfig(cfg *UserModelsConfig) (warnings []string, err error) {
	if cfg == nil {
		return nil, nil
	}

	// Use ParseRegistry() to avoid triggering the global singleton
	baseReg, err := ParseRegistry()
	if err != nil {
		return nil, err
	}

	// Validate custom models
	seenIDs := make(map[string]bool)
	for i, model := range cfg.Custom {
		if model.ID == "" {
			return warnings, fmt.Errorf("custom model at index %d missing 'id'", i)
		}
		if seenIDs[model.ID] {
			return warnings, fmt.Errorf("duplicate custom model ID: %s", model.ID)
		}
		seenIDs[model.ID] = true

		if _, exists := baseReg.byID[model.ID]; exists {
			return warnings, fmt.Errorf("custom model ID conflicts with built-in model: %s", model.ID)
		}

		if len(model.Providers) == 0 {
			return warnings, fmt.Errorf("custom model %s has no providers", model.ID)
		}
	}

	// Validate tag entries against built-in and custom models alike.
	for tag, entries := range cfg.Tags {
		for _, entry := range entries {
			if _, exists := baseReg.byID[entry.Model]; !exists && !seenIDs[entry.Model] {
				return warnings, fmt.Errorf("tags[%q] references unknown model: %s", tag, entry.Model)
			}
			if entry.ThinkingLevel != "" && !IsKnownThinkingLevel(entry.ThinkingLevel) {
				return warnings, fmt.Errorf("tags[%q] (%s): unknown thinking level %q", tag, entry.Model, entry.ThinkingLevel)
			}
		}
	}

	return warnings, nil
}
