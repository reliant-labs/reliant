// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// countedOrgs is an OrgResolver for an org whose live-member count is
// members (unknown when !known), in which every caller is a member.
type countedOrgs struct {
	members int
	known   bool
}

func (countedOrgs) Named(_ context.Context, _, requested string) (string, error) {
	return requested, nil
}
func (countedOrgs) DefaultOrg(context.Context, string) (string, error) { return "", nil }
func (countedOrgs) ActingOrg(_ context.Context, _, requested string) (string, error) {
	return requested, nil
}
func (countedOrgs) ListScope(context.Context, string, string, orgs.Backfill) (*core.OrgScope, error) {
	return nil, nil
}
func (o *countedOrgs) LiveMembersFresh(context.Context, string, string) (int, bool, error) {
	return o.members, o.known, nil
}

func stampOf(t *testing.T, e *adoptEnv, id string) core.UnattendedFunding {
	t.Helper()
	return e.row(t, id).UnattendedFunding
}

// Create stamps a trigger from its org's shape, the read always says what is
// stored, an update that names nothing keeps it, and loosening in a team org
// is refused (org-provider-accounts §11.3).
func TestTriggerUnattendedFunding_CreateReadUpdate(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	members := &countedOrgs{members: 2, known: true}
	e.configure(t, func(d *Deps) { d.Orgs = members })

	team, resp := e.made(t, e.definition(nil))
	assert.Equal(t, core.UnattendedFundingOrgOnly, team.UnattendedFunding, "a new trigger in a two-member org")
	assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY, resp.GetTrigger().GetUnattendedFunding())

	members.members = 1
	solo, resp := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "solo" }))
	assert.Equal(t, core.UnattendedFundingAny, solo.UnattendedFunding, "a new trigger in a solo org")
	assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY, resp.GetTrigger().GetUnattendedFunding())

	members.known = false
	unknown, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "old control plane" }))
	assert.Equal(t, core.UnattendedFundingAny, unknown.UnattendedFunding, "an unknown count stamps any, a default")

	// A trigger stored before the stamp reads ANY.
	_, err := e.repo.ORM().Bun().NewRaw(`UPDATE triggers SET unattended_funding = NULL WHERE id = ?`, unknown.ID).Exec(context.Background())
	require.NoError(t, err)
	got, err := e.svc.GetTrigger(e.creatorCtx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: unknown.ID}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY, got.Msg.GetTrigger().GetUnattendedFunding())

	update := func(id string, f reliantv1.UnattendedFunding) error {
		name := e.row(t, id).Name
		def := e.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = name
			d.ProjectId = e.acmeProject
			d.UnattendedFunding = f
		})
		_, err := e.svc.UpdateTrigger(e.creatorCtx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: id, Trigger: def}))
		return err
	}
	members.members, members.known = 2, true
	require.NoError(t, update(team.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED))
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, team.ID), "an update that names nothing keeps the stamp")
	require.NoError(t, update(unknown.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED))
	assert.Equal(t, core.UnattendedFunding(""), stampOf(t, e, unknown.ID), "an unstamped trigger stays unstamped")

	err = update(team.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, ReasonOrgOnlyAutomation, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, team.ID), "a refused loosening writes nothing")

	require.NoError(t, update(solo.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY))
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, solo.ID), "the owner can always tighten")

	members.members = 1
	require.NoError(t, update(team.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY))
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, team.ID), "loosening in a solo org")
}

// An adopted trigger is re-stamped for its adopter from the org's shape now,
// never kept as its creator's.
func TestAdoptAs_RestampsUnattendedFunding(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	members := &countedOrgs{members: 1, known: true}
	e.configure(t, func(d *Deps) { d.Orgs = members })
	old, _ := e.made(t, e.definition(nil))
	require.Equal(t, core.UnattendedFundingAny, old.UnattendedFunding, "made while the org was solo")
	_, err := e.repo.PauseTrigger(context.Background(), old.ID, core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	old = e.row(t, old.ID)

	members.members = 2
	resp, err := e.svc.AdoptAs(e.bobCtx, old, e.bobID, e.rebinding(nil), "")
	require.NoError(t, err)
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, old.ID))
	assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY, resp.GetTrigger().GetUnattendedFunding())
}
