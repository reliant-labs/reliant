// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The org's provider policy at the one loader's seam (org_policy.go;
// control-plane docs/design/org-policies.md §4.1, §4.2): every reader of a
// scope's candidates sees the same narrowed set, only an enforced refusal
// narrows anything, and audit leaves every resolution as it was.

const (
	policyOrg    = "org-policy"
	policyUser   = "policy-user"
	policyModel  = "claude-4.6-sonnet" // claude, anthropic and the gateway (reliant=claude-sonnet-4-6) serve it
	policyOpus   = "claude-4.6-opus"   // reliant=claude-opus-4-6
	policyAPIKey = "rlat_abcdef0123456789abcdef0123456789"
)

// policyDocs answers each org's policy from a table, at version 4; an org
// absent from it has none. down fails every call, as an outage does.
type policyDocs struct {
	docs map[string]*controlplanev1.OrgPolicy
	down bool
}

func (p policyDocs) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if p.down {
		return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
	}
	if doc, ok := p.docs[orgID]; ok {
		return accesstokenclient.OrgPolicy{Version: 4, Policy: doc}, nil
	}
	return accesstokenclient.OrgPolicy{}, nil
}

func withProvidersSwitch(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })
}

func policyCtx(doc *controlplanev1.OrgPolicy) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(policyDocs{docs: map[string]*controlplanev1.OrgPolicy{policyOrg: doc}}))
}

func onlyDrivers(mode controlplanev1.OrgPolicyMode, allowed ...string) *controlplanev1.OrgPolicy {
	return &controlplanev1.OrgPolicy{Providers: &controlplanev1.ProvidersPolicy{Mode: mode, Allowed: allowed}}
}

const (
	enforced = controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	audited  = controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT
)

// seedPolicyUser gives the person a Claude subscription, an Anthropic API key
// and the managed Reliant key, all their own.
func seedPolicyUser(t *testing.T) *db.Repo {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	InitializeAPIKeyProvider(repo)
	previous := AccountAvailabilityClient
	AccountAvailabilityClient = noReportClients
	t.Cleanup(func() { AccountAvailabilityClient = previous })
	addClaude(t, repo, policyUser, policyUser+"-claude")
	db.SeedProviderAccount(t, repo, policyUser, "anthropic", "sk-ant-api03-"+policyUser)
	db.SeedProviderAccount(t, repo, policyUser, "reliant", policyAPIKey)
	return repo
}

func driverIDs(avail models.AvailableDrivers) []string {
	var ids []string
	for d := range avail.Accounts {
		ids = append(ids, string(d))
	}
	sort.Strings(ids)
	return ids
}

func orgScope(attended bool) Scope {
	return Scope{UserID: policyUser, OrgID: policyOrg, Attended: attended, UnattendedAnySource: !attended}
}

func TestOrgPolicy_NarrowsTheCandidatesAtTheSeam(t *testing.T) {
	repo := seedPolicyUser(t)
	all := []string{"anthropic", "claude", "reliant"}
	org := func(sources ...controlplanev1.ProviderSource) *controlplanev1.OrgPolicy {
		return &controlplanev1.OrgPolicy{Providers: &controlplanev1.ProvidersPolicy{Mode: enforced, AllowedSources: sources}}
	}
	for name, tc := range map[string]struct {
		sw      orgpolicy.Enforcement
		doc     *controlplanev1.OrgPolicy
		scope   Scope
		want    []string
		refused map[models.DriverID]string
	}{
		"enforced drivers list": {orgpolicy.EnforcementEnforce, onlyDrivers(enforced, "anthropic"), orgScope(true), []string{"anthropic"},
			map[models.DriverID]string{"claude": orgpolicy.ReasonProviderNotAllowed, "reliant": orgpolicy.ReasonProviderNotAllowed}},
		"enforced sources: only the org's Reliant credits": {orgpolicy.EnforcementEnforce,
			org(controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT, controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS), orgScope(true), []string{"reliant"},
			map[models.DriverID]string{"anthropic": orgpolicy.ReasonProviderNotAllowed, "claude": orgpolicy.ReasonProviderNotAllowed}},
		"the section audits: nothing narrowed": {orgpolicy.EnforcementEnforce, onlyDrivers(audited, "anthropic"), orgScope(true), all, nil},
		"the point audits: nothing narrowed":   {orgpolicy.EnforcementAudit, onlyDrivers(enforced, "anthropic"), orgScope(true), all, nil},
		"the point is off: nothing narrowed":   {orgpolicy.EnforcementOff, onlyDrivers(enforced, "anthropic"), orgScope(true), all, nil},
		"work of no org: nothing narrowed":     {orgpolicy.EnforcementEnforce, onlyDrivers(enforced, "anthropic"), PersonalScope(policyUser), all, nil},
		"automations narrow an unattended run": {orgpolicy.EnforcementEnforce, &controlplanev1.OrgPolicy{Automations: &controlplanev1.AutomationsPolicy{
			Mode: enforced, AllowedSources: []controlplanev1.ProviderSource{controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT},
		}}, orgScope(false), []string{"reliant"},
			map[models.DriverID]string{"anthropic": orgpolicy.ReasonProviderNotAllowed, "claude": orgpolicy.ReasonProviderNotAllowed}},
		"automations leave an attended run alone": {orgpolicy.EnforcementEnforce, &controlplanev1.OrgPolicy{Automations: &controlplanev1.AutomationsPolicy{
			Mode: enforced, AllowedSources: []controlplanev1.ProviderSource{controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT},
		}}, orgScope(true), all, nil},
	} {
		t.Run(name, func(t *testing.T) {
			withProvidersSwitch(t, tc.sw)
			ctx := policyCtx(tc.doc)
			avail, err := buildAvailableDrivers(ctx, repo, tc.scope, noReportClients)
			require.NoError(t, err)
			assert.Equal(t, tc.want, driverIDs(avail))
			for d := range avail.Drivers {
				assert.Contains(t, tc.want, string(d), "a refused driver has no default account")
			}
			got := map[models.DriverID]string{}
			for d, r := range avail.PolicyRefused {
				got[d] = r.Reason
			}
			if tc.refused == nil {
				assert.Empty(t, avail.PolicyRefused)
			} else {
				assert.Equal(t, tc.refused, got)
			}
		})
	}
}

// The models section narrows only the managed driver, by the gateway's
// api_model spelling: the model stays servable from the person's own
// providers, and the gateway's other models stay offered.
func TestOrgPolicy_ModelsNarrowTheManagedDriver(t *testing.T) {
	repo := seedPolicyUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	ctx := policyCtx(&controlplanev1.OrgPolicy{Models: &controlplanev1.ModelsPolicy{Mode: enforced, Allowed: []string{"claude-opus-4-6"}}})

	avail, err := buildAvailableDrivers(ctx, repo, orgScope(true), noReportClients)
	require.NoError(t, err)
	require.NotNil(t, avail.Availability)
	assert.True(t, avail.Availability("reliant", policyModel).Disabled, "the gateway may not serve a model the org does not allow")
	assert.False(t, avail.Availability("reliant", policyOpus).Disabled, "an allowed model stays offered")
	assert.False(t, avail.Availability("anthropic", policyModel).Disabled, "the models section governs the managed driver only")
	assert.Equal(t, orgpolicy.ReasonModelNotAllowed, avail.PolicyRefused["reliant"].Reason)

	reg := models.MustGetRegistry().WithAvailability(avail.Availability)
	_, err = reg.Resolve(models.ModelSelector{ID: policyModel + "@reliant"}, []string{"reliant"})
	assert.Error(t, err, "the registry does not resolve a refused managed model")
	resolved, err := reg.Resolve(models.ModelSelector{ID: policyOpus}, []string{"reliant"})
	require.NoError(t, err)
	assert.Equal(t, "reliant", resolved.Provider.Driver)
}

// Every reader of the seam sees the narrowed set: the lookups, launch's
// selector validation, the picker's list and the default-account resolver.
func TestOrgPolicy_EveryReaderOfTheSeamIsFiltered(t *testing.T) {
	seedPolicyUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	ctx := policyCtx(onlyDrivers(enforced, "anthropic"))
	scope := orgScope(true)

	assert.Equal(t, []string{"anthropic"}, driverIDs(GetAvailableDrivers(ctx, scope)), "GetAvailableDrivers")
	looked, err := LookupAvailableDrivers(ctx, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"anthropic"}, driverIDs(looked), "LookupAvailableDrivers")

	infos, err := ListAvailableModels(ctx, scope)
	require.NoError(t, err)
	for _, mi := range infos {
		assert.Equal(t, "anthropic", mi.DriverID, "ListAvailableModels offers %s on a refused provider", mi.ID)
	}

	// Launch validation: a model only a refused provider is asked for is
	// refused with the policy's reason; one an allowed provider serves passes.
	err = ValidateModelSelector(ctx, scope, map[string]interface{}{"id": policyModel + "@claude"})
	refusal, ok := orgpolicy.RefusalOf(err)
	require.True(t, ok, "want a policy refusal, got %v", err)
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refusal.Reason)
	assert.False(t, refusal.Unattended)
	assert.NoError(t, ValidateModelSelector(ctx, scope, map[string]interface{}{"id": policyModel}))
	assert.NoError(t, ValidateModelSelector(ctx, PersonalScope(policyUser), map[string]interface{}{"id": policyModel + "@claude"}),
		"the person's own work of no org is not the org's to govern")

	// A local model is a source of its own.
	localOnly := policyCtx(&controlplanev1.OrgPolicy{Providers: &controlplanev1.ProvidersPolicy{Mode: enforced,
		AllowedSources: []controlplanev1.ProviderSource{controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL}}})
	err = ValidateModelSelector(localOnly, scope, map[string]interface{}{"id": "llama3@local"})
	_, ok = orgpolicy.RefusalOf(err)
	assert.True(t, ok, "a local model the org does not allow is refused at launch: %v", err)

	// The default-account resolver: a pin on a refused provider finds it
	// not connected, and never builds it.
	_, err = GetDriver(ctx, scope, models.Preferences{{ModelID: models.ModelID(policyModel + "@claude")}})
	assert.Error(t, err)
}

// A run begun before the policy: what served it is no longer a candidate,
// and OrgPolicyRefusal says the policy is why, with its reason, for the next
// call to pause on. Nothing the policy took serves an unrelated selector.
func TestOrgPolicy_RefusalExplainsAResolutionThePolicyBroke(t *testing.T) {
	repo := seedPolicyUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	ctx := policyCtx(onlyDrivers(enforced, "openai"))

	before, err := buildAvailableDrivers(context.Background(), repo, orgScope(true), noReportClients)
	require.NoError(t, err)
	assert.Contains(t, driverIDs(before), "claude", "before the policy the chat's provider serves")

	after, err := buildAvailableDrivers(ctx, repo, orgScope(false), noReportClients)
	require.NoError(t, err)
	assert.Empty(t, after.Accounts, "after it, nothing the person has is allowed")
	refusal := OrgPolicyRefusal(ctx, orgScope(false), after, models.ModelSelector{ID: policyModel}, true)
	require.NotNil(t, refusal)
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refusal.Reason)
	assert.True(t, refusal.Unattended, "an unattended run's refusal says so")
	assert.Nil(t, OrgPolicyRefusal(ctx, orgScope(true), after, models.ModelSelector{ID: "gpt-6-terra"}, true),
		"a model only openai serves was not taken by the policy")
}

// The driver build re-checks the account it is handed: a refusal under
// enforce, nothing under audit (the call proceeds and is counted).
func TestCheckOrgPolicy(t *testing.T) {
	ctx := policyCtx(&controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: enforced, Allowed: []string{"anthropic", "reliant"}},
		Models:    &controlplanev1.ModelsPolicy{Mode: enforced, Allowed: []string{"claude-opus-4-6"}},
	})
	scope := orgScope(true)

	withProvidersSwitch(t, orgpolicy.EnforcementAudit)
	assert.NoError(t, CheckOrgPolicy(ctx, scope, "claude", models.FundingPersonal, policyModel, true), "audit refuses nothing")

	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementEnforce)
	for name, tc := range map[string]struct {
		driver  models.DriverID
		funding string
		model   string
		reason  string
	}{
		"a refused provider":             {"claude", models.FundingPersonal, policyModel, orgpolicy.ReasonProviderNotAllowed},
		"a local model":                  {"local", models.FundingLocal, "", orgpolicy.ReasonProviderNotAllowed},
		"a refused managed model":        {"reliant", models.FundingOrgWallet, policyModel + "@reliant", orgpolicy.ReasonModelNotAllowed},
		"an allowed managed model":       {"reliant", models.FundingOrgWallet, policyOpus, ""},
		"an allowed provider, any model": {"anthropic", models.FundingPersonal, policyModel, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckOrgPolicy(ctx, scope, tc.driver, tc.funding, tc.model, false)
			if tc.reason == "" {
				assert.NoError(t, err)
				return
			}
			refusal, ok := orgpolicy.RefusalOf(err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.reason, refusal.Reason)
		})
	}
	assert.NoError(t, CheckOrgPolicy(ctx, PersonalScope(policyUser), "claude", models.FundingPersonal, policyModel, true), "work of no org")
}

// A policy that cannot be read while the point enforces fails the read,
// retryably: never an unconstrained run. Under audit the run goes on.
func TestOrgPolicy_UnreadablePolicyFailsClosedOnlyWhereItEnforces(t *testing.T) {
	repo := seedPolicyUser(t)
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(policyDocs{down: true}))

	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	_, err := buildAvailableDrivers(ctx, repo, orgScope(true), noReportClients)
	require.Error(t, err)
	assert.True(t, orgpolicy.IsUnavailable(err), "%v", err)
	_, err = LookupAvailableDrivers(ctx, orgScope(true))
	assert.ErrorIs(t, err, ErrDriverLookupFailed, "a failed read, not an empty set")
	assert.True(t, OrgPolicyRefuses(ctx, orgScope(true), "local", models.FundingLocal), "a choice ahead of the call gives way")

	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementAudit)
	avail, err := buildAvailableDrivers(ctx, repo, orgScope(true), noReportClients)
	require.NoError(t, err)
	assert.Equal(t, []string{"anthropic", "claude", "reliant"}, driverIDs(avail))
}

func TestProviderSource(t *testing.T) {
	for _, tc := range []struct {
		driver  models.DriverID
		funding string
		want    controlplanev1.ProviderSource
	}{
		{"anthropic", models.FundingOrgKey, controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS},
		{"claude", models.FundingOrgSeat, controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_SEATS},
		{"reliant", models.FundingOrgWallet, controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT},
		{"claude", models.FundingPersonal, controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL},
		{"local", "", controlplanev1.ProviderSource_PROVIDER_SOURCE_LOCAL},
		{"openai", "", controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL},
	} {
		assert.Equal(t, tc.want, ProviderSource(tc.driver, tc.funding), "%s/%s", tc.driver, tc.funding)
	}
}
