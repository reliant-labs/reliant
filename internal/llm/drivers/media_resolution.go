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
// the modality, through the same resolution the Resolve*Generator functions
// use, in the scope of the run the call belongs to (WithRunScope). It returns
// a *models.MediaUnavailableError carrying the one shared message when not,
// the org policy's *orgpolicy.Refusal when the policy is why, or a
// *models.MediaPersonalFundingError when only the person's own accounts could
// and they have not chosen to spend them in the org.
func CheckMediaAvailable(ctx context.Context, userID string, modality models.Modality) error {
	if _, err := models.GetRegistry(); err != nil {
		return nil
	}
	_, _, _, err := resolveMedia(ctx, userID, models.ModelSelector{Tags: []string{mediaBaseTag(modality)}}, modality, false)
	return err
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
// CheckMediaAvailable returns; the org policy's *orgpolicy.Refusal when the
// policy is why; and *models.MediaPersonalFundingError when only the person's
// own accounts could serve and they have not chosen to spend them in the org.
func resolveMediaModel(ctx context.Context, userID string, selector models.ModelSelector, modality models.Modality) (*models.ResolvedModel, models.AvailableDrivers, Scope, error) {
	return resolveMedia(ctx, userID, selector, modality, true)
}

// resolveMedia is resolveMediaModel; counted is whether a policy refusal is
// the generation's one judged use (the generation itself, not load_tool's
// look ahead).
func resolveMedia(ctx context.Context, userID string, selector models.ModelSelector, modality models.Modality, counted bool) (*models.ResolvedModel, models.AvailableDrivers, Scope, error) {
	selector.RequireOutputModality = modality

	scope, funding := runScopeFor(ctx, userID)
	all := GetAvailableDrivers(ctx, scope)
	resolved, candidates, err := resolveMediaFrom(ctx, userID, scope, funding, all, selector, modality, counted)
	return resolved, candidates, scope, err
}

// resolveMediaFrom is resolveMedia over all, scope's candidates: the
// resolved model and the candidate set it was resolved from (whose default
// account serves it), or why none serves.
func resolveMediaFrom(ctx context.Context, userID string, scope Scope, funding RunFunding, all models.AvailableDrivers, selector models.ModelSelector, modality models.Modality, counted bool) (*models.ResolvedModel, models.AvailableDrivers, error) {
	selector.RequireOutputModality = modality
	registry, err := models.GetRegistry()
	if err != nil {
		return nil, all, err
	}

	// Who may pay, in the order the person chose (§4.4, §10): the org's
	// accounts first, and the person's own only with their say.
	tries, heldBack := mediaFundingTries(scope, funding, all)
	for _, candidates := range tries {
		if resolved, err := resolveMediaIn(ctx, userID, registry, candidates, selector, modality); err == nil {
			return resolved, candidates, nil
		}
	}
	resolved, err := resolveMediaIn(ctx, userID, registry, all, selector, modality)
	if heldBack && err == nil && resolved != nil {
		return nil, all, models.NewMediaPersonalFundingError(modality, funding.OrgOnly)
	}
	if refusal := mediaPolicyRefusal(ctx, scope, all, selector, counted); refusal != nil {
		return nil, all, refusal
	}
	if err == nil {
		err = models.NewMediaUnavailableError(registry.WithAvailability(all.Availability), modality)
	}
	return nil, all, err
}

// resolveMediaIn resolves selector over candidates: the person's preference
// for the modality's default tier first, then the registry.
func resolveMediaIn(ctx context.Context, userID string, registry *models.ModelRegistry, candidates models.AvailableDrivers, selector models.ModelSelector, modality models.Modality) (*models.ResolvedModel, error) {
	registry = registry.WithAvailability(candidates.Availability)
	providers := mediaProviders(candidates, modality)
	if len(providers) == 0 {
		return nil, models.NewMediaUnavailableError(registry, modality)
	}
	if resolved := preferredMediaModel(ctx, userID, registry, selector, providers, modality); resolved != nil {
		return resolved, nil
	}
	resolved, err := registry.Resolve(selector, providers)
	if err != nil {
		if _, baseErr := registry.Resolve(models.ModelSelector{
			Tags: []string{mediaBaseTag(modality)}, RequireOutputModality: modality,
		}, providers); baseErr != nil {
			return nil, models.NewMediaUnavailableError(registry, modality)
		}
		return nil, fmt.Errorf("no %s model available: %w", modality, err)
	}
	return resolved, nil
}

// mediaFundingTries is the candidate sets a generation in scope resolves
// over, in order, as funding allows (org-provider-accounts §4.4, §10): the
// first that serves is used. heldBack reports that the person's own accounts
// were left out for want of their say, so a generation only those could
// serve is refused with how to give it rather than "connect a provider".
//
// Work of no org, and a person who chose their own accounts first, draw on
// everything (their own first). An org that gives the person nothing
// (all.OrgGave, decided before any account was left out for its status or
// the org's policy, so an org key that is paused, rejected or refused still
// counts) leaves their own accounts serving as before. Otherwise the org's
// accounts serve first, and the person's own only under use_personal (or an
// automation stamped to spend any source); org_only never reaches them.
func mediaFundingTries(scope Scope, funding RunFunding, all models.AvailableDrivers) (tries []models.AvailableDrivers, heldBack bool) {
	if scope.Personal() {
		return []models.AvailableDrivers{all}, false
	}
	if funding.PersonalFirst && !funding.OrgOnly {
		return []models.AvailableDrivers{PersonalFirst(all)}, false
	}
	personal := false
	for _, list := range all.Accounts {
		for _, a := range list {
			personal = personal || a.Funding == models.FundingPersonal
		}
	}
	if !personal || (!all.OrgGave && !funding.OrgOnly) {
		return []models.AvailableDrivers{all}, false
	}
	orgFunded := withoutPersonalAccounts(all)
	if funding.PersonalWhenOrgCannot && !funding.OrgOnly {
		return []models.AvailableDrivers{orgFunded, all}, false
	}
	return []models.AvailableDrivers{orgFunded}, true
}

// withoutPersonalAccounts is avail without the person's own accounts: a
// driver left with none leaves the set, and one that keeps an org-funded
// account defaults to it.
func withoutPersonalAccounts(avail models.AvailableDrivers) models.AvailableDrivers {
	out := avail
	out.Accounts = make(map[models.DriverID][]models.AccountConfig, len(avail.Accounts))
	out.Drivers = make(map[models.DriverID]models.DriverConfig, len(avail.Drivers))
	for d, list := range avail.Accounts {
		kept := make([]models.AccountConfig, 0, len(list))
		for _, a := range list {
			if a.Funding != models.FundingPersonal {
				kept = append(kept, a)
			}
		}
		if len(kept) == 0 {
			continue
		}
		out.Accounts[d] = kept
		def := kept[0].DriverConfig
		if cfg, ok := avail.Drivers[d]; ok {
			for _, a := range kept {
				if a.AccountID == cfg.AccountID {
					def = cfg
				}
			}
		}
		out.Drivers[d] = def
	}
	return out
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
