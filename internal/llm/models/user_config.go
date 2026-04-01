// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"os"

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

// LocalModelDiscoverer is a function that discovers models from a local endpoint.
// It returns a list of model definitions or an error.
// The baseURL is the OpenAI-compatible API endpoint (e.g., http://localhost:11434/v1).
type LocalModelDiscoverer func(baseURL string) ([]ModelDefinition, error)

// MergeUserConfig applies user configuration to the registry.
// This:
//  1. Discovers local models if provider is configured
//  2. Adds custom models to the registry
//  3. Applies tag preferences (reorders byTag lists)
//
// Note: This modifies the registry in place. Clone first if you need to preserve
// the original.
func (r *ModelRegistry) MergeUserConfig(cfg *UserModelsConfig) error {
	return r.MergeUserConfigWithDiscovery(cfg, nil)
}

// MergeUserConfigWithDiscovery applies user configuration to the registry with optional
// local model discovery. If discoverer is non-nil and cfg.Providers.Local is configured,
// models will be discovered from the local endpoint and added to the registry.
//
// This:
//  1. Discovers local models if provider is configured and discoverer is provided
//  2. Adds custom models to the registry
//  3. Applies tag preferences (reorders byTag lists)
//
// Note: This modifies the registry in place. Clone first if you need to preserve
// the original.
func (r *ModelRegistry) MergeUserConfigWithDiscovery(cfg *UserModelsConfig, discoverer LocalModelDiscoverer) error {
	if cfg == nil {
		return nil
	}

	// Step 1: Discover local models if provider is configured
	if err := r.discoverLocalModels(cfg, discoverer); err != nil {
		return err
	}

	// Step 2: Add custom models
	if err := r.addCustomModels(cfg.Custom); err != nil {
		return err
	}

	// Step 3: Apply tag preferences
	if err := r.applyTagPreferences(cfg.TagPreferences); err != nil {
		return err
	}

	return nil
}

// discoverLocalModels discovers and adds local models if the local provider is configured.
func (r *ModelRegistry) discoverLocalModels(cfg *UserModelsConfig, discoverer LocalModelDiscoverer) error {
	if discoverer == nil {
		return nil
	}
	if cfg.Providers.Local == nil || cfg.Providers.Local.BaseURL == "" {
		return nil
	}

	models, err := discoverer(cfg.Providers.Local.BaseURL)
	if err != nil {
		return fmt.Errorf("failed to discover local models: %w", err)
	}

	for _, model := range models {
		// Skip if model ID already exists (user may have defined it explicitly)
		if _, exists := r.byID[model.ID]; exists {
			continue
		}

		// Add to models slice and index
		r.models = append(r.models, model)
		modelPtr := &r.models[len(r.models)-1]
		r.byID[model.ID] = modelPtr

		// Add to tag indices
		for _, tag := range model.Tags {
			r.byTag[tag] = append(r.byTag[tag], modelPtr)
		}
	}

	return nil
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

		// Add to models slice and index
		r.models = append(r.models, model)
		modelPtr := &r.models[len(r.models)-1]
		r.byID[model.ID] = modelPtr

		// Add to tag indices
		for _, tag := range model.Tags {
			r.byTag[tag] = append(r.byTag[tag], modelPtr)
		}
	}

	return nil
}

// applyTagPreferences reorders the byTag lists according to user preferences.
// For each tag in preferences, the specified model IDs are moved to the front
// of that tag's list in the specified order.
func (r *ModelRegistry) applyTagPreferences(preferences map[string][]string) error {
	for tag, preferredIDs := range preferences {
		if err := r.reorderTagModels(tag, preferredIDs); err != nil {
			return fmt.Errorf("failed to apply tag preference for %q: %w", tag, err)
		}
	}
	return nil
}

// reorderTagModels reorders the models for a specific tag.
// Models in preferredIDs are moved to the front in the specified order.
// Models not in preferredIDs maintain their relative order after the preferred ones.
func (r *ModelRegistry) reorderTagModels(tag string, preferredIDs []string) error {
	current := r.byTag[tag]
	if len(current) == 0 {
		// Tag doesn't exist - ignore silently (might be a user-defined tag for custom models)
		return nil
	}

	// Build a set of current model IDs for this tag
	currentSet := make(map[string]*ModelDefinition, len(current))
	for _, model := range current {
		currentSet[model.ID] = model
	}

	// Build the new ordered list
	var reordered []*ModelDefinition

	// First, add preferred models in order
	for _, id := range preferredIDs {
		model, exists := currentSet[id]
		if !exists {
			// Model doesn't have this tag - check if it exists at all
			if _, modelExists := r.byID[id]; !modelExists {
				return fmt.Errorf("unknown model ID in preference: %s", id)
			}
			// Model exists but doesn't have this tag - skip silently
			continue
		}
		reordered = append(reordered, model)
		delete(currentSet, id) // Remove from set so we don't add it again
	}

	// Then, add remaining models in their original order
	for _, model := range current {
		if _, stillInSet := currentSet[model.ID]; stillInSet {
			reordered = append(reordered, model)
		}
	}

	r.byTag[tag] = reordered
	return nil
}

// CreateRegistryWithUserConfig creates a new registry with user configuration applied.
// This is the recommended way to get a configured registry:
//  1. Parses the embedded YAML
//  2. Clones the registry
//  3. Applies user configuration
//
// Returns an unmodified registry if cfg is nil.
// Note: This does not discover local models. Use CreateRegistryWithDiscovery if you
// need local model discovery.
func CreateRegistryWithUserConfig(cfg *UserModelsConfig) (*ModelRegistry, error) {
	return CreateRegistryWithDiscovery(cfg, nil)
}

// CreateRegistryWithDiscovery creates a new registry with user configuration and
// optional local model discovery.
//
// If discoverer is non-nil and cfg.Providers.Local is configured, models will be
// discovered from the local endpoint and added to the registry.
//
// This is the recommended way to get a fully configured registry:
//  1. Parses the embedded YAML
//  2. Clones the registry
//  3. Discovers local models (if configured)
//  4. Applies user configuration
//
// Returns an unmodified registry if cfg is nil.
func CreateRegistryWithDiscovery(cfg *UserModelsConfig, discoverer LocalModelDiscoverer) (*ModelRegistry, error) {
	// Use ParseRegistry() instead of GetRegistry() to create a fresh registry
	// without affecting the global singleton. This allows callers to use
	// SetGlobalRegistry() to install the configured registry.
	baseReg, err := ParseRegistry()
	if err != nil {
		return nil, err
	}

	if cfg == nil {
		return baseReg, nil
	}

	// Clone and apply user config with discovery
	userReg := baseReg.Clone()
	if err := userReg.MergeUserConfigWithDiscovery(cfg, discoverer); err != nil {
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

	// Validate tag preferences
	for tag, preferredIDs := range cfg.TagPreferences {
		for _, id := range preferredIDs {
			// Check if model exists (either built-in or custom)
			if _, exists := baseReg.byID[id]; !exists && !seenIDs[id] {
				warnings = append(warnings, fmt.Sprintf("tag preference %q references unknown model: %s", tag, id))
			}
		}
	}

	return warnings, nil
}
