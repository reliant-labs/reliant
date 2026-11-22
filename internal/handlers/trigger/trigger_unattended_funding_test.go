// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The stamp a trigger write decides (org-provider-accounts §11.3, I4): a new
// trigger in a team org is org_only, in a solo org any, any when the count is
// unknown (an old control plane); the owner can always tighten; loosening is
// refused in a team org, and elsewhere needs a person; an update that does
// not name it keeps it.
func TestUnattendedFundingFor(t *testing.T) {
	const (
		unspecified = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED
		orgOnly     = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY
		anySource   = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY
	)
	org := "org-acme"
	stamped := func(f core.UnattendedFunding) *core.Trigger { return &core.Trigger{UnattendedFunding: f} }
	for _, tc := range []struct {
		name     string
		orgID    *string
		members  int
		known    bool
		countErr error
		chosen   reliantv1.UnattendedFunding
		existing *core.Trigger
		want     core.UnattendedFunding
		refused  bool
		// person signs the write in to the app; otherwise it is an access
		// token, what an agent shell or a script holds.
		person bool
		// needsPerson is a widening refused only because no person made it.
		needsPerson bool
	}{
		{name: "a new trigger in a two-member org is org_only", orgID: &org, members: 2, known: true, chosen: unspecified, want: core.UnattendedFundingOrgOnly},
		{name: "a new trigger in a solo org is any", orgID: &org, members: 1, known: true, chosen: unspecified, want: core.UnattendedFundingAny},
		{name: "a new trigger with an old control plane is any", orgID: &org, chosen: unspecified, want: core.UnattendedFundingAny},
		{name: "a trigger with no org has no stamp", chosen: orgOnly, want: ""},
		{name: "an update that names none keeps org_only", orgID: &org, members: 1, known: true, chosen: unspecified,
			existing: stamped(core.UnattendedFundingOrgOnly), want: core.UnattendedFundingOrgOnly},
		{name: "an update that names none keeps an unstamped trigger unstamped", orgID: &org, members: 5, known: true, chosen: unspecified,
			existing: stamped(""), want: ""},
		{name: "the owner can always tighten", orgID: &org, members: 5, known: true, chosen: orgOnly,
			existing: stamped(core.UnattendedFundingAny), want: core.UnattendedFundingOrgOnly},
		{name: "loosening in a team org is refused", orgID: &org, members: 2, known: true, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), refused: true},
		{name: "loosening with an unknown count is refused", orgID: &org, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), refused: true},
		{name: "loosening in a solo org by a person is allowed", orgID: &org, members: 1, known: true, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), person: true, want: core.UnattendedFundingAny},
		{name: "loosening in a solo org by an agent needs a person", orgID: &org, members: 1, known: true, chosen: anySource,
			existing: stamped(core.UnattendedFundingOrgOnly), needsPerson: true},
		{name: "a new trigger asking for any in a team org is refused", orgID: &org, members: 3, known: true, chosen: anySource, refused: true},
		{name: "a new trigger asking for any in a solo org is its default, no person needed", orgID: &org, members: 1, known: true,
			chosen: anySource, want: core.UnattendedFundingAny},
		{name: "an agent can always tighten", orgID: &org, members: 1, known: true, chosen: orgOnly,
			existing: stamped(core.UnattendedFundingAny), want: core.UnattendedFundingOrgOnly},
		{name: "naming any on a trigger already any is no loosening", orgID: &org, members: 3, known: true, chosen: anySource,
			existing: stamped(""), want: core.UnattendedFundingAny},
		{name: "a count that cannot be read is retried, never stamped any", orgID: &org, countErr: errors.New("down"), chosen: unspecified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{deps: Deps{Orgs: &MockOrgResolver{
				LiveMembersFreshFunc: func(_ context.Context, userID, orgID string) (int, bool, error) {
					assert.Equal(t, "owner", userID)
					assert.Equal(t, org, orgID)
					return tc.members, tc.known, tc.countErr
				},
			}}}
			ctx := auth.WithCredential(context.Background(), auth.CredentialAccessToken)
			if tc.person {
				ctx = auth.WithCredential(context.Background(), auth.CredentialSession)
			}
			got, err := s.unattendedFundingFor(ctx, "owner", tc.orgID, tc.chosen, tc.existing)
			switch {
			case tc.refused:
				require.Error(t, err)
				assert.Equal(t, ReasonOrgOnlyAutomation, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			case tc.needsPerson:
				require.Error(t, err)
				assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
				assert.Equal(t, auth.ReasonPersonRequired, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
				assert.Empty(t, got)
			case tc.countErr != nil:
				require.Error(t, err)
				assert.Empty(t, got)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// policyDoc answers one policy for every org.
type policyDoc struct{ doc *controlplanev1.OrgPolicy }

func (p policyDoc) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 2, Policy: p.doc}, nil
}

// §11.3: loosening an automation in a team org is allowed only when the org's
// policy decides its automations' sources and admits members' own accounts:
// the automations section names PERSONAL, both the platform switch
// (PointProviders) and the section's mode enforce, and the providers section,
// if it names sources, names PERSONAL too. Audit, either way, keeps the stamp.
// Even then only a person may loosen, at create or update.
func TestUnattendedFundingFor_PolicyMayAllowLoosening(t *testing.T) {
	org := "org-acme"
	personal := controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL
	orgKeys := controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS
	enforce := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	audit := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT
	s := &Service{deps: Deps{Orgs: &MockOrgResolver{
		LiveMembersFreshFunc: func(context.Context, string, string) (int, bool, error) { return 4, true, nil },
	}}}
	existing := &core.Trigger{UnattendedFunding: core.UnattendedFundingOrgOnly}
	setPlatform := func(t *testing.T, e orgpolicy.Enforcement) {
		t.Helper()
		prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
		orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, e)
		t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })
	}
	person := auth.WithCredential(context.Background(), auth.CredentialSession)
	loosenAs := func(as context.Context, doc *controlplanev1.OrgPolicy, existing *core.Trigger) (core.UnattendedFunding, error) {
		ctx := orgpolicy.WithResolver(as, orgpolicy.New(policyDoc{doc}))
		return s.unattendedFundingFor(ctx, "owner", &org, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY, existing)
	}
	loosen := func(doc *controlplanev1.OrgPolicy) (core.UnattendedFunding, error) {
		return loosenAs(person, doc, existing)
	}
	automations := func(mode controlplanev1.OrgPolicyMode, sources ...controlplanev1.ProviderSource) *controlplanev1.OrgPolicy {
		return &controlplanev1.OrgPolicy{Automations: &controlplanev1.AutomationsPolicy{Mode: mode, AllowedSources: sources}}
	}

	t.Run("both enforce: allowed", func(t *testing.T) {
		setPlatform(t, orgpolicy.EnforcementEnforce)
		got, err := loosen(automations(enforce, orgKeys, personal))
		require.NoError(t, err)
		assert.Equal(t, core.UnattendedFundingAny, got)
		got, err = loosenAs(person, automations(enforce, orgKeys, personal), nil)
		require.NoError(t, err, "a person creating one that asks for any")
		assert.Equal(t, core.UnattendedFundingAny, got)
	})

	for name, cred := range map[string]auth.Credential{
		"an access token (the CLI login, an agent shell)": auth.CredentialAccessToken,
		"an OAuth client":             auth.CredentialOAuthClient,
		"an unclassified worker call": auth.CredentialUnknown,
	} {
		t.Run("both enforce, "+name+": needs a person", func(t *testing.T) {
			setPlatform(t, orgpolicy.EnforcementEnforce)
			agent := auth.WithCredential(context.Background(), cred)
			for what, existing := range map[string]*core.Trigger{"update": existing, "create": nil} {
				got, err := loosenAs(agent, automations(enforce, orgKeys, personal), existing)
				require.Error(t, err, what)
				assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), what)
				assert.Equal(t, auth.ReasonPersonRequired, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader), what)
				assert.Contains(t, err.Error(), "needs a person signed in to the Reliant app", what)
				assert.Empty(t, got, what)
			}
		})
	}

	for name, tc := range map[string]struct {
		platform orgpolicy.Enforcement
		doc      *controlplanev1.OrgPolicy
	}{
		"section enforced, platform in audit": {orgpolicy.EnforcementAudit, automations(enforce, personal)},
		"platform enforces, section in audit": {orgpolicy.EnforcementEnforce, automations(audit, personal)},
		"platform off":                        {orgpolicy.EnforcementOff, automations(enforce, personal)},
		"no policy":                           {orgpolicy.EnforcementEnforce, &controlplanev1.OrgPolicy{}},
		"automations without personal":        {orgpolicy.EnforcementEnforce, automations(enforce, orgKeys)},
		"providers refuse personal": {orgpolicy.EnforcementEnforce, &controlplanev1.OrgPolicy{
			Automations: &controlplanev1.AutomationsPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{personal}},
			Providers:   &controlplanev1.ProvidersPolicy{AllowedSources: []controlplanev1.ProviderSource{orgKeys}},
		}},
	} {
		t.Run(name+": refused", func(t *testing.T) {
			setPlatform(t, tc.platform)
			_, err := loosen(tc.doc)
			require.Error(t, err)
			assert.Equal(t, ReasonOrgOnlyAutomation, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
		})
	}

	t.Run("an unreadable policy: refused", func(t *testing.T) {
		setPlatform(t, orgpolicy.EnforcementEnforce)
		ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(downPolicy{}))
		_, err := s.unattendedFundingFor(ctx, "owner", &org, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY, existing)
		require.Error(t, err)
		assert.Equal(t, ReasonOrgOnlyAutomation, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
	})
}

// downPolicy is a control plane that cannot be reached.
type downPolicy struct{}

func (downPolicy) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{}, errors.New("control plane down")
}
