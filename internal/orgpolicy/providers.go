// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"slices"
	"strings"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

// ── PROVIDERS AND MODELS AT RESOLUTION (§4.1 point 3, §4.2) ──────────
//
// Subscriptions, BYO API keys and local models spend the person's own money
// or run on their own hardware, so the gateway never sees them: only reliant
// can say which accounts may serve an org's runs. PointProviders is where
// reliant resolves a run's model and account (launch, the registry, the
// account router, failover, the driver build), and it applies three sections:
//
//   - providers: which drivers (allowed) and which kinds of account, by who
//     pays (allowed_sources), may serve any run of the org;
//   - automations: the same source list again, for a run nobody is present
//     for (org-provider-accounts.md §11.6);
//   - models: which managed models the Reliant gateway may serve. The gateway
//     enforces it itself (G1, on its own switch); applied here as well, the
//     person is never offered, routed onto or failed over to a managed model
//     the gateway would refuse. It governs only the managed driver: its ids
//     are the gateway's api_model spellings.

// PointProviders is the providers point: the providers, automations and
// models sections where a run's model and account are resolved.
const PointProviders Point = "providers"

// ManagedDriverID is the driver of the Reliant gateway, the one the models
// section governs.
const ManagedDriverID = "reliant"

// ProviderAdmission is what doc says about serving a call of one of the org's
// runs from driver with an account of source. providers.allowed (empty admits
// every driver) and providers.allowed_sources (empty admits every source)
// apply to every run; automations.allowed_sources (empty adds nothing) also
// applies to a run nobody is present for. No such section governs nothing.
//
// The answer's Mode is the mode of the section that refuses; when both do,
// ENFORCE if either enforces, so an audited automations rule never relaxes
// an enforced providers one.
func ProviderAdmission(doc *controlplanev1.OrgPolicy, driver string, source controlplanev1.ProviderSource, unattended bool) Admission {
	providers, automations := doc.GetProviders(), doc.GetAutomations()
	if !unattended {
		automations = nil
	}
	if providers == nil && automations == nil {
		return Admission{}
	}
	adm := Admission{Governed: true, Admitted: true}
	refuse := func(mode controlplanev1.OrgPolicyMode) {
		if adm.Admitted || mode == controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE {
			adm.Mode = mode
		}
		adm.Admitted = false
	}
	if providers != nil && !providersAdmit(providers, strings.TrimSpace(driver), source) {
		refuse(providers.GetMode())
	}
	if automations != nil && len(automations.GetAllowedSources()) > 0 && !slices.Contains(automations.GetAllowedSources(), source) {
		refuse(automations.GetMode())
	}
	return adm
}

// providersAdmit is the providers section's own answer.
func providersAdmit(sec *controlplanev1.ProvidersPolicy, driver string, source controlplanev1.ProviderSource) bool {
	return (len(sec.GetAllowed()) == 0 || slices.ContainsFunc(sec.GetAllowed(), func(id string) bool { return strings.TrimSpace(id) == driver })) &&
		(len(sec.GetAllowedSources()) == 0 || slices.Contains(sec.GetAllowedSources(), source))
}

// ModelAdmission is what doc's models section says about the managed model
// apiModel (the gateway's api_model spelling). No section governs nothing;
// allow_all admits every model; otherwise only the listed ones.
func ModelAdmission(doc *controlplanev1.OrgPolicy, apiModel string) Admission {
	sec := doc.GetModels()
	if sec == nil {
		return Admission{}
	}
	apiModel = strings.TrimSpace(apiModel)
	return Admission{
		Governed: true,
		Admitted: sec.GetAllowAll() || (apiModel != "" && slices.ContainsFunc(sec.GetAllowed(), func(id string) bool { return strings.TrimSpace(id) == apiModel })),
		Mode:     sec.GetMode(),
	}
}

// SourceName is source as a decision's subject and a log names it:
// "org_accounts", "personal", ...
func SourceName(source controlplanev1.ProviderSource) string {
	return strings.ToLower(strings.TrimPrefix(source.String(), "PROVIDER_SOURCE_"))
}

// JudgeProvider decides serving a call of orgID's runs from driver with an
// account of source (Judge, at PointProviders). The decision's Subject is
// "<driver>/<source>".
func JudgeProvider(ctx context.Context, orgID, driver string, source controlplanev1.ProviderSource, unattended, quiet bool) (Decision, error) {
	return Judge(ctx, PointProviders, orgID, driver+"/"+SourceName(source), ReasonProviderNotAllowed, quiet,
		func(doc *controlplanev1.OrgPolicy) Admission {
			return ProviderAdmission(doc, driver, source, unattended)
		})
}

// JudgeModel decides serving a call of orgID's runs with the managed model
// apiModel (Judge, at PointProviders). The decision's Subject is the model.
func JudgeModel(ctx context.Context, orgID, apiModel string, quiet bool) (Decision, error) {
	return Judge(ctx, PointProviders, orgID, apiModel, ReasonModelNotAllowed, quiet,
		func(doc *controlplanev1.OrgPolicy) Admission { return ModelAdmission(doc, apiModel) })
}
