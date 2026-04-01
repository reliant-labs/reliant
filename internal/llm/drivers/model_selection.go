// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"sort"

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
	if ms.ID == "mock" || ms.ID == "tiny-context" || ms.ID == "small-context" {
		return nil
	}

	// Get available drivers/providers for the user
	availableDrivers := GetAvailableDrivers(ctx, userID)

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
	registry := models.MustGetRegistry()
	_, err = registry.Resolve(ms, availableProviders)
	if err != nil {
		// Provide actionable error message
		if ms.ID != "" {
			return fmt.Errorf("model '%s' is not available: %w. Check your API key configuration in Settings", ms.ID, err)
		}
		return fmt.Errorf("no model matching tags %v is available: %w. Check your API key configuration in Settings", ms.Tags, err)
	}

	return nil
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
