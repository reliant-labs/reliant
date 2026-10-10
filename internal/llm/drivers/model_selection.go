// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// sortModels sorts models with newer/best models first, grouped by model family
// Uses centralized priority from models.yaml order
func sortModels(modelList []models.Model) {
	reg := models.MustGetRegistry()
	sort.Slice(modelList, func(i, j int) bool {
		mi, mj := modelList[i], modelList[j]
		miID := string(mi.ID)
		mjID := string(mj.ID)

		// First sort by model family
		fi := models.FamilyPriority[models.GetModelFamily(miID)]
		fj := models.FamilyPriority[models.GetModelFamily(mjID)]
		if fi == 0 {
			fi = 99
		}
		if fj == 0 {
			fj = 99
		}
		if fi != fj {
			return fi < fj
		}

		// Within same family, sort by model priority (from YAML order)
		miPriority := reg.GetModelPriority(miID)
		mjPriority := reg.GetModelPriority(mjID)
		if miPriority != mjPriority {
			return miPriority < mjPriority
		}

		// Fall back to alphabetical by name
		return mi.Name < mj.Name
	})
}

// GetAvailableModelsForUser returns all models user can access with their configured API keys
// Models are sorted by family (Claude, GPT, Gemini) with newest first within each family
func GetAvailableModelsForUser(ctx context.Context, userID string) []models.Model {
	availableDrivers := GetAvailableDrivers(ctx, userID)
	registry := models.MustGetRegistry()
	userVisible := registry.GetUserVisibleModels()

	var available []models.Model

	for _, def := range userVisible {
		modelID := models.ModelID(def.ID)
		// Check if we have a driver that can use this model
		_, found := models.SelectBestDriver(modelID, availableDrivers)
		if found {
			available = append(available, def.ToModel())
			logging.Debug("Model available for user", "userID", userID, "modelID", modelID)
		}
	}

	// Sort models: grouped by family, newest first within each family
	sortModels(available)

	return available
}

// ValidateModelIsAvailable checks if a specific model can be used with configured API keys
func ValidateModelIsAvailable(ctx context.Context, userID string, modelID string) error {
	// Parse model ID to extract base model (handle driver suffix like "gemini-2.5-pro@gemini")
	baseModelID, _ := ParseModelIDWithDriver(modelID)
	registry := models.MustGetRegistry()
	def, ok := registry.GetDefinition(baseModelID)
	if !ok {
		return fmt.Errorf("model %s not found", modelID)
	}

	availableDrivers := GetAvailableDrivers(ctx, userID)
	_, found := models.SelectBestDriver(models.ModelID(def.ID), availableDrivers)

	if !found {
		return fmt.Errorf("model %s is not available with your configured API keys", modelID)
	}

	return nil
}

// ValidateModelSelector validates that a model selector can be resolved with the user's configured API keys.
// This is used for early validation during chat creation to fail fast if the model isn't available.
//
// The selector can be:
//   - map[string]interface{} with "id" and/or "tags" keys
//   - models.ModelSelector struct
//   - *models.ModelSelector pointer
//
// Strings are NOT accepted — model selectors must always be objects.
// String-to-object conversion happens at the gRPC ingestion boundary only.
//
// Returns nil if the model can be resolved, or an error with actionable guidance.
func ValidateModelSelector(ctx context.Context, userID string, selector interface{}) error {
	// Convert selector to models.ModelSelector
	ms, err := toModelSelector(selector)
	if err != nil {
		return err
	}

	// Empty selector is valid - will use default at runtime
	if ms.ID == "" && len(ms.Tags) == 0 {
		return nil
	}

	// Skip validation for mock models (used in tests) - they don't require API keys
	if isTestModel(ms.ID) {
		return nil
	}

	// Get available drivers/providers for the user. A failed settings read is
	// not "no API keys": it must stay distinguishable so callers can retry it.
	availableDrivers, err := LookupAvailableDrivers(ctx, userID)
	if err != nil {
		return err
	}

	// Check if user has any API keys configured
	if len(availableDrivers.Drivers) == 0 {
		return fmt.Errorf("no API keys configured - please add an API key in Settings")
	}

	// Build availableProviders slice from configured drivers
	availableProviders := make([]string, 0, len(availableDrivers.Drivers))
	for driverID, driverConfig := range availableDrivers.Drivers {
		// Use IsConfigured() which handles both API key drivers and local drivers (BaseURL)
		if driverConfig.IsConfigured() {
			availableProviders = append(availableProviders, string(driverID))
		}
	}

	if len(availableProviders) == 0 {
		return fmt.Errorf("no API keys configured - please add an API key in Settings")
	}

	// Try to resolve the model
	registry := models.MustGetRegistry().WithAvailability(availableDrivers.Availability)
	_, err = registry.Resolve(ms, availableProviders)
	if err != nil {
		// A model pinned to a provider that cannot serve the account says
		// which provider, why, and both ways out — the same words the run
		// uses when it meets the pin (see resolutionError in the runtime).
		var pinned *models.ProviderUnavailableError
		if errors.As(err, &pinned) {
			return &unservableModelError{msg: fmt.Sprintf("model '%s' is not available: %s", ms.ID, pinned.Explain(availableDrivers.Unavailable)), cause: err}
		}
		if ms.ID != "" {
			return fmt.Errorf("model '%s' is not available: %w. Check your API key configuration in Settings", ms.ID, err)
		}
		return fmt.Errorf("no model matching tags %v is available: %w. Check your API key configuration in Settings", ms.Tags, err)
	}

	return nil
}

// unservableModelError is a selector no connected provider can serve, worded
// for the user. Its cause stays in the chain, so it still matches
// drivererrors.ErrNoServableProvider.
type unservableModelError struct {
	msg   string
	cause error
}

func (e *unservableModelError) Error() string { return e.msg }
func (e *unservableModelError) Unwrap() error { return e.cause }

// ModelSubstitution is a model selector a send moved off a provider that
// cannot serve the account, onto what the account's connected providers
// offer in its place (models.ModelRegistry.Substitute).
type ModelSubstitution struct {
	// From is the selector's id as it arrived, e.g. "gpt-5.6-sol@codex".
	From string
	// To is the pinned replacement, "<model>@<driver>" — the shape the
	// composer's model picker stores.
	To string
	// Selector is the replacement selector value: the original's fields with
	// id replaced, tags/providers dropped, and the replacement's effort.
	Selector map[string]interface{}
	// Notice tells the user, in the chat, what changed, why, and how to go
	// back.
	Notice string
}

// SubstituteUnservableModel decides the send-time fallback for one model
// selector value (a workflow "model" input): when the selector names a model
// that none of the account's servable providers can serve — its pinned
// provider is not connected, rejected the credential, or does not offer it on
// this plan — it returns the substitute and true. Any other selector,
// including one that resolves, one naming an unknown model, and one with no
// substitute at all, returns false and must be left as it is: the run's own
// resolution then fails it with the explained, terminal error.
//
// This is deliberately a SEND-time decision. The runtime never reroutes a pin
// (defaultGetDriver: a silent switch runs the request on a different bill):
// a send is the user acting, so the switch is announced in the chat and
// recorded on the run's inputs, where every later record names the provider
// that actually serves it.
func SubstituteUnservableModel(value interface{}, available models.AvailableDrivers) (ModelSubstitution, bool) {
	ms, err := toModelSelector(value)
	if err != nil || ms.ID == "" || isTestModel(ms.ID) {
		return ModelSubstitution{}, false
	}
	providers := servableProviderIDs(available)
	if len(providers) == 0 {
		return ModelSubstitution{}, false
	}
	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	_, err = reg.Resolve(ms, providers)
	if err == nil || !errors.Is(err, drivererrors.ErrNoServableProvider) {
		return ModelSubstitution{}, false
	}
	baseID, _ := ParseModelIDWithDriver(ms.ID)
	resolved, ok := reg.Substitute(baseID, providers)
	if !ok {
		return ModelSubstitution{}, false
	}

	to := resolved.Definition.ID + "@" + resolved.Provider.Driver
	selector := map[string]interface{}{}
	if original, isMap := value.(map[string]interface{}); isMap {
		for k, v := range original {
			selector[k] = v
		}
	}
	selector["id"] = to
	delete(selector, "tags")
	delete(selector, "providers")
	// The same model elsewhere keeps the effort the user chose; a different
	// model runs at its tier's effort, which may not even exist on the
	// original's scale.
	if resolved.Definition.ID != baseID {
		if resolved.ThinkingLevel != "" {
			selector["thinking_level"] = resolved.ThinkingLevel
		} else {
			delete(selector, "thinking_level")
		}
	}

	why := err.Error()
	var pinned *models.ProviderUnavailableError
	if errors.As(err, &pinned) {
		why = pinned.Reason(available.Unavailable)
	}
	goBack := fmt.Sprintf("pick %s in the composer's model picker once it is available again", baseID)
	if pinned != nil {
		goBack = fmt.Sprintf("reconnect %s in Settings → Providers, then pick %s in the composer's model picker", pinned.ProviderNames(), baseID)
	}
	notice := fmt.Sprintf("%s. So this chat continues on %s via %s, the same tier on a provider you have connected. To go back, %s.",
		why, resolved.Definition.ID, models.ProviderDisplayName(resolved.Provider.Driver), goBack)

	return ModelSubstitution{From: ms.ID, To: to, Selector: selector, Notice: notice}, true
}

// servableProviderIDs is the account's providers that can take a request,
// sorted so that resolution over them is deterministic.
func servableProviderIDs(available models.AvailableDrivers) []string {
	ids := make([]string, 0, len(available.Drivers))
	for driverID, cfg := range available.Drivers {
		if cfg.IsConfigured() {
			ids = append(ids, string(driverID))
		}
	}
	sort.Strings(ids)
	return ids
}

// isTestModel reports the model ids tests run without any provider.
func isTestModel(id string) bool {
	return id == "mock" || id == "tiny-context" || id == "small-context"
}

// toModelSelector converts various selector formats to models.ModelSelector
func toModelSelector(selector interface{}) (models.ModelSelector, error) {
	switch s := selector.(type) {
	case models.ModelSelector:
		return s, nil
	case *models.ModelSelector:
		if s == nil {
			return models.ModelSelector{}, nil
		}
		return *s, nil
	case string:
		return models.ModelSelector{}, fmt.Errorf("model selector must be an object (e.g. {id: \"model-name\"}), got string %q — strings are not accepted; convert to {id: string} at the system boundary", s)
	case map[string]interface{}:
		ms := models.ModelSelector{}
		if id, ok := s["id"].(string); ok {
			ms.ID = id
		}
		if tags, ok := s["tags"].([]interface{}); ok {
			for _, t := range tags {
				if str, ok := t.(string); ok {
					ms.Tags = append(ms.Tags, str)
				}
			}
		}
		if tags, ok := s["tags"].([]string); ok {
			ms.Tags = tags
		}
		if providers, ok := s["providers"].([]interface{}); ok {
			for _, p := range providers {
				if str, ok := p.(string); ok {
					ms.Providers = append(ms.Providers, str)
				}
			}
		}
		if providers, ok := s["providers"].([]string); ok {
			ms.Providers = providers
		}
		return ms, nil
	default:
		return models.ModelSelector{}, fmt.Errorf("unsupported model selector type: %T", selector)
	}
}
