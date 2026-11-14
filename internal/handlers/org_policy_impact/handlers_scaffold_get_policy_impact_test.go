package org_policy_impact_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	org_policy_impact "github.com/reliant-labs/reliant/internal/handlers/org_policy_impact"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
)

const org = "org-acme"

// roles is control-plane's answer: user -> role in org.
type roles map[string]string

func (r roles) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return orgID == org && r[userID] != "", true, nil
}

func (r roles) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if orgID != org {
		return "", true, nil
	}
	return r[userID], true, nil
}

// calls is the org's chats' latest calls, and the window it was asked for.
type calls struct {
	mu    sync.Mutex
	rows  []db.OrgChatCall
	since time.Time
}

func (c *calls) LatestOrgChatCalls(_ context.Context, orgID string, since time.Time) ([]db.OrgChatCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.since = since
	if orgID != org {
		return nil, nil
	}
	return c.rows, nil
}

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T, store *calls) *org_policy_impact.Service {
	t.Helper()
	return org_policy_impact.NewTestOrgPolicyImpact(t, org_policy_impact.WithOrgPolicyImpactDeps(org_policy_impact.Deps{
		Store: store, Now: func() time.Time { return now },
	}))
}

func as(user string, c auth.Credential) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	return auth.WithCredential(authz.WithMembership(ctx, roles{"ada": orgs.RoleAdmin, "olga": orgs.RoleOwner, "mia": orgs.RoleMember}), c)
}

func policyJSON(t *testing.T, doc *controlplanev1.OrgPolicy) string {
	t.Helper()
	b, err := protojson.Marshal(doc)
	require.NoError(t, err)
	return string(b)
}

// A proposed policy's impact is counted, by reason, on each chat's latest
// call, the way the resolver judges the next one: the provider and the kind
// of account (who pays), the automations section for a chat a trigger
// launched, and the models section for the managed driver only.
func TestGetPolicyImpact_CountsTheChatsTheProposedPolicyWouldRefuse(t *testing.T) {
	store := &calls{rows: []db.OrgChatCall{
		{ChatID: "c1", Provider: "claude", Model: "claude-4.6-sonnet@claude", Funding: "personal"},
		{ChatID: "c2", Provider: "anthropic", Model: "claude-4.6-sonnet", Funding: "org_key"},
		{ChatID: "c3", Provider: "reliant", Model: "claude-4.6-sonnet@reliant", Funding: "org_wallet"},
		{ChatID: "c4", Provider: "reliant", Model: "claude-4.6-opus", Funding: "org_wallet"},
		{ChatID: "c5", Provider: "claude", Model: "claude-4.6-sonnet", Funding: "org_seat", Automation: true},
		{ChatID: "c6", Provider: "anthropic", Model: "claude-4.6-sonnet", Funding: "org_key", Automation: true},
	}}
	svc := newService(t, store)
	enforce := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	doc := &controlplanev1.OrgPolicy{
		Providers: &controlplanev1.ProvidersPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{
			controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS, controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_SEATS,
			controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT,
		}},
		Automations: &controlplanev1.AutomationsPolicy{Mode: enforce, AllowedSources: []controlplanev1.ProviderSource{
			controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS, controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT,
		}},
		// Audit or enforce, the preview counts what the rule refuses.
		Models: &controlplanev1.ModelsPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, Allowed: []string{"claude-opus-4-6"}},
	}

	resp, err := svc.GetPolicyImpact(as("ada", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{
		OrgId: org, PolicyJson: policyJSON(t, doc),
	}))
	require.NoError(t, err)
	got := resp.Msg
	assert.EqualValues(t, 14, got.GetWindowDays())
	assert.Equal(t, now.Add(-org_policy_impact.ImpactWindow), store.since)
	assert.EqualValues(t, 6, got.GetChatsConsidered())
	assert.EqualValues(t, 3, got.GetChatsRefused(), "c1 (a personal account), c3 (a managed model not allowed), c5 (an automation on a seat)")
	assert.EqualValues(t, 1, got.GetAutomationsRefused())
	require.Len(t, got.GetReasons(), 2)
	assert.Equal(t, orgpolicy.ReasonModelNotAllowed, got.GetReasons()[0].GetReason())
	assert.EqualValues(t, 1, got.GetReasons()[0].GetChats())
	assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, got.GetReasons()[1].GetReason())
	assert.EqualValues(t, 2, got.GetReasons()[1].GetChats())
	assert.EqualValues(t, 1, got.GetReasons()[1].GetAutomations())

	// No policy refuses nothing.
	resp, err = svc.GetPolicyImpact(as("olga", auth.CredentialSession), connect.NewRequest(&pb.GetPolicyImpactRequest{OrgId: org}))
	require.NoError(t, err)
	assert.EqualValues(t, 6, resp.Msg.GetChatsConsidered())
	assert.Zero(t, resp.Msg.GetChatsRefused())
	assert.Empty(t, resp.Msg.GetReasons())
}

// Who may ask: an owner or admin of the org, as a person; a bad document is
// the caller's to fix.
func TestGetPolicyImpact_Refusals(t *testing.T) {
	svc := newService(t, &calls{})
	for name, tc := range map[string]struct {
		ctx    context.Context
		req    *pb.GetPolicyImpactRequest
		code   connect.Code
		reason string
	}{
		"a member":               {as("mia", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired},
		"a non-member":           {as("nick", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired},
		"an admin's CLI login":   {as("ada", auth.CredentialAccessToken), &pb.GetPolicyImpactRequest{OrgId: org}, connect.CodePermissionDenied, orgs.ReasonHumanOnly},
		"no org":                 {as("ada", auth.CredentialSession), &pb.GetPolicyImpactRequest{}, connect.CodeInvalidArgument, ""},
		"a document that is not": {as("ada", auth.CredentialSession), &pb.GetPolicyImpactRequest{OrgId: org, PolicyJson: `{"providers": 7}`}, connect.CodeInvalidArgument, org_policy_impact.ReasonPolicyInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.GetPolicyImpact(tc.ctx, connect.NewRequest(tc.req))
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err), "%v", err)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			}
		})
	}
}
