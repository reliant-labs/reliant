// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"slices"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// This file applies a run's org policy to what its resolution may draw on
// (control-plane docs/design/org-policies.md §4.1 point 3, §4.2;
// org-provider-accounts.md §9, §11.6: org-policies R2): the providers section
// (drivers, and kinds of account by who pays), the automations section for a
// run nobody is present for, and the models section for the managed driver.
//
// It sits on the one loader's seam (buildAvailableDriversFor), so every reader
// of a scope's candidates sees the same narrowed set: launch validation, the
// registry's choice of model and driver, the account router and its
// alternatives, failover's fallback model, and the default account. A rate
// limit can therefore never fail a turn over onto a forbidden provider: the
// router never sees one. CheckOrgPolicy re-checks the account a call is
// finally built on, which also covers what never passes through the loader (a
// local model on the person's machine).
//
// Only an ENFORCED refusal narrows anything: the platform switch
// (orgpolicy.PointProviders) and the section's own mode both enforce. Under
// audit, the default, the set is untouched and the driver build counts each
// call a policy would have refused. A policy that cannot be read while the
// point enforces fails the read retryably: never an unconstrained run.

// ProviderSource is who pays for a call served from driver with an account of
// funding (models.Funding*), as the org policy names it. A local model is
// LOCAL; an account of no known class is PERSONAL, the class every restrictive
// rule refuses first.
func ProviderSource(driver models.DriverID, funding string) controlplanev1.ProviderSource {
	switch {
	case driver == models.DriverID(local.Family) || funding == models.FundingLocal:
		return controlplanev1.ProviderSource_PROVIDER_SOURCE_LOCAL
	case funding == models.FundingOrgKey:
		return controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS
	case funding == models.FundingOrgSeat:
		return controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_SEATS
	case funding == models.FundingOrgWallet:
		return controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT
	}
	return controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL
}

// policyModelDisabledReason is what the picker shows for a managed model the
// org's policy does not allow.
const policyModelDisabledReason = "Your organization's policy does not allow this model"

// orgPolicyFilter is a scope's org policy as resolution applies it. A nil
// filter narrows nothing.
type orgPolicyFilter struct {
	doc        *controlplanev1.OrgPolicy
	unattended bool
}

// unattendedScope reports whether scope's run has nobody present for it, so
// the org's automations section applies too (§11.6).
func unattendedScope(scope Scope) bool { return scope.OrgID != "" && !scope.Attended }

// orgPolicyFilterFor is scope's filter: nil for work of no org, a point that
// does not enforce (audit refuses nothing, off reads nothing), and a policy
// with no rule here. The error is a policy that cannot be read while the
// point enforces: the caller's read fails, retryably.
func orgPolicyFilterFor(ctx context.Context, scope Scope) (*orgPolicyFilter, error) {
	if scope.OrgID == "" || orgpolicy.EnforcementOf(orgpolicy.PointProviders) != orgpolicy.EnforcementEnforce {
		return nil, nil
	}
	pol, err := orgpolicy.For(ctx, scope.OrgID)
	if err != nil {
		return nil, fmt.Errorf("the organization's provider policy: %w", err)
	}
	doc := pol.Doc
	unattended := unattendedScope(scope)
	if doc.GetProviders() == nil && doc.GetModels() == nil && (doc.GetAutomations() == nil || !unattended) {
		return nil, nil
	}
	return &orgPolicyFilter{doc: doc, unattended: unattended}, nil
}

// automationsDecide reports whether the org's automations section decides an
// unattended run's sources by itself: it names sources and it is enforced.
// Then it is total (org-provider-accounts.md §11.3: "an explicit policy is
// total"): it replaces the automation's unattended-funding stamp, wider or
// narrower, and accounts() admits exactly what it (and the providers
// section) allows. Under audit it decides nothing, and the stamp stands.
func (f *orgPolicyFilter) automationsDecide() bool {
	return f != nil && f.unattended && orgpolicy.AutomationsDecide(f.doc)
}

// liftStamp is scope with the automation's stamp lifted when the automations
// section decides its sources (automationsDecide): the loader then offers
// every source, and accounts() keeps the ones the section admits. Any other
// scope is returned unchanged.
func (f *orgPolicyFilter) liftStamp(scope Scope) Scope {
	if f.automationsDecide() {
		scope.UnattendedAnySource = true
	}
	return scope
}

// refusesAccount reports whether the policy refuses a call from driver with
// an account of funding.
func (f *orgPolicyFilter) refusesAccount(driver models.DriverID, funding string) bool {
	adm := orgpolicy.ProviderAdmission(f.doc, string(driver), ProviderSource(driver, funding), f.unattended)
	return adm.Governed && !adm.Admitted && orgpolicy.Enforced(orgpolicy.PointProviders, adm.Mode)
}

// narrowsModels reports whether the models section refuses some managed model.
func (f *orgPolicyFilter) narrowsModels() bool {
	sec := f.doc.GetModels()
	return sec != nil && !sec.GetAllowAll() && orgpolicy.Enforced(orgpolicy.PointProviders, sec.GetMode())
}

// refusesModel reports whether the policy refuses modelID on driver: only the
// managed driver's models are governed.
func (f *orgPolicyFilter) refusesModel(driver, modelID string) bool {
	if driver != orgpolicy.ManagedDriverID || !f.narrowsModels() {
		return false
	}
	return !orgpolicy.ModelAdmission(f.doc, ManagedAPIModel(modelID)).Admitted
}

// accounts is in without the accounts the policy refuses, and what it took
// (models.PolicyRefusal): a driver whose every account is refused leaves the
// set, with provider_not_allowed and the funding of one of its accounts; the
// managed driver whose models are narrowed stays, with model_not_allowed. A
// driver that keeps an account the policy allows (an org key beside a
// refused personal key) is not recorded: it still serves, so the policy is
// not why a resolution on it fails.
func (f *orgPolicyFilter) accounts(in map[models.DriverID][]models.AccountConfig) (map[models.DriverID][]models.AccountConfig, map[models.DriverID]models.PolicyRefusal) {
	if f == nil {
		return in, nil
	}
	out := make(map[models.DriverID][]models.AccountConfig, len(in))
	refused := map[models.DriverID]models.PolicyRefusal{}
	for driver, list := range in {
		kept := make([]models.AccountConfig, 0, len(list))
		var first *models.AccountConfig
		for i, acct := range list {
			if f.refusesAccount(driver, acct.Funding) {
				if first == nil {
					first = &list[i]
				}
				continue
			}
			kept = append(kept, acct)
		}
		switch {
		case len(kept) > 0:
			out[driver] = kept
		case first != nil:
			refused[driver] = models.PolicyRefusal{Reason: orgpolicy.ReasonProviderNotAllowed, Funding: first.Funding}
		}
	}
	if kept := out[models.ManagedDriverID]; len(kept) > 0 && f.narrowsModels() {
		refused[models.ManagedDriverID] = models.PolicyRefusal{Reason: orgpolicy.ReasonModelNotAllowed, Funding: kept[0].Funding}
	}
	if len(refused) == 0 {
		refused = nil
	}
	return out, refused
}

// availability is base with the managed models the policy refuses disabled,
// so neither the registry nor the picker nor failover offers them.
func (f *orgPolicyFilter) availability(base models.AvailabilityFunc) models.AvailabilityFunc {
	if f == nil || !f.narrowsModels() {
		return base
	}
	return func(driver, modelID string) models.ModelAvailability {
		if f.refusesModel(driver, modelID) {
			return models.ModelAvailability{Disabled: true, Reason: policyModelDisabledReason}
		}
		if base == nil {
			return models.ModelAvailability{}
		}
		return base(driver, modelID)
	}
}

// ManagedAPIModel is the gateway's spelling of modelID (the managed driver's
// api_model), "" when the managed driver does not serve it.
func ManagedAPIModel(modelID string) string {
	reg, err := models.GetRegistry()
	if err != nil {
		return ""
	}
	base, _ := ParseModelIDWithDriver(modelID)
	def, ok := reg.GetDefinition(base)
	if !ok {
		return ""
	}
	for _, p := range def.Providers {
		if p.Driver == orgpolicy.ManagedDriverID {
			return p.APIModel
		}
	}
	return ""
}

// CheckOrgPolicy judges a call of scope's run served from driver with an
// account of funding, on modelID (a registry id, "<id>@<driver>" accepted; the
// models section reads it only for the managed driver): the *orgpolicy.Refusal
// when the org's policy refuses it (its Unattended set from scope), the
// policy's error when it cannot be read while the point enforces, nil
// otherwise, and always nil for work of no org.
//
// counted is whether this is the call's one judged use: the driver build
// passes true, so audit counts once per call; a look ahead of the call
// (launch validation, a pick) passes false.
func CheckOrgPolicy(ctx context.Context, scope Scope, driver models.DriverID, funding, modelID string, counted bool) error {
	if scope.OrgID == "" {
		return nil
	}
	unattended := unattendedScope(scope)
	d, err := orgpolicy.JudgeProvider(ctx, scope.OrgID, string(driver), ProviderSource(driver, funding), unattended, !counted)
	if err != nil {
		return err
	}
	if !d.Refused && driver == models.ManagedDriverID {
		if d, err = orgpolicy.JudgeModel(ctx, scope.OrgID, ManagedAPIModel(modelID), !counted); err != nil {
			return err
		}
	}
	if d.Refused {
		return &orgpolicy.Refusal{Reason: d.Reason, Unattended: unattended}
	}
	return nil
}

// OrgPolicyRefusal explains a selector that does not resolve over available,
// scope's candidates: the org policy's *orgpolicy.Refusal when what the policy
// took out of the set would have served it, nil when the policy is not why
// (it took nothing, or nothing it took serves the selector either). A
// resolution that fails reports this instead of "connect a provider", so a
// chat begun before the policy pauses at its next call with the reason.
//
// The refused driver is re-judged through CheckOrgPolicy (counted as given),
// so the refusal names the exact policy version and a policy loosened since
// the read explains nothing.
func OrgPolicyRefusal(ctx context.Context, scope Scope, available models.AvailableDrivers, selector models.ModelSelector, counted bool) *orgpolicy.Refusal {
	if len(available.PolicyRefused) == 0 || (selector.ID == "" && len(selector.Tags) == 0) {
		return nil
	}
	refused := make([]models.DriverID, 0, len(available.PolicyRefused))
	for driver := range available.PolicyRefused {
		refused = append(refused, driver)
	}
	slices.Sort(refused)
	reg := models.MustGetRegistry()
	for _, driver := range refused {
		resolved, err := reg.Resolve(selector, []string{string(driver)})
		if err != nil || resolved.Provider.Driver != string(driver) {
			continue
		}
		err = CheckOrgPolicy(ctx, scope, driver, available.PolicyRefused[driver].Funding, resolved.Definition.ID, counted)
		if refusal, ok := orgpolicy.RefusalOf(err); ok {
			return refusal
		}
	}
	return nil
}

// OrgPolicyRefuses reports, without counting, whether scope's org policy
// refuses every call from driver with an account of funding: for a choice
// ahead of the call (a pinned local preference) that should give way to one
// the policy allows. Only an enforced refusal is one; a policy that cannot be
// read while the point enforces refuses.
func OrgPolicyRefuses(ctx context.Context, scope Scope, driver models.DriverID, funding string) bool {
	f, err := orgPolicyFilterFor(ctx, scope)
	if err != nil {
		return true
	}
	return f != nil && f.refusesAccount(driver, funding)
}
