// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The org integrations policy at the resolver (control-plane
// docs/design/org-policies.md §4.3): governed at USE in the org's runs, never
// at connect, so a person's own connection stays usable in their other orgs.

// orgPolicies answers each org's policy from a fixed table; an org absent from
// it has no policy.
type orgPolicies map[string]*controlplanev1.OrgPolicy

func (p orgPolicies) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	doc, ok := p[orgID]
	if !ok {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 3, Policy: doc}, nil
}

// policyOutage is a control plane that cannot be asked for a policy.
type policyOutage struct{}

func (policyOutage) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
}

// enforceIntegrations turns the platform switch for integrations to enforce
// for one test.
func enforceIntegrations(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointIntegrations)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, prev) })
}

// orgAOnlySlack is org-a enforcing a list without GitHub; org-b has no policy.
var orgAOnlySlack = orgPolicies{"org-a": {Integrations: &controlplanev1.IntegrationsPolicy{
	Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: []string{"slack"},
}}}

func policyCtx(src orgpolicy.Source) context.Context {
	ctx := authz.WithMembership(context.Background(), orgMembers{"org-a": {"dana"}, "org-b": {"dana"}})
	return orgpolicy.WithResolver(ctx, orgpolicy.New(src))
}

func TestIntegrationPolicy_PersonalConnectionUsableInOrgBButNotInOrgA(t *testing.T) {
	enforceIntegrations(t, orgpolicy.EnforcementEnforce)
	e := newEnv(t)
	ctx := policyCtx(orgAOnlySlack)
	mine := e.connect("dana", "mine")
	inA, inB, noOrg := e.newRunInOrg("dana", "org-a"), e.newRunInOrg("dana", "org-b"), e.newRun("dana")

	for name, ref := range map[string]connections.Ref{
		"by integration":   {IntegrationID: "github"},
		"by connection id": {ConnectionID: mine.ID},
		"by both":          {ConnectionID: mine.ID, IntegrationID: "github"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.resolver.ForCall(ctx, serverCall(inA), ref)
			requireIntegrationNotAllowed(t, err)

			got, err := e.resolver.ForCall(ctx, serverCall(inB), ref)
			require.NoError(t, err, "org-b has no policy: the same personal connection works there")
			require.Equal(t, mine.ID, got.ConnectionID)

			_, err = e.resolver.ForCall(ctx, serverCall(noOrg), ref)
			require.NoError(t, err, "a run of no org is never governed")
		})
	}
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used' AND run_id=$2`, mine.ID, inA),
		"a refused use is not recorded as a use")

	// Usable answers what ForCall would, so the run in org-a is not offered
	// GitHub's tools at all.
	usable, err := e.resolver.Usable(ctx, connections.CallSite{RunID: inA, Placement: connections.PlacementServer},
		[]connections.Ref{{IntegrationID: "github"}})
	require.NoError(t, err)
	requireIntegrationNotAllowed(t, usable[connections.Ref{IntegrationID: "github"}])

	// AdmitIntegration, for the uses a delegated authority serves.
	_, err = e.resolver.AdmitIntegration(ctx, inA, "github")
	requireIntegrationNotAllowed(t, err)
	_, err = e.resolver.AdmitIntegration(ctx, inA, "slack")
	require.NoError(t, err)
	_, err = e.resolver.AdmitIntegration(ctx, inB, "github")
	require.NoError(t, err)

	// The generic HTTP integration is raw egress, never governed (D14):
	// org-a's list leaves it out, and it is admitted all the same.
	_, err = e.resolver.AdmitIntegration(ctx, inA, orgpolicy.GenericHTTPIntegrationID)
	require.NoError(t, err)
}

// requireIntegrationNotAllowed pins the refusal's typed shape: a
// FailedPrecondition (the class of needs_reauth: permanent, never retried)
// carrying reason integration_not_allowed on the wire, with a message that
// says what to do.
func requireIntegrationNotAllowed(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err), "%v", err)
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	var ce *connections.Error
	require.True(t, errors.As(err, &ce))
	require.Equal(t, connections.ReasonIntegrationNotAllowed, ce.Reason)
	require.Equal(t, "integration_not_allowed", ce.Reason, "the reason is part of the API")
	wire := svcerr.ToConnect(err)
	require.Equal(t, connect.CodeFailedPrecondition, wire.Code())
	require.Equal(t, "integration_not_allowed", wire.Meta().Get(svcerr.ReasonHeader))
	require.Contains(t, err.Error(), "your organization's policy does not allow the github integration")
}

// Audit (the platform switch, or the section's own mode) counts and refuses
// nothing; off never asks.
func TestIntegrationPolicy_AuditAndOffRefuseNothing(t *testing.T) {
	e := newEnv(t)
	mine := e.connect("dana", "mine")
	inA := e.newRunInOrg("dana", "org-a")
	auditing := orgPolicies{"org-a": {Integrations: &controlplanev1.IntegrationsPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, Allowed: []string{"slack"},
	}}}
	for name, tc := range map[string]struct {
		sw  orgpolicy.Enforcement
		src orgpolicy.Source
	}{
		"platform audit, section enforce":   {orgpolicy.EnforcementAudit, orgAOnlySlack},
		"platform enforce, section audit":   {orgpolicy.EnforcementEnforce, auditing},
		"platform off":                      {orgpolicy.EnforcementOff, orgAOnlySlack},
		"platform audit, policy unreadable": {orgpolicy.EnforcementAudit, policyOutage{}},
		"platform off, policy unreadable":   {orgpolicy.EnforcementOff, policyOutage{}},
	} {
		t.Run(name, func(t *testing.T) {
			enforceIntegrations(t, tc.sw)
			got, err := e.resolver.ForCall(policyCtx(tc.src), serverCall(inA), connections.Ref{IntegrationID: "github"})
			require.NoError(t, err)
			require.Equal(t, mine.ID, got.ConnectionID)
		})
	}
}

// An enforcing point whose policy cannot be read fails closed, retryably.
func TestIntegrationPolicy_UnreadableUnderEnforceIsUnavailable(t *testing.T) {
	enforceIntegrations(t, orgpolicy.EnforcementEnforce)
	e := newEnv(t)
	e.connect("dana", "mine")
	_, err := e.resolver.ForCall(policyCtx(policyOutage{}), serverCall(e.newRunInOrg("dana", "org-a")), connections.Ref{IntegrationID: "github"})
	require.Equal(t, connections.CodeUnavailable, connections.CodeOf(err), "%v", err)
}

// A trigger's own connection is judged by its org's policy too (a poll).
func TestIntegrationPolicy_ForTrigger(t *testing.T) {
	enforceIntegrations(t, orgpolicy.EnforcementEnforce)
	e := newEnv(t)
	mine := e.connect("dana", "mine")
	ctx := policyCtx(orgAOnlySlack)
	inA := e.integrationTrigger("dana", "org-a", mine.ID)
	inB := e.integrationTrigger("dana", "org-b", mine.ID)

	_, err := e.resolver.ForTrigger(ctx, connections.TriggerSite{TriggerID: inA, Placement: connections.PlacementServer})
	requireIntegrationNotAllowed(t, err)
	_, err = e.resolver.ForTrigger(ctx, connections.TriggerSite{TriggerID: inB, Placement: connections.PlacementServer})
	require.NoError(t, err)
}

// integrationTrigger stores an enabled GitHub trigger of org owned by owner,
// listening through connID, and returns its id.
func (e *env) integrationTrigger(owner, org, connID string) string {
	e.t.Helper()
	id := "trg-" + uuid.NewString()[:8]
	require.NoError(e.t, e.repo.CreateTrigger(context.Background(), &core.Trigger{
		ID: id, UserID: owner, OrgID: &org, ProjectID: "test-project", Name: id, Kind: core.TriggerKindIntegration,
		Enabled: true, Workflow: "w", NoMachine: true, ConnectionID: &connID,
		Config: []byte(`{"integration":"github"}`), CreatedAt: fixedNow, UpdatedAt: fixedNow,
	}))
	return id
}

// The picker's marker (ListIntegrations org_id): only a governed integration
// is marked, with whether a use would be refused.
func TestIntegrationPolicy_OrgIntegrationPolicies(t *testing.T) {
	e := newEnv(t)
	ctx := policyCtx(orgAOnlySlack)
	ids := []string{"github", "slack"}

	enforceIntegrations(t, orgpolicy.EnforcementEnforce)
	got, err := e.svc.OrgIntegrationPolicies(ctx, "dana", "org-a", ids)
	require.NoError(t, err)
	require.Equal(t, map[string]connections.IntegrationPolicy{
		"github": {Allowed: false, Enforced: true, Version: 3},
		"slack":  {Allowed: true, Enforced: true, Version: 3},
	}, got)

	got, err = e.svc.OrgIntegrationPolicies(ctx, "dana", "org-b", ids)
	require.NoError(t, err)
	require.Empty(t, got, "an org with no policy marks nothing")

	orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, orgpolicy.EnforcementAudit)
	got, err = e.svc.OrgIntegrationPolicies(ctx, "dana", "org-a", ids)
	require.NoError(t, err)
	require.False(t, got["github"].Enforced, "under the audit switch a disallowed integration still works")

	_, err = e.svc.OrgIntegrationPolicies(ctx, "mallory", "org-a", ids)
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err))
}
