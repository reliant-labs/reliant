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
	"github.com/reliant-labs/reliant/internal/orgpolicy"
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
// Resolve*Generator functions use, in the scope of the run the call belongs
// to (WithRunScope). It returns a *models.MediaUnavailableError carrying the
// one shared message when not, or the org policy's *orgpolicy.Refusal when
// the policy is why.
func CheckMediaAvailable(ctx context.Context, userID string, modality models.Modality) error {
	registry, err := models.GetRegistry()
	if err != nil {
		return nil
	}
	scope := runScopeFor(ctx, userID)
	availableDrivers := GetAvailableDrivers(ctx, scope)
	registry = registry.WithAvailability(availableDrivers.Availability)
	selector := models.ModelSelector{
		Tags:                  []string{mediaBaseTag(modality)},
		RequireOutputModality: modality,
	}
	if providers := mediaProviders(availableDrivers, modality); len(providers) > 0 {
		if _, err = registry.Resolve(selector, providers); err == nil {
			return nil
		}
	}
	if refusal := mediaPolicyRefusal(ctx, scope, availableDrivers, selector, false); refusal != nil {
		return refusal
	}
	return models.NewMediaUnavailableError(registry, modality)
}

// mediaPolicyRefusal is the org policy's refusal when what it took out of
// scope's candidates would have served selector (OrgPolicyRefusal), so a
// generation the policy forbids says so instead of "connect a provider".
func mediaPolicyRefusal(ctx context.Context, scope Scope, available models.AvailableDrivers, selector models.ModelSelector, counted bool) *orgpolicy.Refusal {
	refusal := OrgPolicyRefusal(ctx, scope, available, selector, counted)
	if refusal != nil {
		refusal.Unattended = !scope.Attended
	}
	return refusal
}

// mediaFunding is who pays for a generation served from driver: the funding
// of the driver's default account, the one available.Drivers holds.
func mediaFunding(available models.AvailableDrivers, driver string) string {
	accounts := available.Accounts[models.DriverID(driver)]
	def := available.Drivers[models.DriverID(driver)]
	for _, acct := range accounts {
		if acct.AccountID == def.AccountID {
			return acct.Funding
		}
	}
	if len(accounts) > 0 {
		return accounts[0].Funding
	}
	return ""
}

// checkMediaPolicy is the driver build's check of a generation (as call_llm's
// is of a model call, org-policies §4.2): the org's policy, judged on the
// account it is finally served from, so a generation is refused even when it
// reached the build around the loader. It counts the generation's one judged
// use. Work of no org is never refused.
func checkMediaPolicy(ctx context.Context, scope Scope, available models.AvailableDrivers, resolved *models.ResolvedModel) error {
	driver := resolved.Provider.Driver
	err := CheckOrgPolicy(ctx, scope, models.DriverID(driver), mediaFunding(available, driver), resolved.Definition.ID, true)
	if refusal, ok := orgpolicy.RefusalOf(err); ok {
		refusal.Unattended = !scope.Attended
		return refusal
	}
	return err
}

func mediaBaseTag(modality models.Modality) string {
	if modality == models.ModalityVideo {
		return DefaultVideoGenTag
	}
	return DefaultImageGenTag
}

// resolveMediaModel is the registry resolution shared by image and video, in
// the scope of the run the call belongs to (WithRunScope): a chat's org and
// who is behind it, as its model calls resolve. A user with no provider for
// the modality gets *models.MediaUnavailableError, the same error
// CheckMediaAvailable returns, or the org policy's *orgpolicy.Refusal when
// the policy is why.
func resolveMediaModel(ctx context.Context, userID string, selector models.ModelSelector, modality models.Modality) (*models.ResolvedModel, models.AvailableDrivers, Scope, error) {
	selector.RequireOutputModality = modality

	scope := runScopeFor(ctx, userID)
	availableDrivers := GetAvailableDrivers(ctx, scope)
	providers := mediaProviders(availableDrivers, modality)

	registry, err := models.GetRegistry()
	if err != nil {
		return nil, availableDrivers, scope, err
	}
	registry = registry.WithAvailability(availableDrivers.Availability)
	if len(providers) == 0 {
		if refusal := mediaPolicyRefusal(ctx, scope, availableDrivers, selector, true); refusal != nil {
			return nil, availableDrivers, scope, refusal
		}
		return nil, availableDrivers, scope, models.NewMediaUnavailableError(registry, modality)
	}

	if resolved := preferredMediaModel(ctx, userID, registry, selector, providers, modality); resolved != nil {
		return resolved, availableDrivers, scope, nil
	}

	resolved, err := registry.Resolve(selector, providers)
	if err != nil {
		if refusal := mediaPolicyRefusal(ctx, scope, availableDrivers, selector, true); refusal != nil {
			return nil, availableDrivers, scope, refusal
		}
		if _, baseErr := registry.Resolve(models.ModelSelector{
			Tags: []string{mediaBaseTag(modality)}, RequireOutputModality: modality,
		}, providers); baseErr != nil {
			return nil, availableDrivers, scope, models.NewMediaUnavailableError(registry, modality)
		}
		return nil, availableDrivers, scope, fmt.Errorf("no %s model available: %w", modality, err)
	}
	return resolved, availableDrivers, scope, nil
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
		logging.WarnContext(ctx, "Media tag preferences partially unreadable; affected tags use defaults", "userID", userID, "error", err)
	}
	return all[baseTag].PreferredModel(registry, selector, providers)
}
