// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// triggerOrgSource: every caller is a live member of acme and home, not of
// globex; home is the default.
type triggerOrgSource struct{}

func (triggerOrgSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// withOrgs makes env's deployment one with orgs, through the real resolver.
func (e *triggerTestEnv) withOrgs(t *testing.T) *triggerTestEnv {
	t.Helper()
	resolver := orgs.New(triggerOrgSource{})
	e.configure(t, func(d *Deps) { d.Orgs = resolver })
	// A request through a listener carries the membership authz decides with.
	e.ctx = authz.WithMembership(e.ctx, resolver)
	return e
}

// orgProject is a project of env's user in orgID (nil: none yet).
func (e *triggerTestEnv) orgProject(t *testing.T, orgID *string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, e.repo.CreateProject(context.Background(), &db.Project{
		ID: id, UserID: e.userID, OrgID: orgID, Name: "p-" + id[:6], Path: t.TempDir(), IsGitRepo: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

func (e *triggerTestEnv) createIn(ctx context.Context, projectID string, org *string) (*reliantv1.Trigger, error) {
	resp, err := e.svc.CreateTrigger(ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: e.definition(func(d *reliantv1.TriggerDefinition) { d.ProjectId = projectID }),
		OrgId:   org,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetTrigger(), nil
}

func (e *triggerTestEnv) userTriggers(t *testing.T) []*core.Trigger {
	t.Helper()
	stored, err := e.repo.ListTriggers(context.Background(), core.TriggerFilters{UserID: e.userID})
	require.NoError(t, err)
	return stored
}

func requireDenied(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.Code(err), "error: %v", err)
	if reason != "" {
		assert.Equal(t, reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader), "error: %v", err)
	}
}

// A trigger's org is verified where it is created: an org the caller is not
// in, another org than a machine credential's, or a project of an org the
// caller is not in is refused and stores nothing; a project of a member org
// gives the trigger that org.
func TestCreateTrigger_OrgIsVerified(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme, home, globex := "acme", "home", "globex"
	acmeProject, globexProject := env.orgProject(t, &acme), env.orgProject(t, &globex)

	_, err := env.createIn(env.ctx, env.projectID, &globex)
	requireDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)

	// A credential bound to acme cannot even see a project of home (its
	// default org, which a project with no org yet belongs to): authz refuses
	// it before the org is asked about; naming another org than its own is a
	// mismatch.
	machine := auth.WithMachineToken(env.ctx, &fat.Principal{OrgID: acme, ActingUserID: env.userID})
	_, err = env.createIn(machine, env.projectID, &home)
	requireDenied(t, err, connect.CodeNotFound, "")
	_, err = env.createIn(machine, acmeProject, &home)
	requireDenied(t, err, connect.CodePermissionDenied, orgs.ReasonOrgMismatch)

	// A project of an org the caller is not in is not theirs to see at all
	// (authz), before its org is ever asked about.
	for _, blank := range []*string{nil, new(string)} {
		_, err = env.createIn(env.ctx, globexProject, blank)
		requireDenied(t, err, connect.CodeNotFound, "")
	}
	_, err = env.createIn(env.ctx, acmeProject, &home)
	requireDenied(t, err, connect.CodeFailedPrecondition, "")
	require.Empty(t, env.userTriggers(t), "a refused create stores no trigger")

	created, err := env.createIn(env.ctx, acmeProject, nil)
	require.NoError(t, err)
	assert.Equal(t, acme, created.GetOrgId(), "a project's org is the trigger's")
	legacy, err := env.createIn(env.ctx, env.projectID, nil)
	require.NoError(t, err)
	assert.Equal(t, home, legacy.GetOrgId(), "no project org and none named: the default")
}

// A trigger stays in its org: an update cannot move it to a project of
// another org, including a trigger with no org yet, whose org is its
// project's.
func TestUpdateTrigger_StaysInItsOrg(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme, home := "acme", "home"
	acmeProject, acmeOther, homeProject := env.orgProject(t, &acme), env.orgProject(t, &acme), env.orgProject(t, &home)
	legacyProject := env.orgProject(t, nil)

	move := func(id, projectID string) error {
		_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id:      id,
			Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.ProjectId = projectID }),
		}))
		return err
	}
	stored := func(id string) *core.Trigger {
		tr, err := env.repo.GetTrigger(context.Background(), id)
		require.NoError(t, err)
		return tr
	}

	assigned, err := env.createIn(env.ctx, acmeProject, nil)
	require.NoError(t, err)
	requireDenied(t, move(assigned.GetId(), homeProject), connect.CodeFailedPrecondition, "")
	requireDenied(t, move(assigned.GetId(), legacyProject), connect.CodeFailedPrecondition, "")
	assert.Equal(t, acmeProject, stored(assigned.GetId()).ProjectID)
	require.NoError(t, move(assigned.GetId(), acmeOther), "within its org")
	assert.Equal(t, acmeOther, stored(assigned.GetId()).ProjectID)

	// A trigger written before orgs, in an acme project: acme's.
	unassigned, err := env.createIn(env.ctx, acmeProject, nil)
	require.NoError(t, err)
	_, err = env.repo.ORM().Bun().NewRaw(`UPDATE triggers SET org_id = NULL WHERE id = ?`, unassigned.GetId()).Exec(context.Background())
	require.NoError(t, err)
	requireDenied(t, move(unassigned.GetId(), legacyProject), connect.CodeFailedPrecondition, "")
	acmeThird := env.orgProject(t, &acme)
	require.NoError(t, move(unassigned.GetId(), acmeThird))
	row := stored(unassigned.GetId())
	require.NotNil(t, row.OrgID, "the move stamps the org it was decided in")
	assert.Equal(t, acme, *row.OrgID)
}

// leftAcmeSource is triggerOrgSource after the caller left acme.
type leftAcmeSource struct{}

func (leftAcmeSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "home"}, nil
}

// Every update of an org's trigger acts in that org, moved or not: a caller
// who has left the org, or whose credential is bound to another org, cannot
// reconfigure it or move it within the org, and its stored definition is
// unchanged.
func TestUpdateTrigger_ActsInTheTriggersOrg(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme := "acme"
	acmeProject, acmeOther := env.orgProject(t, &acme), env.orgProject(t, &acme)
	created, err := env.createIn(env.ctx, acmeProject, nil)
	require.NoError(t, err)
	before, err := env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)

	update := func(ctx context.Context, projectID string) error {
		_, err := env.svc.UpdateTrigger(ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id: created.GetId(),
			Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
				d.ProjectId = projectID
				d.Message = "Reconfigured."
			}),
		}))
		return err
	}
	unchanged := func() {
		t.Helper()
		after, err := env.repo.GetTrigger(context.Background(), created.GetId())
		require.NoError(t, err)
		assert.Equal(t, before.ProjectID, after.ProjectID)
		assert.Equal(t, before.Message, after.Message)
	}

	machine := auth.WithMachineToken(env.ctx, &fat.Principal{OrgID: "home", ActingUserID: env.userID})
	// Reconfigure in place, and move within the org.
	for _, projectID := range []string{acmeProject, acmeOther} {
		requireDenied(t, update(machine, projectID), connect.CodeNotFound, "") // authz: another org's trigger
		unchanged()
	}

	env.configure(t, func(d *Deps) { d.Orgs = orgs.New(leftAcmeSource{}) })
	for _, projectID := range []string{acmeProject, acmeOther} {
		requireDenied(t, update(env.ctx, projectID), connect.CodePermissionDenied, orgs.ReasonNotOrgMember)
		unchanged()
	}
}

// The trigger list shows what GetTrigger would: a removed member's acme
// triggers are not listed even when the request names no org, while home's
// and the unassigned ones are; and a credential bound to acme, whose acting
// user has left acme, lists nothing of it.
func TestListTriggers_LeavesOutALeftOrgsTriggers(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme, home := "acme", "home"
	acmeTrigger, err := env.createIn(env.ctx, env.orgProject(t, &acme), nil)
	require.NoError(t, err)
	homeTrigger, err := env.createIn(env.ctx, env.orgProject(t, &home), nil)
	require.NoError(t, err)

	removed := orgs.New(leftAcmeSource{})
	env.configure(t, func(d *Deps) { d.Orgs = removed })
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), removed)
	listed, err := env.svc.ListTriggers(ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	var ids []string
	for _, tr := range listed.Msg.GetTriggers() {
		ids = append(ids, tr.GetId())
	}
	require.Contains(t, ids, homeTrigger.GetId())
	require.NotContains(t, ids, acmeTrigger.GetId(), "a left org's trigger, no org_id named")

	machine := auth.WithMachineToken(ctx, &fat.Principal{OrgID: acme, ActingUserID: env.userID})
	_, err = env.svc.ListTriggers(machine, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	requireDenied(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember)
}

// backfillBeforeWrite is the trigger repository with a backfill that stamps
// the user's unassigned rows with org between the handler's org decision and
// its write: the interleaving the write's own re-check exists for.
type backfillBeforeWrite struct {
	Repository
	repo   *db.Repo
	userID string
	org    string
}

func (r backfillBeforeWrite) UpdateTriggerInOrg(ctx context.Context, t *core.Trigger, decided db.TriggerOrgDecision) error {
	if err := r.repo.AssignUnassignedOrg(context.Background(), r.userID, r.org); err != nil {
		return err
	}
	return r.Repository.UpdateTriggerInOrg(ctx, t, decided)
}

// An update decided in one org is not written once a backfill commits the
// trigger, or the project it is in, to another org between the decision and
// the write: neither a move to a project of the decided org nor an update in
// place lands, and the stored definition is unchanged.
func TestUpdateTrigger_ABackfillBetweenDecisionAndWriteIsRefused(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	home := "home"
	homeProject := env.orgProject(t, &home)

	// A trigger written before orgs, in a project written before orgs: both
	// decide as the caller's default, home, until the backfill says otherwise.
	unassigned := func() *core.Trigger {
		t.Helper()
		project := env.orgProject(t, nil)
		created, err := env.createIn(env.ctx, project, nil)
		require.NoError(t, err)
		_, err = env.repo.ORM().Bun().NewRaw(`UPDATE triggers SET org_id = NULL WHERE id = ?`, created.GetId()).Exec(context.Background())
		require.NoError(t, err)
		stored, err := env.repo.GetTrigger(context.Background(), created.GetId())
		require.NoError(t, err)
		require.Nil(t, stored.OrgID)
		return stored
	}

	// The backfill stamps the user's unassigned rows acme (a default that
	// changed under the request) after the handler decided home.
	env.configure(t, func(d *Deps) {
		d.Repo = backfillBeforeWrite{Repository: d.Repo, repo: env.repo, userID: env.userID, org: "acme"}
	})
	update := func(tr *core.Trigger, projectID string) error {
		_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id: tr.ID,
			Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
				d.ProjectId = projectID
				d.Message = "Reconfigured."
			}),
		}))
		return err
	}
	refused := func(name string, tr *core.Trigger, projectID string) {
		t.Helper()
		requireDenied(t, update(tr, projectID), connect.CodeFailedPrecondition, "")
		after, err := env.repo.GetTrigger(context.Background(), tr.ID)
		require.NoError(t, err)
		assert.Equal(t, tr.ProjectID, after.ProjectID, name)
		assert.Equal(t, tr.Message, after.Message, name)
		require.NotNil(t, after.OrgID, name)
		assert.Equal(t, "acme", *after.OrgID, "%s: the backfill's org stands", name)
	}
	// Each backfill stamps every unassigned row, so each case starts from
	// rows created after the previous one's.
	refused("a move to a project of the decided org", unassigned(), homeProject)
	inPlace := unassigned()
	refused("an update in place", inPlace, inPlace.ProjectID)
}
