package project

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
)

// Who may delete, restore and purge a project (control-plane
// docs/design/unified-shell-and-projects.md §11.4, D19): its owner, and an
// org admin only once the owner has left the org; never a member, an
// outsider, or a credential bound to another org; an admin only as a person.

const deleteOrg = "acme"

// deleteNow is the fixed clock the service under test reads (Deps.Now), so
// a restore window's end is asserted exactly, never against the wall clock.
var deleteNow = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

// deleteMembers is control-plane's membership answer: every user in roles is
// a member of deleteOrg with that role, unless they left.
type deleteMembers struct {
	roles map[string]string
	left  map[string]bool
}

func (m deleteMembers) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		orgID = deleteOrg
	}
	role, ok := m.roles[userID]
	if !ok || m.left[userID] || orgID != deleteOrg {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true, Role: role}, nil
}

type deleteAuthFixture struct {
	t                              *testing.T
	repo                           *db.Repo
	purges                         projectpurge.Service
	owner, admin, member, outsider string
	project, trigger               string
	members                        deleteMembers
	schedules                      *recordingSchedules
}

// recordingSchedules records the triggers whose schedules were converged.
type recordingSchedules struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingSchedules) Sync(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func (r *recordingSchedules) synced() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newDeleteAuthFixture(t *testing.T) *deleteAuthFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	s := uuid.NewString()[:8]
	f := &deleteAuthFixture{
		t: t, repo: repo, purges: projectpurge.New(projectpurge.Deps{DB: repo.ORM()}),
		owner: "owner-" + s, admin: "admin-" + s, member: "member-" + s, outsider: "outsider-" + s,
		project: "proj-" + s, trigger: "trig-" + s,
		schedules: &recordingSchedules{},
	}
	f.members = deleteMembers{
		roles: map[string]string{f.owner: orgs.RoleMember, f.admin: orgs.RoleAdmin, f.member: orgs.RoleMember},
		left:  map[string]bool{},
	}
	ctx := context.Background()
	now := deleteNow
	org := deleteOrg
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: f.project, UserID: f.owner, OrgID: &org, Name: "doomed",
		Path: "/home/owner/doomed-" + s, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, triggertest.Create(ctx, repo, &core.Trigger{ID: f.trigger, UserID: f.owner, OrgID: &org, ProjectID: f.project,
		Name: "nightly", Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now}))
	return f
}

// service is the project service over the fixture's database, deciding
// membership as control-plane answers it now (a fresh resolver, so a
// departure is seen at once).
func (f *deleteAuthFixture) service() (*Service, *orgs.Resolver) {
	m := orgs.New(f.members)
	clock := func() time.Time { return deleteNow }
	return NewTestProject(f.t, WithProjectDeps(withStores(Deps{Repo: f.repo, Projects: projectaccesstest.For(f.repo), Purges: f.purges, Orgs: m, Schedules: f.schedules, Now: clock}))), m
}

// as is a request as userID through credential c; a machine token is also
// bound to an org.
func as(m *orgs.Resolver, userID string, c auth.Credential, boundOrg string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	ctx = authz.WithMembership(ctx, m)
	ctx = auth.WithCredential(ctx, c)
	if boundOrg != "" {
		ctx = authz.WithCredentialOrg(ctx, boundOrg)
	}
	return ctx
}

func (f *deleteAuthFixture) delete(svc *Service, ctx context.Context, fromMachines bool) (*reliantv1.DeleteProjectResponse, error) {
	resp, err := svc.DeleteProject(ctx, connect.NewRequest(&reliantv1.DeleteProjectRequest{ProjectId: f.project, RemoveFromMachines: fromMachines}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (f *deleteAuthFixture) scheduled() bool {
	f.t.Helper()
	p, err := f.repo.GetProject(context.Background(), f.project)
	require.NoError(f.t, err)
	return p.DeleteAfter != nil
}

func TestProjectDeleteAuthorityWhileTheOwnerIsAMember(t *testing.T) {
	f := newDeleteAuthFixture(t)
	svc, m := f.service()
	for _, c := range []struct {
		name     string
		ctx      context.Context
		wantCode connect.Code
		reason   string
	}{
		{"a member who is not an admin is not told it exists", as(m, f.member, auth.CredentialSession, ""), connect.CodeNotFound, ""},
		{"an outsider is not told it exists", as(m, f.outsider, auth.CredentialSession, ""), connect.CodeNotFound, ""},
		{"an admin may not delete a live member's work", as(m, f.admin, auth.CredentialSession, ""), connect.CodeFailedPrecondition, ReasonProjectOwnerIsMember},
		{"the owner's machine token bound to another org reaches nothing here", as(m, f.owner, auth.CredentialAccessToken, "globex"), connect.CodeNotFound, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.delete(svc, c.ctx, false)
			require.Error(t, err)
			assert.Equal(t, c.wantCode, svcerr.Code(err), "error: %v", err)
			if c.reason != "" {
				assert.Equal(t, c.reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			}
			assert.False(t, f.scheduled(), "nothing was scheduled")
		})
	}

	t.Run("the owner's machine token in the project's org schedules it, and the owner restores it", func(t *testing.T) {
		resp, err := f.delete(svc, as(m, f.owner, auth.CredentialAccessToken, deleteOrg), false)
		require.NoError(t, err)
		assert.True(t, resp.GetSuccess())
		require.NotNil(t, resp.DeleteAfter)
		assert.Equal(t, deleteNow.Add(core.ProjectDeleteWindow).Format(time.RFC3339), resp.GetDeleteAfter())
		assert.True(t, f.scheduled())
		assert.Equal(t, []string{f.trigger}, f.schedules.synced(), "the paused trigger's schedule is converged")

		owner := as(m, f.owner, auth.CredentialSession, "")
		_, err = svc.GetProject(owner, connect.NewRequest(&reliantv1.GetProjectRequest{ProjectId: f.project}))
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a scheduled project is hidden from its owner")
		list, err := svc.ListProjects(owner, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
		require.NoError(t, err)
		assert.Empty(t, list.Msg.GetProjects())
		deleted, err := svc.ListDeletedProjects(owner, connect.NewRequest(&reliantv1.ListDeletedProjectsRequest{}))
		require.NoError(t, err)
		require.Len(t, deleted.Msg.GetProjects(), 1)
		assert.NotEmpty(t, deleted.Msg.GetProjects()[0].GetPath(), "the owner sees their own path")

		restored, err := svc.RestoreProject(owner, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
		require.NoError(t, err)
		assert.EqualValues(t, 1, restored.Msg.GetTriggersResumed())
		assert.Nil(t, restored.Msg.GetProject().DeleteAfter)
		assert.False(t, f.scheduled())
		tr, err := store.GetTrigger(context.Background(), f.repo.ORM(), f.trigger)
		require.NoError(t, err)
		assert.True(t, tr.Enabled, "restore turns the paused trigger back on")
	})
}

func TestProjectDeleteAuthorityOnceTheOwnerLeft(t *testing.T) {
	f := newDeleteAuthFixture(t)
	f.members.left[f.owner] = true
	svc, m := f.service()
	admin := as(m, f.admin, auth.CredentialSession, "")

	for _, c := range []struct {
		name     string
		ctx      context.Context
		machines bool
		wantCode connect.Code
	}{
		{"the departed owner no longer reaches it", as(m, f.owner, auth.CredentialSession, ""), false, connect.CodeNotFound},
		{"a member who is not an admin still may not", as(m, f.member, auth.CredentialSession, ""), false, connect.CodeNotFound},
		{"an admin acts only as a person, never through a machine token", as(m, f.admin, auth.CredentialAccessToken, deleteOrg), false, connect.CodePermissionDenied},
		{"an admin may not remove code from the departed owner's machines", admin, true, connect.CodePermissionDenied},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.delete(svc, c.ctx, c.machines)
			require.Error(t, err)
			assert.Equal(t, c.wantCode, svcerr.Code(err), "error: %v", err)
			assert.False(t, f.scheduled())
		})
	}

	t.Run("an admin deletes it, finds it under recently deleted, and restores it", func(t *testing.T) {
		_, err := f.delete(svc, admin, false)
		require.NoError(t, err)
		assert.True(t, f.scheduled())
		p, err := f.repo.GetProject(context.Background(), f.project)
		require.NoError(t, err)
		assert.Equal(t, f.admin, *p.DeletedBy)

		deleted, err := svc.ListDeletedProjects(admin, connect.NewRequest(&reliantv1.ListDeletedProjectsRequest{}))
		require.NoError(t, err)
		require.Len(t, deleted.Msg.GetProjects(), 1)
		got := deleted.Msg.GetProjects()[0]
		assert.Empty(t, got.GetPath(), "an admin is not shown the departed owner's path")
		assert.Equal(t, f.admin, got.GetDeletedByUserId())

		_, err = svc.PurgeProjectNow(admin, connect.NewRequest(&reliantv1.PurgeProjectNowRequest{ProjectId: f.project, ConfirmName: "doomed"}))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "only the owner purges at once")

		member := as(m, f.member, auth.CredentialSession, "")
		_, err = svc.RestoreProject(member, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a member may not restore it either")

		restored, err := svc.RestoreProject(admin, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
		require.NoError(t, err)
		assert.EqualValues(t, 1, restored.Msg.GetTriggersResumed())
		assert.False(t, f.scheduled())
	})
}

// A deleted project still holds its path until it is purged: adding the
// folder again says how to free it rather than failing on the key.
func TestCreateProjectAtADeletedProjectsPathSaysHowToFreeIt(t *testing.T) {
	f := newDeleteAuthFixture(t)
	svc, m := f.service()
	owner := as(m, f.owner, auth.CredentialSession, "")
	_, err := f.delete(svc, owner, false)
	require.NoError(t, err)
	p, err := f.repo.GetProject(context.Background(), f.project)
	require.NoError(t, err)

	_, err = svc.CreateProject(owner, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "again", Path: p.Path}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "error: %v", err)
	assert.Equal(t, ReasonProjectScheduledForDeletion, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
	assert.Contains(t, err.Error(), "reliant project restore "+f.project)
}

func TestPurgeProjectNowNeedsTheOwnerAndItsName(t *testing.T) {
	f := newDeleteAuthFixture(t)
	svc, m := f.service()
	owner := as(m, f.owner, auth.CredentialSession, "")

	_, err := svc.PurgeProjectNow(owner, connect.NewRequest(&reliantv1.PurgeProjectNowRequest{ProjectId: f.project, ConfirmName: "wrong"}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = f.repo.GetProject(context.Background(), f.project)
	require.NoError(t, err, "a wrong name deletes nothing")

	_, err = svc.PurgeProjectNow(owner, connect.NewRequest(&reliantv1.PurgeProjectNowRequest{ProjectId: f.project, ConfirmName: "doomed"}))
	require.NoError(t, err)
	svc.WaitForPurges()
	_, err = f.repo.GetProject(context.Background(), f.project)
	assert.True(t, svcerr.IsNotFound(err), "the project is gone once its batches finish")
	assert.Contains(t, f.schedules.synced(), f.trigger, "the purged trigger's schedule is converged away")
}

// heldPurge is the database with an immediate purge's batches held until
// release is closed; started closes when they are reached. With fail set
// they then fail instead of running.
type heldPurge struct {
	projectpurge.Service
	started, release chan struct{}
	fail             error
}

func (h heldPurge) PurgeClaimedProject(ctx context.Context, id, owner string) ([]string, error) {
	close(h.started)
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if h.fail != nil {
		return nil, h.fail
	}
	return h.Service.PurgeClaimedProject(ctx, id, owner)
}

// PurgeProjectNow answers once the project is claimed, before its batches
// run: a project with large chats takes longer to delete than a request may
// run (prod: deadline_exceeded after 10s, 2026-11-18). By then the project is
// hidden and can no longer be restored; the batches finish after the
// response, and the purged triggers' schedules are converged then. A purge
// whose batches fail is left to the purge worker, which finishes it once the
// claim's lease has passed.
func TestPurgeProjectNowAnswersBeforeItsBatchesRun(t *testing.T) {
	for _, c := range []struct {
		name string
		fail error
	}{
		{name: "the batches finish after the response"},
		{name: "batches that fail are finished by the purge worker", fail: errors.New("test: the batches stopped part way")},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDeleteAuthFixture(t)
			m := orgs.New(f.members)
			clock := func() time.Time { return deleteNow }
			held := heldPurge{Service: f.purges, started: make(chan struct{}), release: make(chan struct{}), fail: c.fail}
			svc := NewTestProject(t, WithProjectDeps(withStores(Deps{Repo: f.repo, Projects: projectaccesstest.For(f.repo), Purges: held, Orgs: m, Schedules: f.schedules, Now: clock})))
			owner := as(m, f.owner, auth.CredentialSession, "")

			_, err := svc.PurgeProjectNow(owner, connect.NewRequest(&reliantv1.PurgeProjectNowRequest{ProjectId: f.project, ConfirmName: "doomed"}))
			require.NoError(t, err, "the rpc answers while its batches are held")
			<-held.started

			p, err := f.repo.GetProject(context.Background(), f.project)
			require.NoError(t, err, "the batches have not run")
			require.NotNil(t, p.PurgeStartedAt, "the project is claimed")
			_, err = svc.GetProject(owner, connect.NewRequest(&reliantv1.GetProjectRequest{ProjectId: f.project}))
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a claimed project is hidden at once")
			_, err = svc.RestoreProject(owner, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
			assert.Error(t, err, "a claimed project can no longer be restored")
			assert.True(t, f.scheduled(), "the refused restore changed nothing")
			assert.Empty(t, f.schedules.synced(), "no schedule is converged before the batches finish")

			close(held.release)
			svc.WaitForPurges()
			ctx := context.Background()
			if c.fail == nil {
				_, err = f.repo.GetProject(ctx, f.project)
				assert.True(t, svcerr.IsNotFound(err), "the detached batches deleted the project")
				assert.Contains(t, f.schedules.synced(), f.trigger, "the purged trigger's schedule is converged away")
				return
			}
			_, err = f.repo.GetProject(ctx, f.project)
			require.NoError(t, err, "a failed purge leaves the project, hidden and claimed")
			assert.Empty(t, f.schedules.synced(), "nothing was purged, so no schedule moved")
			res, err := f.purges.PurgeDueProjects(ctx, time.Now().Add(projectpurge.Lease+time.Minute), projectpurge.Lease, 50)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Purged, "the worker finishes it once the claim's lease has passed")
			_, err = f.repo.GetProject(ctx, f.project)
			assert.True(t, svcerr.IsNotFound(err), "the project is gone")
		})
	}
}

// movesBeforeWrite is the database with a project move landing between the
// handler's authority decision and its write: each delete or restore first
// commits the project's restamp to another org, as MoveProjectOrg would.
type movesBeforeWrite struct {
	projectpurge.Service
	repo *db.Repo
	to   string
}

func (m movesBeforeWrite) move(ctx context.Context, id string) error {
	_, err := m.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = ?0 WHERE id = ?1`, m.to, id).Exec(ctx)
	return err
}

func (m movesBeforeWrite) ScheduleProjectDeletion(ctx context.Context, id, by string, after time.Time, decided core.ProjectDecision) (core.ProjectDeletion, error) {
	if err := m.move(ctx, id); err != nil {
		return core.ProjectDeletion{}, err
	}
	return m.Service.ScheduleProjectDeletion(ctx, id, by, after, decided)
}

func (m movesBeforeWrite) RestoreProject(ctx context.Context, id string, decided core.ProjectDecision) ([]string, error) {
	if err := m.move(ctx, id); err != nil {
		return nil, err
	}
	return m.Service.RestoreProject(ctx, id, decided)
}

// An admin of the org a departed member's project is in may delete and
// restore it there (D19), but not once the project has moved to another org
// between the admin's authority check and the write: both are refused, and
// nothing changes.
func TestAnAdminsDeleteAndRestoreRefuseAProjectThatMovedOrg(t *testing.T) {
	f := newDeleteAuthFixture(t)
	f.members.left[f.owner] = true
	m := orgs.New(f.members)
	clock := func() time.Time { return deleteNow }
	moving := NewTestProject(t, WithProjectDeps(withStores(Deps{Repo: f.repo, Projects: projectaccesstest.For(f.repo), Purges: movesBeforeWrite{Service: f.purges, repo: f.repo, to: "globex"}, Orgs: m, Schedules: f.schedules, Now: clock})))
	admin := as(m, f.admin, auth.CredentialSession, "")

	_, err := f.delete(moving, admin, false)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "error: %v", err)
	assert.False(t, f.scheduled(), "the delete wrote nothing")

	// Back in acme and deleted there; then a restore that a move overtakes.
	_, err = f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = ?0 WHERE id = ?1`, deleteOrg, f.project).Exec(context.Background())
	require.NoError(t, err)
	plain, _ := f.service()
	_, err = f.delete(plain, admin, false)
	require.NoError(t, err)
	require.True(t, f.scheduled())

	_, err = moving.RestoreProject(admin, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "error: %v", err)
	assert.True(t, f.scheduled(), "the restore wrote nothing")
}
