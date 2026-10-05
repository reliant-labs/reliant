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
//
// Models and tags are independent: models describe capability, cost and
// providers; tags are ordered {model, thinking_level} lists that decide which
// model a tier resolves to and how hard it thinks. A model's tags are derived
// from those lists, never declared on the model.
type ModelRegistry struct {
	models []ModelDefinition           // Preserves order from YAML (display priority)
	byID   map[string]*ModelDefinition // Fast lookup by model ID
	tags   map[string][]TagEntry       // tag -> ordered entries (resolution order)

	// avail is the per-account availability filter set by WithAvailability.
	// Nil on the shared global registry.
	avail AvailabilityFunc
}

// WithAvailability returns a view of the registry whose Resolve honors a
// per-(driver, model) availability filter: a provider the filter reports
// Disabled for a model is skipped for that model only. An explicit id fails
// with the provider's reason; tag resolution falls through to the next servable
// candidate. The view shares the (read-only) catalog, so it is cheap to make per
// request; a nil filter returns the registry unchanged.
func (r *ModelRegistry) WithAvailability(avail AvailabilityFunc) *ModelRegistry {
	if avail == nil {
		return r
	}
	view := *r
	view.avail = avail
	return &view
}

// ProviderPriority defines the resolution priority for providers.
// Lower numbers have higher priority.
var ProviderPriority = map[string]int{
	"anthropic":   1,
	"antigravity": 1,
	"codex":       1,
	"copilot":     1,
	"openai":      1,
	"gemini":      1,
	"vertexai":    1,
	"reliant":     1,
	"local":       2,
	"openrouter":  10,
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
func InitGlobalRegistryWithUserConfig(cfg *UserModelsConfig) error {
	if cfg == nil {
		_, err := GetRegistry()
		return err
	}
	reg, err := CreateRegistryWithUserConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create registry with user config: %w", err)
	}
	SetGlobalRegistry(reg)
	return nil
}

// ParseRegistry parses the embedded YAML and builds a new ModelRegistry.
func ParseRegistry() (*ModelRegistry, error) {
	data, err := modelsYAML.ReadFile("definitions/models.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded models.yaml: %w", err)
	}

	return ParseRegistryFromBytes(data)
}

// ParseRegistryFromBytes parses a YAML byte slice into a ModelRegistry.
// Useful for testing or loading from alternative sources.
func ParseRegistryFromBytes(data []byte) (*ModelRegistry, error) {
	var config ModelsConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse models YAML: %w", err)
	}
	return buildRegistry(config.Models, config.Tags)
}

// buildRegistry creates a ModelRegistry from model definitions and the tag
// lists that map tiers onto them.
func buildRegistry(models []ModelDefinition, tags map[string][]TagEntry) (*ModelRegistry, error) {
	reg := &ModelRegistry{
		models: models,
		byID:   make(map[string]*ModelDefinition, len(models)),
		tags:   make(map[string][]TagEntry, len(tags)),
	}

	for i := range models {
		model := &reg.models[i]
		if _, exists := reg.byID[model.ID]; exists {
			return nil, fmt.Errorf("duplicate model ID: %s", model.ID)
		}
		reg.byID[model.ID] = model
	}

	for tag, entries := range tags {
		if err := reg.setTag(tag, entries); err != nil {
			return nil, err
		}
	}

	return reg, nil
}

// setTag validates and installs one tag's entries.
//
// Every check fails the parse rather than warning. An entry naming a model
// nothing defines, a level nothing understands, or a model listed twice (the
// second position can never win) does nothing at runtime and looks exactly
// like working config — failing loudly at startup is the only way that typo
// is ever noticed.
func (r *ModelRegistry) setTag(tag string, entries []TagEntry) error {
	if tag == "" {
		return fmt.Errorf("tags: empty tag name")
	}
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if _, ok := r.byID[entry.Model]; !ok {
			return fmt.Errorf("tags[%q][%d]: unknown model %q", tag, i, entry.Model)
		}
		if entry.ThinkingLevel != "" && !IsKnownThinkingLevel(entry.ThinkingLevel) {
			return fmt.Errorf("tags[%q][%d] (%s): unknown thinking level %q (must be one of: %s)",
				tag, i, entry.Model, entry.ThinkingLevel, strings.Join(KnownThinkingLevels, ", "))
		}
		if seen[entry.Model] {
			return fmt.Errorf("tags[%q]: model %q listed more than once", tag, entry.Model)
		}
		seen[entry.Model] = true
	}
	r.tags[tag] = slices.Clone(entries)
	return nil
}

// GetDefinition returns the model definition for the given ID.
func (r *ModelRegistry) GetDefinition(id string) (*ModelDefinition, bool) {
	model, ok := r.byID[id]
	return model, ok
}

// TagEntries returns a tag's ordered entries — the models that serve it and
// the effort each runs at — in resolution order.
func (r *ModelRegistry) TagEntries(tag string) []TagEntry {
	return slices.Clone(r.tags[tag])
}

// GetModelsByTag returns the models listed under a tag, in resolution order.
func (r *ModelRegistry) GetModelsByTag(tag string) []*ModelDefinition {
	entries := r.tags[tag]
	out := make([]*ModelDefinition, 0, len(entries))
	for _, entry := range entries {
		out = append(out, r.byID[entry.Model])
	}
	return out
}

// TagsOf returns every tag that lists the model, sorted. A model has no tags
// of its own; this is the derived view surfaces like ListModels report.
func (r *ModelRegistry) TagsOf(modelID string) []string {
	var out []string
	for tag, entries := range r.tags {
		if slices.ContainsFunc(entries, func(e TagEntry) bool { return e.Model == modelID }) {
			out = append(out, tag)
		}
	}
	slices.Sort(out)
	return out
}

// ThinkingLevelFor is the effort a model runs at when chosen by id — or via a
// tag entry that declares none: the model's capability default, "" when it
// cannot reason.
func ThinkingLevelFor(model *ModelDefinition) string {
	return ResolveThinkingCapability(model.Capabilities).DefaultLevel
}

// ListAll returns all model definitions in definition order.
func (r *ModelRegistry) ListAll() []ModelDefinition {
	result := make([]ModelDefinition, len(r.models))
	copy(result, r.models)
	return result
}

// GetUserVisibleModels returns the models shown on chat-model surfaces: the
// per-chat picker, ListModels, model preferences, and anything else that lets a
// user pick the model a conversation runs on.
//
// It is GetUserVisibleModelsForModality(ModalityText). Chat is a text-output
// operation, so a model that cannot emit text has no chat-completions endpoint
// and would fail every request the picker could send it. Image-generation
// models (output_modalities: [image]) are therefore excluded here even though
// they are visibility: user — they are user-visible, just not on this surface.
//
// A multi-modal model declaring output_modalities: [text, image] still appears,
// because it can serve a chat request.
//
// If you want user-visible models irrespective of what they emit, use
// GetUserVisibleModelsForModality with the modality your surface needs, or
// ListAll for the whole registry.
func (r *ModelRegistry) GetUserVisibleModels() []*ModelDefinition {
	return r.GetUserVisibleModelsForModality(ModalityText)
}

// GetUserVisibleModelsForModality returns all models that should be shown in
// user-facing UI for an operation that produces the given output modality.
//
// Visibility and capability are two independent gates and both apply: models
// with VisibilityMeta or VisibilityDev are excluded regardless of modality, and
// models that cannot produce the requested modality are excluded regardless of
// visibility. Modality is read through ModelCapabilities.CanOutput, so a
// definition that declares no output_modalities counts as text-only.
func (r *ModelRegistry) GetUserVisibleModelsForModality(modality Modality) []*ModelDefinition {
	var result []*ModelDefinition
	for i := range r.models {
		model := &r.models[i]
		// Include if visibility is "user" or empty (default)
		if model.Visibility != VisibilityUser && model.Visibility != "" {
			continue
		}
		if !model.Capabilities.CanOutput(modality) {
			continue
		}
		result = append(result, model)
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

// ListAllTags returns every declared tag, sorted.
func (r *ModelRegistry) ListAllTags() []string {
	tags := make([]string, 0, len(r.tags))
	for tag := range r.tags {
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
//     - Models are scored by the weights of the selector tags that list them
//     - Highest scoring models are tried first; ties go to position in the
//     earliest matching tag's list
//     - Falls back gracefully if no perfect match exists
//  3. For each candidate, find a provider that's in availableProviders
//  4. If selector.Providers is set, try each in order (first available wins)
//  5. Return error if no match found
//
// The result's ThinkingLevel is the effort to run at absent an explicit one:
// for tag selection, the level of the entry that won under the EARLIEST
// selector tag listing the model (clamped to what the model supports); for id
// selection, or an entry declaring none, the model's capability default.
//
// Provider priority: native drivers (anthropic, openai, gemini, vertexai) have
// priority 1, openrouter has priority 10.
func (r *ModelRegistry) Resolve(selector ModelSelector, availableProviders []string) (*ResolvedModel, error) {
	avail := r.avail
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

		// An explicitly named model still has to be able to do the job. Failing
		// here names both the model and the modality, which is a far better
		// error than whatever the driver would produce on a generation call to
		// a text-only endpoint.
		if selector.RequireOutputModality != "" && !model.Capabilities.CanOutput(selector.RequireOutputModality) {
			return nil, fmt.Errorf("model %s cannot generate %s (it produces %v)",
				model.ID, selector.RequireOutputModality, model.Capabilities.EffectiveOutputModalities())
		}

		// Use driver suffix if present, otherwise use selector's providers
		// Driver suffix (@provider) is a hard constraint; selector.Providers is a preference
		preferredProviders := selector.Providers
		hardConstraint := false
		if driverSuffix != "" {
			preferredProviders = []string{driverSuffix}
			hardConstraint = true // @suffix means user explicitly wants this provider
		}

		servable, disabledReason := restrictProviders(model, availableSet, avail)
		provider, err := r.findBestProvider(model, preferredProviders, servable, hardConstraint)
		if err != nil {
			if disabledReason != "" {
				return nil, fmt.Errorf("model %s is unavailable: %s", model.ID, disabledReason)
			}
			return nil, err
		}
		return resolvedWithAvailability(model, provider, ThinkingLevelFor(model), avail), nil
	}

	// Case 2: Resolve by tags using best-match scoring
	candidates := r.findCandidatesByBestMatch(selector.Tags)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no models found matching tags: %v", selector.Tags)
	}

	// Apply the modality filter BEFORE provider selection. Tag scoring degrades
	// gracefully by design, so without this a request for an image model would
	// silently settle for whichever text model happened to match a secondary
	// tag like "cheap".
	if selector.RequireOutputModality != "" {
		candidates = slices.DeleteFunc(candidates, func(c tagCandidate) bool {
			return !c.model.Capabilities.CanOutput(selector.RequireOutputModality)
		})
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no models matching tags %v can generate %s",
				selector.Tags, selector.RequireOutputModality)
		}
	}

	// The first candidate with an available provider wins, at its entry's effort.
	for _, candidate := range candidates {
		servable, _ := restrictProviders(candidate.model, availableSet, avail)
		provider, err := r.findBestProvider(candidate.model, selector.Providers, servable, false)
		if err == nil {
			level := ThinkingLevelFor(candidate.model)
			if candidate.entry.ThinkingLevel != "" {
				level = ClampThinkingLevel(ResolveThinkingCapability(candidate.model.Capabilities), candidate.entry.ThinkingLevel)
			}
			return resolvedWithAvailability(candidate.model, provider, level, avail), nil
		}
	}

	return nil, fmt.Errorf("no available provider for models with tags: %v (tried %d candidates)", selector.Tags, len(candidates))
}

// tagCandidate is a model a tag selector may resolve to, with the entry that
// put it there — the one under the EARLIEST selector tag listing the model,
// which is the entry whose effort the request carries.
type tagCandidate struct {
	model *ModelDefinition
	entry TagEntry
	score int
	// rank orders ties: (index of the entry's tag in the selector, position of
	// the entry within that tag's list).
	tagIndex, position int
}

// findCandidatesByBestMatch returns every model listed under any selector tag,
// best match first. Sort order:
//  1. Total score (higher is better)
//  2. Earliest selector tag the model is listed under
//  3. Position within that tag's list
//
// This enables graceful degradation: tags: [local, fast] prefers a model
// listed under both, but falls back to models listed under just one.
//
// Scoring: For tags [t1, t2, t3], weights are [4, 2, 1] (powers of 2, descending).
// This ensures earlier tags always outweigh combinations of later tags.
func (r *ModelRegistry) findCandidatesByBestMatch(tags []string) []tagCandidate {
	byModel := make(map[string]*tagCandidate)
	var order []string
	for i, tag := range tags {
		weight := 1 << (len(tags) - 1 - i) // 2^(n-1-i)
		for position, entry := range r.tags[tag] {
			candidate, seen := byModel[entry.Model]
			if !seen {
				candidate = &tagCandidate{model: r.byID[entry.Model], entry: entry, tagIndex: i, position: position}
				byModel[entry.Model] = candidate
				order = append(order, entry.Model)
			}
			candidate.score += weight
		}
	}

	out := make([]tagCandidate, 0, len(order))
	for _, id := range order {
		out = append(out, *byModel[id])
	}
	slices.SortStableFunc(out, func(a, b tagCandidate) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if a.tagIndex != b.tagIndex {
			return a.tagIndex - b.tagIndex
		}
		return a.position - b.position
	})
	return out
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
		models: slices.Clone(r.models),
		byID:   make(map[string]*ModelDefinition, len(r.byID)),
		tags:   make(map[string][]TagEntry, len(r.tags)),
	}
	for tag, entries := range r.tags {
		cloned.tags[tag] = slices.Clone(entries)
	}
	// Rebuild the index pointing into the new models slice.
	for i := range cloned.models {
		cloned.byID[cloned.models[i].ID] = &cloned.models[i]
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
