// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/modelprefs"
)

func init() {
	// load_tool refuses a media tool the user cannot run. The check needs the
	// user's credentials, which live here, and this package imports tools, so
	// the dependency is registered rather than imported.
	tools.SetMediaAvailabilityCheck(CheckMediaAvailable)
}

// mediaProviders returns the providers able to serve a media modality for this
// user.
func mediaProviders(availableDrivers models.AvailableDrivers, _ models.Modality) []string {
	return configuredProviderIDs(availableDrivers)
}

// CheckMediaAvailable reports whether the user has a provider that can serve
// the modality, using the same availability-filtered registry path the
// Resolve*Generator functions use. It returns a *models.MediaUnavailableError
// carrying the one shared message when not.
func CheckMediaAvailable(ctx context.Context, userID string, modality models.Modality) error {
	registry, err := models.GetRegistry()
	if err != nil {
		return nil
	}
	availableDrivers := GetAvailableDrivers(ctx, userID)
	registry = registry.WithAvailability(availableDrivers.Availability)
	providers := mediaProviders(availableDrivers, modality)
	if len(providers) == 0 {
		return models.NewMediaUnavailableError(registry, modality)
	}
	_, err = registry.Resolve(models.ModelSelector{
		Tags:                  []string{mediaBaseTag(modality)},
		RequireOutputModality: modality,
	}, providers)
	if err != nil {
		return models.NewMediaUnavailableError(registry, modality)
	}
	return nil
}

func mediaBaseTag(modality models.Modality) string {
	if modality == models.ModalityVideo {
		return DefaultVideoGenTag
	}
	return DefaultImageGenTag
}

// resolveMediaModel is the registry resolution shared by image and video. A
// user with no provider for the modality gets *models.MediaUnavailableError,
// the same error CheckMediaAvailable returns.
func resolveMediaModel(ctx context.Context, userID string, selector models.ModelSelector, modality models.Modality) (*models.ResolvedModel, models.AvailableDrivers, error) {
	selector.RequireOutputModality = modality

	availableDrivers := GetAvailableDrivers(ctx, userID)
	providers := mediaProviders(availableDrivers, modality)

	registry, err := models.GetRegistry()
	if err != nil {
		return nil, availableDrivers, err
	}
	registry = registry.WithAvailability(availableDrivers.Availability)
	if len(providers) == 0 {
		return nil, availableDrivers, models.NewMediaUnavailableError(registry, modality)
	}

	if resolved := preferredMediaModel(ctx, userID, registry, selector, providers, modality); resolved != nil {
		return resolved, availableDrivers, nil
	}

	resolved, err := registry.Resolve(selector, providers)
	if err != nil {
		if _, baseErr := registry.Resolve(models.ModelSelector{
			Tags: []string{mediaBaseTag(modality)}, RequireOutputModality: modality,
		}, providers); baseErr != nil {
			return nil, availableDrivers, models.NewMediaUnavailableError(registry, modality)
		}
		return nil, availableDrivers, fmt.Errorf("no %s model available: %w", modality, err)
	}
	return resolved, availableDrivers, nil
}

// preferredMediaModel applies the user's Settings preference for the modality's
// base tag (video-gen / image-gen), the same model.tag_config.<tag> row the
// chat tiers use, so no second preference mechanism exists. It applies only to
// the default-tier selector [<base>, flagship]: an explicit model id, a
// workflow-bound selector with providers, or a non-default tier (fast,
// cinematic) are specific requests the preference must not override.
func preferredMediaModel(ctx context.Context, userID string, registry *models.ModelRegistry, selector models.ModelSelector, providers []string, modality models.Modality) *models.ResolvedModel {
	baseTag := mediaBaseTag(modality)
	if selector.ID != "" || len(selector.Providers) > 0 ||
		!slices.Equal(selector.Tags, []string{baseTag, models.TagFlagship}) {
		return nil
	}
	if globalAPIKeyProvider == nil {
		return nil
	}
	all, err := modelprefs.LoadAll(ctx, globalAPIKeyProvider.repo, userID)
	if err != nil {
		logging.Warn("Media tag preferences partially unreadable; affected tags use defaults", "userID", userID, "error", err)
	}
	return all[baseTag].PreferredModel(registry, selector, providers)
}
