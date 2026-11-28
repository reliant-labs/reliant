// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
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
	err = update(team.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
	require.Error(t, err, "loosening in a solo org still needs a person")
	assert.Equal(t, auth.ReasonPersonRequired, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, team.ID))
	e.creatorCtx = auth.WithCredential(e.creatorCtx, auth.CredentialSession)
	require.NoError(t, update(team.ID, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY))
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, team.ID), "a person loosening in a solo org")
}

// Widening a trigger's funding to its owner's own AI accounts is a person's
// decision, even where org policy admits it: an agent shell holds the
// owner's CLI login, and a raw CreateTrigger or UpdateTrigger with it is
// refused. Tightening, and writes that leave the stamp alone, stay open to
// every credential, so existing automations keep working.
func TestTriggerUnattendedFunding_WideningNeedsAPerson(t *testing.T) {
	// Not parallel: it sets the process-wide provider-policy enforcement.
	prev := orgpolicy.EnforcementOf(orgpolicy.PointProviders)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointProviders, prev) })

	e := setupAdoption(t)
	e.configure(t, func(d *Deps) { d.Orgs = &countedOrgs{members: 3, known: true} })
	personal := controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL
	admits := orgpolicy.New(policyDoc{&controlplanev1.OrgPolicy{Automations: &controlplanev1.AutomationsPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, AllowedSources: []controlplanev1.ProviderSource{personal},
	}}})
	as := func(c auth.Credential) context.Context {
		return orgpolicy.WithResolver(auth.WithCredential(e.creatorCtx, c), admits)
	}
	agent, person := as(auth.CredentialAccessToken), as(auth.CredentialSession)
	const (
		unspecified = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED
		orgOnly     = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY
		anySource   = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY
	)
	definition := func(name string, f reliantv1.UnattendedFunding) *reliantv1.TriggerDefinition {
		return e.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = name
			d.ProjectId = e.acmeProject
			d.UnattendedFunding = f
		})
	}
	create := func(ctx context.Context, name string, f reliantv1.UnattendedFunding) (string, error) {
		resp, err := e.svc.CreateTrigger(ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: definition(name, f)}))
		if err != nil {
			return "", err
		}
		return resp.Msg.GetTrigger().GetId(), nil
	}
	update := func(ctx context.Context, id, name string, f reliantv1.UnattendedFunding) error {
		_, err := e.svc.UpdateTrigger(ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: id, Trigger: definition(name, f)}))
		return err
	}
	needsPerson := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
		assert.Equal(t, auth.ReasonPersonRequired, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
		assert.Contains(t, err.Error(), "turn it on in the app")
	}
	list := func() int {
		resp, err := e.svc.ListTriggers(person, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
		require.NoError(t, err)
		return len(resp.Msg.GetTriggers())
	}

	// Create.
	before := list()
	_, err := create(agent, "agent widened", anySource)
	needsPerson(t, err)
	assert.Equal(t, before, list(), "a refused create stores nothing")
	wide, err := create(person, "person widened", anySource)
	require.NoError(t, err)
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, wide))
	tight, err := create(agent, "agent default", unspecified)
	require.NoError(t, err, "an agent creating with the default stamp")
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, tight))
	explicit, err := create(agent, "agent tight", orgOnly)
	require.NoError(t, err, "an agent creating org_only")
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, explicit))

	// Update.
	needsPerson(t, update(agent, tight, "agent default", anySource))
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, tight), "a refused widening writes nothing")
	require.NoError(t, update(agent, tight, "renamed by an agent", unspecified), "an unrelated update")
	assert.Equal(t, "renamed by an agent", e.row(t, tight).Name)
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, tight), "an unrelated update keeps the stamp")
	require.NoError(t, update(agent, wide, "person widened", anySource), "an agent naming the stamp a trigger already has")
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, wide))
	require.NoError(t, update(agent, wide, "renamed again", unspecified), "an unrelated update of a widened trigger")
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, wide))
	require.NoError(t, update(agent, wide, "renamed again", orgOnly), "an agent tightening")
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, wide))
	require.NoError(t, update(person, wide, "renamed again", anySource), "a person widening")
	assert.Equal(t, core.UnattendedFundingAny, stampOf(t, e, wide))
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
	_, err := store.NewTriggerWritesExtStore(e.repo.ORM()).SystemPauseTrigger(context.Background(), old.ID, core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	old = e.row(t, old.ID)

	members.members = 2
	resp, err := e.svc.AdoptAs(e.bobCtx, old, e.bobID, e.rebinding(nil), "")
	require.NoError(t, err)
	assert.Equal(t, core.UnattendedFundingOrgOnly, stampOf(t, e, old.ID))
	assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY, resp.GetTrigger().GetUnattendedFunding())
}

// Enabling a workflow again with only a different stamp is a change, not
// ALREADY_ENABLED or a bare resume: a tightening is written, and a loosening
// in a team org is refused and reported, never silently ignored. Both for an
// activation that is on and for one that is paused.
func TestEnableWorkflow_AFundingOnlyChangeIsNotANoOp(t *testing.T) {
	t.Parallel()
	for name, paused := range map[string]bool{"an enabled activation": false, "a paused activation": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := setupTriggerTest(t).withOrgs(t)
			members := &countedOrgs{members: 1, known: true}
			env.configure(t, func(d *Deps) { d.Orgs = members })
			acme := "acme"
			project := env.orgProject(t, &acme)
			env.saveWorkflow(t, "nightly", scheduledWorkflowYAML)

			enable := func(f reliantv1.UnattendedFunding) *reliantv1.WorkflowTriggerResult {
				t.Helper()
				resp, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(&reliantv1.EnableWorkflowRequest{
					Workflow: "nightly", ProjectId: project, DaemonId: env.daemonID, Message: "Run the nightly job.",
					UnattendedFunding: f,
				}))
				require.NoError(t, err)
				require.Len(t, resp.Msg.GetResults(), 1)
				return resp.Msg.GetResults()[0]
			}
			row := func(id string) *core.Trigger {
				t.Helper()
				r, err := store.GetTrigger(context.Background(), env.repo.ORM(), id)
				require.NoError(t, err)
				return r
			}
			pause := func(id string) {
				t.Helper()
				if !paused {
					return
				}
				_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
				require.NoError(t, err)
			}

			first := enable(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED)
			id := first.GetActivation().GetId()
			require.NotEmpty(t, id, "%v", first)
			require.Equal(t, core.UnattendedFundingAny, row(id).UnattendedFunding, "made in a solo org")

			pause(id)
			tightened := enable(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY)
			assert.NotEqual(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, tightened.GetOutcome())
			assert.NotEqual(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, tightened.GetOutcome(), tightened.GetMessage())
			assert.Equal(t, core.UnattendedFundingOrgOnly, row(id).UnattendedFunding, "the tightening is written")
			assert.True(t, row(id).Enabled)

			members.members = 2
			pause(id)
			loosened := enable(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
			assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, loosened.GetOutcome(), "a refused loosening is reported")
			assert.Contains(t, loosened.GetMessage(), "own AI accounts")
			assert.Equal(t, core.UnattendedFundingOrgOnly, row(id).UnattendedFunding, "and writes nothing")
			assert.Equal(t, !paused, row(id).Enabled, "nothing else changes either")

			again := enable(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED)
			if !paused {
				assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, again.GetOutcome(), "naming no stamp keeps it")
			}
			assert.Equal(t, core.UnattendedFundingOrgOnly, row(id).UnattendedFunding)
		})
	}
}
