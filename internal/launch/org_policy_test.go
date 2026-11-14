// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// Launch validation under the org's provider policy (control-plane
// docs/design/org-policies.md §4.1: "fails at launch with the reason, so the
// person finds out when they press send, not mid-turn").

type launchPolicy struct{ doc *controlplanev1.OrgPolicy }

func (p launchPolicy) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 5, Policy: p.doc}, nil
}

// anthropicAndOpenAIKeys is a person's own Anthropic and OpenAI keys.
func anthropicAndOpenAIKeys() *drivers.MockAPIKeyStore {
	return &drivers.MockAPIKeyStore{
		ListProviderAccountsWithSecretsFunc: func(context.Context, string) ([]core.AccountWithSecrets, error) {
			key := func(id, provider, secret string) core.AccountWithSecrets {
				return core.AccountWithSecrets{
					ProviderAccount: core.ProviderAccount{ID: id, UserID: "user-1", OwnerKind: core.AccountOwnerUser, Provider: provider, AuthKind: core.AuthKindAPIKey},
					Secrets:         core.AccountSecrets{Secret: secret},
				}
			}
			return []core.AccountWithSecrets{key("ant", "anthropic", "sk-ant-api03-test"), key("oai", "openai", "sk-oai-test")}, nil
		},
		ListAccountLimitsFunc: func(context.Context, string) (map[string]core.LimitState, error) { return nil, nil },
		ListOwnedProviderAccountsWithSecretsFunc: func(context.Context, core.AccountOwner) ([]core.AccountWithSecrets, error) {
			return nil, nil
		},
		ListOwnedAccountLimitsFunc: func(context.Context, core.AccountOwner) (map[string]core.LimitState, error) { return nil, nil },
	}
}

func TestLaunchValidation_FailsWithThePolicysReason(t *testing.T) {
	drivers.InitializeAPIKeyProvider(anthropicAndOpenAIKeys())
	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })

	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(launchPolicy{&controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: []string{"openai"}},
	}}))
	org := "org-acme"
	chat := &db.Chat{ProjectID: "project-1", OrgID: &org}
	launcher := NewLauncher(nil, nil, nil, nil, "")
	inputs := func() map[string]interface{} {
		return map[string]interface{}{"mode": "auto", "model": map[string]interface{}{"id": "claude-4.6-sonnet@anthropic"}}
	}

	// A launch: the model input names a provider the org does not allow.
	errs := launcher.validateChatWorkflowInputsAt(ctx, "user-1", chat, launchAccountScope(Spec{}, "user-1", chat), "builtin://agent", "", inputs(), false)
	require.Len(t, errs, 1)
	refused, ok := PolicyRefusal(errs[0])
	require.True(t, ok, "want a policy refusal, got %v", errs[0])
	assert.Equal(t, "model", refused.Input)
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refused.Refusal.Reason)
	assert.True(t, svcerr.IsFailedPrecondition(refused), "a verdict, not a malformed input")
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, svcerr.ToConnect(refused).Meta().Get(svcerr.ReasonHeader))
	assert.Contains(t, refused.Error(), "Your organization's policy does not allow this provider")
	assert.NotContains(t, refused.Error(), "RELIANT_ORG_POLICY_REFUSED", "an rpc's message carries no chat marker")

	// The same input on a send: the send moves it onto an allowed provider
	// (SubstituteUnservableModels, with a notice), so it is not refused.
	assert.Empty(t, launcher.ValidateChatWorkflowInputs(ctx, "user-1", chat, "builtin://agent", inputs()))

	// An allowed provider passes; a chat of no org is not the org's to govern.
	assert.Empty(t, launcher.validateChatWorkflowInputsAt(ctx, "user-1", chat, launchAccountScope(Spec{}, "user-1", chat), "builtin://agent", "",
		map[string]interface{}{"mode": "auto", "model": map[string]interface{}{"id": "gpt-6-terra"}}, false))
	noOrg := &db.Chat{ProjectID: "project-1"}
	assert.Empty(t, launcher.validateChatWorkflowInputsAt(ctx, "user-1", noOrg, launchAccountScope(Spec{}, "user-1", noOrg), "builtin://agent", "", inputs(), false))
}

// A launch nobody is behind is validated as the unattended run it starts:
// the org's automations section applies; a person's launch it does not.
func TestLaunchAccountScope(t *testing.T) {
	org := "org-acme"
	chat := &db.Chat{OrgID: &org}
	assert.Equal(t, drivers.Scope{UserID: "u", OrgID: org, Attended: true}, launchAccountScope(Spec{}, "u", chat))
	assert.Equal(t, drivers.Scope{UserID: "u", OrgID: org, UnattendedAnySource: true}, launchAccountScope(Spec{Unattended: true}, "u", chat))
	assert.Equal(t, drivers.PersonalScope("u"), launchAccountScope(Spec{Unattended: true}, "u", &db.Chat{}))
	assert.Equal(t, drivers.PersonalScope("u"), AccountScope("u", nil))
}

type downPolicy struct{}

func (downPolicy) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
}

// A local model input under a policy that cannot be read is a failed read
// (drivers.ErrDriverLookupFailed, which buildInputs returns as a retryable
// InternalError), never a policy verdict or an invalid input: a trigger's
// firing is retried, not recorded failed.
func TestLaunchValidation_AnUnreadablePolicyIsRetryable(t *testing.T) {
	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(downPolicy{}))
	org := "org-acme"
	chat := &db.Chat{ProjectID: "project-1", OrgID: &org}

	errs := NewLauncher(nil, nil, nil, nil, "").validateChatWorkflowInputsAt(ctx, "user-1", chat, launchAccountScope(Spec{Unattended: true}, "user-1", chat),
		"builtin://agent", "", map[string]interface{}{"mode": "auto", "model": map[string]interface{}{"id": "llama3@local"}}, false)
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], drivers.ErrDriverLookupFailed)
	_, refused := PolicyRefusal(errs[0])
	assert.False(t, refused)
}
