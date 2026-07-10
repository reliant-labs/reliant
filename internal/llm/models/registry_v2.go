// Copyright (c) 2025 Reliant Labs
package models

import (
	"embed"
	"fmt"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed definitions/models.yaml
var modelsYAML embed.FS

// ModelRegistry holds the parsed models and provides lookup capabilities.
// It preserves the order from the YAML file for deterministic tag resolution.
type ModelRegistry struct {
	models []ModelDefinition             // Preserves order from YAML
	byID   map[string]*ModelDefinition   // Fast lookup by model ID
	byTag  map[string][]*ModelDefinition // tag -> models with that tag (in order)
}

// ProviderPriority defines the resolution priority for providers.
// Lower numbers have higher priority.
var ProviderPriority = map[string]int{
	"anthropic":  1,
	"codex":      1,
	"copilot":    1,
	"openai":     1,
	"gemini":     1,
	"xai":        1,
	"vertexai":   1,
	"reliant":    1,
	"local":      2,
	"openrouter": 10,
}

var (
	globalRegistry     *ModelRegistry
	globalRegistryMu   sync.RWMutex
	globalRegistryOnce sync.Once
	globalRegistryErr  error
	registryReplaced   bool // true if SetGlobalRegistry was called
)

// GetRegistry returns the global model registry, initializing it if necessary.
// This is thread-safe. If SetGlobalRegistry was called, returns that registry.
// Otherwise, returns the default registry parsed from embedded YAML.
func GetRegistry() (*ModelRegistry, error) {
	globalRegistryMu.RLock()
	if registryReplaced {
		reg, err := globalRegistry, globalRegistryErr
		globalRegistryMu.RUnlock()
		return reg, err
	}
	globalRegistryMu.RUnlock()

	// Lazy init the default registry
	globalRegistryOnce.Do(func() {
		globalRegistryMu.Lock()
		defer globalRegistryMu.Unlock()
		if !registryReplaced {
			globalRegistry, globalRegistryErr = ParseRegistry()
		}
	})

	globalRegistryMu.RLock()
	defer globalRegistryMu.RUnlock()
	return globalRegistry, globalRegistryErr
}

// MustGetRegistry returns the global registry or panics if initialization fails.
// Use this only when you're certain the registry is valid (e.g., after startup checks).
func MustGetRegistry() *ModelRegistry {
	reg, err := GetRegistry()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize model registry: %v", err))
	}
	return reg
}

// SetGlobalRegistry replaces the global registry with the provided registry.
// This can be called at any time during startup to install a user-configured
// registry. Once called, GetRegistry() will return this registry.
//
// This is safe to call even if init() functions have already called GetRegistry(),
// as the replacement will take effect for all future calls.
//
// Typical usage:
//
//	if cfg.Models != nil {
//	    reg, err := models.CreateRegistryWithUserConfig(cfg.Models)
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    models.SetGlobalRegistry(reg)
//	}
//	// Now GetRegistry() and MustGetRegistry() will return the configured registry
func SetGlobalRegistry(reg *ModelRegistry) {
	globalRegistryMu.Lock()
	defer globalRegistryMu.Unlock()
	globalRegistry = reg
	globalRegistryErr = nil
	registryReplaced = true
}

// InitGlobalRegistryWithUserConfig initializes the global registry with user configuration.
// If cfg is nil, the default embedded registry is used.
// This should be called once during application startup, before any other code
// accesses the registry.
//
// Note: This does NOT discover local models. Use InitGlobalRegistryWithDiscovery
// to enable local model discovery.
//
// Returns an error if registry initialization fails.
func InitGlobalRegistryWithUserConfig(cfg *UserModelsConfig) error {
	return InitGlobalRegistryWithDiscovery(cfg, nil)
}

// InitGlobalRegistryWithDiscovery initializes the global registry with user configuration
// and optional local model discovery.
//
// If cfg is nil, the default embedded registry is used.
// If discoverer is non-nil and cfg.Providers.Local is configured, models will be
// discovered from the local endpoint and added to the registry.
//
// This should be called once during application startup, before any other code
// accesses the registry.
//
// Returns an error if registry initialization fails.
func InitGlobalRegistryWithDiscovery(cfg *UserModelsConfig, discoverer LocalModelDiscoverer) error {
	if cfg == nil {
		// No user config - just ensure the default registry is initialized
		_, err := GetRegistry()
		return err
	}

	// Create registry with user config and discovery
	reg, err := CreateRegistryWithDiscovery(cfg, discoverer)
	if err != nil {
		return fmt.Errorf("failed to create registry with user config: %w", err)
	}

	// Set as global registry
	SetGlobalRegistry(reg)
	return nil
}

// ParseRegistry parses the embedded YAML and builds a new ModelRegistry.
func ParseRegistry() (*ModelRegistry, error) {
	data, err := modelsYAML.ReadFile("definitions/models.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded models.yaml: %w", err)
	}

	var config ModelsConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse models.yaml: %w", err)
	}

	return buildRegistry(config.Models)
}

// ParseRegistryFromBytes parses a YAML byte slice into a ModelRegistry.
// Useful for testing or loading from alternative sources.
func ParseRegistryFromBytes(data []byte) (*ModelRegistry, error) {
	var config ModelsConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse models YAML: %w", err)
	}
	return buildRegistry(config.Models)
}

// buildRegistry creates a ModelRegistry from a slice of model definitions.
func buildRegistry(models []ModelDefinition) (*ModelRegistry, error) {
	reg := &ModelRegistry{
		models: models,
		byID:   make(map[string]*ModelDefinition, len(models)),
		byTag:  make(map[string][]*ModelDefinition),
	}

	for i := range models {
		model := &reg.models[i]

		// Check for duplicate IDs
		if _, exists := reg.byID[model.ID]; exists {
			return nil, fmt.Errorf("duplicate model ID: %s", model.ID)
		}
		reg.byID[model.ID] = model

		// Index by tags
		for _, tag := range model.Tags {
			reg.byTag[tag] = append(reg.byTag[tag], model)
		}
	}

	return reg, nil
}

// GetDefinition returns the model definition for the given ID.
func (r *ModelRegistry) GetDefinition(id string) (*ModelDefinition, bool) {
	model, ok := r.byID[id]
	return model, ok
}

// GetModelsByTag returns all models that have the given tag, in definition order.
func (r *ModelRegistry) GetModelsByTag(tag string) []*ModelDefinition {
	return r.byTag[tag]
}

// ListAll returns all model definitions in definition order.
func (r *ModelRegistry) ListAll() []ModelDefinition {
	result := make([]ModelDefinition, len(r.models))
	copy(result, r.models)
	return result
}

// GetUserVisibleModels returns all models that should be shown in user-facing UI.
// This includes models with VisibilityUser or empty visibility (which defaults to user-visible).
// Models with VisibilityMeta or VisibilityDev are excluded.
func (r *ModelRegistry) GetUserVisibleModels() []*ModelDefinition {
	var result []*ModelDefinition
	for i := range r.models {
		model := &r.models[i]
		// Include if visibility is "user" or empty (default)
		if model.Visibility == VisibilityUser || model.Visibility == "" {
			result = append(result, model)
		}
	}
	return result
}

// ListModelsByProvider returns all models that have a provider mapping for the given driver.
// For example, ListModelsByProvider("anthropic") returns all models that can be used with Anthropic.
func (r *ModelRegistry) ListModelsByProvider(provider string) []ModelDefinition {
	var result []ModelDefinition
	for _, model := range r.models {
		for _, p := range model.Providers {
			if p.Driver == provider {
				result = append(result, model)
				break
			}
		}
	}
	return result
}

// ListAllTags returns all unique tags across all models.
func (r *ModelRegistry) ListAllTags() []string {
	tags := make([]string, 0, len(r.byTag))
	for tag := range r.byTag {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return tags
}

// Resolve takes a ModelSelector and available providers, and returns the best match.
// Resolution rules:
//  1. If selector.ID is set, find exact match by ID
//  2. If selector.Tags is set, use best-match scoring (not strict AND):
//     - Earlier tags in the list have higher weight
//     - Models are scored by sum of weights for matching tags
//     - Highest scoring models are tried first
//     - Falls back gracefully if no perfect match exists
//  3. For each candidate, find a provider that's in availableProviders
//  4. If selector.Providers is set, try each in order (first available wins)
//  5. Return error if no match found
//
// Provider priority: native drivers (anthropic, openai, gemini, xai, vertexai) have
// priority 1, openrouter has priority 10. Within candidates, pick the model that
// appears first in the YAML (definition order).
func (r *ModelRegistry) Resolve(selector ModelSelector, availableProviders []string) (*ResolvedModel, error) {
	if selector.ID == "" && len(selector.Tags) == 0 {
		return nil, fmt.Errorf("ModelSelector must have either ID or Tags set")
	}

	// Build a set of available providers for fast lookup
	availableSet := make(map[string]bool, len(availableProviders))
	for _, p := range availableProviders {
		availableSet[p] = true
	}

	// Case 1: Resolve by exact ID
	if selector.ID != "" {
		// Parse ID to handle @driver suffix (e.g. "model@provider")
		idToLookup := selector.ID
		var driverSuffix string

		if idx := strings.LastIndex(selector.ID, "@"); idx != -1 {
			idToLookup = selector.ID[:idx]
			driverSuffix = selector.ID[idx+1:]
		}

		model, ok := r.byID[idToLookup]
		if !ok {
			return nil, fmt.Errorf("model not found: %s", selector.ID)
		}

		// Use driver suffix if present, otherwise use selector's providers
		// Driver suffix (@provider) is a hard constraint; selector.Providers is a preference
		preferredProviders := selector.Providers
		hardConstraint := false
		if driverSuffix != "" {
			preferredProviders = []string{driverSuffix}
			hardConstraint = true // @suffix means user explicitly wants this provider
		}

		provider, err := r.findBestProvider(model, preferredProviders, availableSet, hardConstraint)
		if err != nil {
			return nil, err
		}
		return &ResolvedModel{
			Definition: *model,
			Provider:   *provider,
		}, nil
	}

	// Case 2: Resolve by tags using best-match scoring
	candidates := r.findModelsByBestMatch(selector.Tags)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no models found matching tags: %v", selector.Tags)
	}

	// Find the first candidate (in definition order) with an available provider
	for _, model := range candidates {
		provider, err := r.findBestProvider(model, selector.Providers, availableSet, false)
		if err == nil {
			return &ResolvedModel{
				Definition: *model,
				Provider:   *provider,
			}, nil
		}
	}

	return nil, fmt.Errorf("no available provider for models with tags: %v (tried %d candidates)", selector.Tags, len(candidates))
}

// modelScore holds a model and its match score for sorting.
type modelScore struct {
	model *ModelDefinition
	score int
	index int // Original index for stable sorting
}

// findModelsByBestMatch returns models sorted by how well they match the given tags.
// Earlier tags in the list have higher weight. Models are sorted by:
//  1. Total score (higher is better)
//  2. Definition order (earlier in YAML wins ties)
//
// This enables graceful degradation: tags: [local, fast] will prefer models
// that have both tags, but will fall back to models with just "local" or just
// "fast" if no perfect match exists.
//
// Scoring: For tags [t1, t2, t3], weights are [4, 2, 1] (powers of 2, descending).
// This ensures earlier tags always outweigh combinations of later tags.
func (r *ModelRegistry) findModelsByBestMatch(tags []string) []*ModelDefinition {
	if len(tags) == 0 {
		return nil
	}

	// Calculate weights: powers of 2, with first tag having highest weight
	// e.g., [local, fast, cheap] -> weights [4, 2, 1]
	weights := make([]int, len(tags))
	for i := range tags {
		weights[i] = 1 << (len(tags) - 1 - i) // 2^(n-1-i)
	}

	// Score all models
	var scored []modelScore
	for i := range r.models {
		model := &r.models[i]
		score := r.scoreModelTags(model, tags, weights)
		if score > 0 {
			scored = append(scored, modelScore{
				model: model,
				score: score,
				index: i,
			})
		}
	}

	if len(scored) == 0 {
		return nil
	}

	// Sort by score descending, then by index ascending (stable sort)
	slices.SortStableFunc(scored, func(a, b modelScore) int {
		if a.score != b.score {
			return b.score - a.score // Higher score first
		}
		return a.index - b.index // Earlier definition first
	})

	// Extract sorted models
	result := make([]*ModelDefinition, len(scored))
	for i, s := range scored {
		result[i] = s.model
	}

	return result
}

// scoreModelTags calculates the weighted score for a model based on matching tags.
func (r *ModelRegistry) scoreModelTags(model *ModelDefinition, tags []string, weights []int) int {
	tagSet := make(map[string]bool, len(model.Tags))
	for _, t := range model.Tags {
		tagSet[t] = true
	}

	score := 0
	for i, tag := range tags {
		if tagSet[tag] {
			score += weights[i]
		}
	}
	return score
}

// findBestProvider finds the best available provider for a model.
// If preferredProviders is set, tries each in order and returns the first available.
// If no preferred provider is available:
//   - If hardConstraint is true, returns an error (used for explicit @provider suffix)
//   - If hardConstraint is false, falls back to system priority (used for selector.Providers)
func (r *ModelRegistry) findBestProvider(model *ModelDefinition, preferredProviders []string, availableSet map[string]bool, hardConstraint bool) (*ProviderMapping, error) {
	// If preferred providers specified, try them in order
	if len(preferredProviders) > 0 {
		for _, preferred := range preferredProviders {
			for i := range model.Providers {
				p := &model.Providers[i]
				if p.Driver == preferred && availableSet[p.Driver] {
					return p, nil
				}
			}
		}
		// None of the preferred providers available
		if hardConstraint {
			return nil, fmt.Errorf("none of required providers %v available for model %s", preferredProviders, model.ID)
		}
		// Fall through to system priority
	}

	// Find best available provider by system priority. Iteration follows the
	// model's YAML provider order, so selection is deterministic; ties keep
	// the earlier YAML entry, except that user-owned (BYO) credentials always
	// beat the managed reliant driver on a priority tie — a user who
	// connected their own subscription expects it to be used.
	var bestProvider *ProviderMapping
	bestPriority := 1000 // Start with a high number

	for i := range model.Providers {
		p := &model.Providers[i]
		if !availableSet[p.Driver] {
			continue
		}

		priority, ok := ProviderPriority[p.Driver]
		if !ok {
			priority = 5 // Default priority for unknown providers
		}

		switch {
		case priority < bestPriority:
			bestPriority = priority
			bestProvider = p
		case priority == bestPriority && bestProvider != nil &&
			IsManagedDriver(DriverID(bestProvider.Driver)) && !IsManagedDriver(DriverID(p.Driver)):
			// Priority tie: BYO beats managed regardless of YAML order.
			bestProvider = p
		}
	}

	if bestProvider == nil {
		return nil, fmt.Errorf("no available provider for model %s", model.ID)
	}

	return bestProvider, nil
}

// ResolveWithFallback attempts to resolve with the primary selector, falling back
// to the fallback selector if the primary fails.
func (r *ModelRegistry) ResolveWithFallback(primary, fallback ModelSelector, availableProviders []string) (*ResolvedModel, error) {
	result, err := r.Resolve(primary, availableProviders)
	if err == nil {
		return result, nil
	}

	// Try fallback
	result, fallbackErr := r.Resolve(fallback, availableProviders)
	if fallbackErr == nil {
		return result, nil
	}

	// Return the original error for context
	return nil, fmt.Errorf("primary: %w; fallback: %v", err, fallbackErr)
}

// Clone creates a deep copy of the registry.
// This is useful for applying user configurations without modifying the global registry.
func (r *ModelRegistry) Clone() *ModelRegistry {
	cloned := &ModelRegistry{
		models: make([]ModelDefinition, len(r.models)),
		byID:   make(map[string]*ModelDefinition, len(r.byID)),
		byTag:  make(map[string][]*ModelDefinition),
	}

	// Deep copy models
	copy(cloned.models, r.models)

	// Rebuild indices pointing to the new models slice
	for i := range cloned.models {
		model := &cloned.models[i]
		cloned.byID[model.ID] = model
		for _, tag := range model.Tags {
			cloned.byTag[tag] = append(cloned.byTag[tag], model)
		}
	}

	return cloned
}

// GetModelPriority returns the priority of a model based on its position in models.yaml.
// Lower numbers have higher priority. Returns 999 for unknown models.
func (r *ModelRegistry) GetModelPriority(modelID string) int {
	for i, model := range r.models {
		if model.ID == modelID {
			return i + 1 // 1-indexed
		}
	}
	return 999 // Unknown model
}

// GetModelFamily returns the family of a model (claude, openai, gemini, grok, etc.)
func GetModelFamily(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "claude"), strings.HasPrefix(modelID, "vertex-claude"):
		return "claude"
	case strings.HasPrefix(modelID, "gpt"):
		return "openai"
	case strings.HasPrefix(modelID, "gemini"), strings.HasPrefix(modelID, "vertex-gemini"):
		return "gemini"
	case strings.HasPrefix(modelID, "grok"):
		return "grok"
	default:
		return "other"
	}
}

// FamilyPriority returns the display order for model families.
// Lower numbers appear first.
var FamilyPriority = map[string]int{
	"claude": 1,
	"openai": 2,
	"gemini": 3,
	"grok":   4,
	"other":  99,
}
