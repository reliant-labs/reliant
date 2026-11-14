// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The org's provider policy where a call resolves (control-plane
// docs/design/org-policies.md §4.2, §5, R2): a chat begun before the policy
// is refused at its next call with the policy's reason, failover never lands
// on a forbidden provider, the driver build is the floor for anything that
// reached it around the seam, and audit refuses nothing.

const providersPolicyOrg = "org-providers"

// twoKeyStore is the person's own Anthropic and OpenAI API keys, read through
// the real loader (drivers.BuildAvailableDrivers), so the org's policy
// narrows them exactly as in production.
type twoKeyStore struct{}

func (twoKeyStore) ListProviderAccountsWithSecrets(context.Context, string) ([]core.AccountWithSecrets, error) {
	key := func(id, provider, secret string) core.AccountWithSecrets {
		return core.AccountWithSecrets{
			ProviderAccount: core.ProviderAccount{ID: id, UserID: "u1", OwnerKind: core.AccountOwnerUser, Provider: provider, AuthKind: core.AuthKindAPIKey},
			Secrets:         core.AccountSecrets{Secret: secret},
		}
	}
	return []core.AccountWithSecrets{key("ant", "anthropic", "sk-ant"), key("oai", "openai", "sk-oai")}, nil
}
func (twoKeyStore) ListAccountLimits(context.Context, string) (map[string]core.LimitState, error) {
	return nil, nil
}
func (twoKeyStore) ListOwnedProviderAccountsWithSecrets(context.Context, core.AccountOwner) ([]core.AccountWithSecrets, error) {
	return nil, nil
}
func (twoKeyStore) ListOwnedAccountLimits(context.Context, core.AccountOwner) (map[string]core.LimitState, error) {
	return nil, nil
}
func (twoKeyStore) ListMemberAccountGrants(context.Context, string, string) (map[string]time.Time, error) {
	return nil, nil
}

// throughTheSeam makes every account read of the call go through the real
// loader over twoKeyStore.
func throughTheSeam(t *testing.T) {
	t.Helper()
	orig := lookupAvailableDrivers
	lookupAvailableDrivers = func(ctx context.Context, scope drivers.Scope) (models.AvailableDrivers, error) {
		return drivers.BuildAvailableDrivers(ctx, twoKeyStore{}, scope)
	}
	t.Cleanup(func() { lookupAvailableDrivers = orig })
}

type providersPolicy struct{ doc *controlplanev1.OrgPolicy }

func (p providersPolicy) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if orgID != providersPolicyOrg || p.doc == nil {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 9, Policy: p.doc}, nil
}

func providersCtx(mode controlplanev1.OrgPolicyMode, allowed ...string) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(providersPolicy{&controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: mode, Allowed: allowed},
	}}))
}

func withProvidersEnforcement(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })
}

func orgCall(thread string, attended bool, env *routingEnv) llmCallSpec {
	return llmCallSpec{
		UserID: "u1", OrgID: providersPolicyOrg, Attended: attended, UnattendedAnySource: !attended,
		SessionID: "s", ThreadID: thread, Selector: models.ModelSelector{ID: routeModel}, Routing: env.routing,
	}
}

func TestOrgPolicyProviders_AChatBegunBeforeThePolicyIsRefusedAtItsNextCall(t *testing.T) {
	throughTheSeam(t)
	withProvidersEnforcement(t, orgpolicy.EnforcementEnforce)
	env := newRoutingEnv(t)

	res, err := resolveLLMCall(context.Background(), nil, orgCall("t1", true, env))
	require.NoError(t, err, "before the policy the chat runs")
	assert.Equal(t, "anthropic", res.ProviderDriver)

	ctx := providersCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "openai")
	for name, spec := range map[string]llmCallSpec{
		"its next call":                     orgCall("t1", true, env),
		"its next call, bound to anthropic": func() llmCallSpec { s := orgCall("t1", true, env); s.ChatProvider = "anthropic"; return s }(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveLLMCall(ctx, nil, spec)
			refusal, ok := orgpolicy.RefusalOf(err)
			require.True(t, ok, "want the policy's refusal, got %v", err)
			assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refusal.Reason)
			assert.False(t, refusal.Unattended, "a person is behind the chat: the turn pauses")
			assert.False(t, isProviderSwitchHold(err), "a policy refusal is not a hold on the chat's provider")
		})
	}

	_, err = resolveLLMCall(ctx, nil, orgCall("t2", false, env))
	refusal, ok := orgpolicy.RefusalOf(err)
	require.True(t, ok, "%v", err)
	assert.True(t, refusal.Unattended, "nobody is behind an automation's run: it ends instead of pausing")
}

// Audit mode counts without refusing: the call resolves exactly as with no
// policy, on the provider the policy would refuse.
func TestOrgPolicyProviders_AuditRefusesNothing(t *testing.T) {
	throughTheSeam(t)
	env := newRoutingEnv(t)
	for name, tc := range map[string]struct {
		sw   orgpolicy.Enforcement
		mode controlplanev1.OrgPolicyMode
	}{
		"the point audits":   {orgpolicy.EnforcementAudit, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE},
		"the section audits": {orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT},
	} {
		t.Run(name, func(t *testing.T) {
			withProvidersEnforcement(t, tc.sw)
			ctx := providersCtx(tc.mode, "openai")
			res, err := resolveLLMCall(ctx, nil, orgCall("t-"+name, true, env))
			require.NoError(t, err)
			assert.Equal(t, "anthropic", res.ProviderDriver)
			d, err := orgpolicy.JudgeProvider(ctx, providersPolicyOrg, "anthropic", drivers.ProviderSource("anthropic", models.FundingPersonal), false, true)
			require.NoError(t, err)
			assert.True(t, d.Denied && !d.Refused, "the use is counted as one the policy would refuse, and proceeds")
		})
	}
}

// Failover's alternatives come from the narrowed set: a usage limit never
// moves a turn onto a provider the org does not allow.
func TestOrgPolicyProviders_FailoverNeverLandsOnAForbiddenProvider(t *testing.T) {
	throughTheSeam(t)
	withProvidersEnforcement(t, orgpolicy.EnforcementEnforce)
	const fallback = "gpt-5.5@openai"
	limited := &ratelimit.UsageLimitError{Provider: "Anthropic", ResetAt: time.Now().Add(time.Hour)}
	scope := drivers.Scope{UserID: "u1", OrgID: providersPolicyOrg, Attended: true}

	for name, tc := range map[string]struct {
		allowed []string
		want    string
	}{
		"the fallback's provider is forbidden": {[]string{"anthropic"}, ""},
		"the fallback's provider is allowed":   {[]string{"anthropic", "openai"}, fallback},
	} {
		t.Run(name, func(t *testing.T) {
			env := newRoutingEnv(t)
			ctx := providersCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, tc.allowed...)
			first, err := resolveLLMCall(ctx, nil, orgCall("t-"+name, true, env))
			require.NoError(t, err)
			got := env.routing.usageLimitFailure(ctx, limited, usageLimitContext{
				Scope: scope, ThreadID: "t-" + name, Driver: "anthropic", ModelID: routeModel, Account: first.Account,
				Policy: types.FailoverPolicy{FallbackModel: fallback},
			})
			var f *types.UsageLimitFailure
			require.ErrorAs(t, got, &f)
			assert.Equal(t, tc.want, f.Facts.FallbackModel)
		})
	}
}

// The driver build is the floor: an account that reached it around the seam
// (here, a lookup that ignores the policy) is refused, never built.
func TestOrgPolicyProviders_TheDriverBuildIsTheFloor(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("mine", "me@example.com", 0)))
	withProvidersEnforcement(t, orgpolicy.EnforcementEnforce)
	env := newRoutingEnv(t)
	ctx := providersCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "anthropic")

	spec := orgCall("t1", true, env)
	_, err := resolveLLMCall(ctx, nil, spec)
	refusal, ok := orgpolicy.RefusalOf(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refusal.Reason)
	assert.Empty(t, env.built, "the refused account was never built")
}
