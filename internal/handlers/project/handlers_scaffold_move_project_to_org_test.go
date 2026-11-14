// yours: scaffolded once, never touched again — forge will not overwrite this file.
package project_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// moveFixture is the project handler over a scripted ledger: projects by id,
// the moves recorded, and the saga starts asked for.
type moveFixture struct {
	projects map[string]*db.Project
	moves    map[string]*core.ProjectOrgMove
	blockers core.ProjectOrgMoveBlockers
	started  []string
	stamped  []string
	// settle is the state a started move reads back as.
	settle core.ProjectOrgMoveState
}

func newMoveFixture() *moveFixture {
	acme := "acme"
	return &moveFixture{
		projects: map[string]*db.Project{
			"p-mine":   {ID: "p-mine", UserID: "test-user", OrgID: &acme},
			"p-theirs": {ID: "p-theirs", UserID: "someone-else", OrgID: &acme},
			"p-legacy": {ID: "p-legacy", UserID: "test-user"},
		},
		moves:  map[string]*core.ProjectOrgMove{},
		settle: core.ProjectOrgMoveStateCompleted,
	}
}

func (f *moveFixture) service(t *testing.T, gateOn bool) *project.Service {
	repo := &project.MockRepository{
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if p, ok := f.projects[id]; ok {
				return p, nil
			}
			return nil, svcerr.ErrNotFound
		},
		ProjectOrgMoveBlockersFunc: func(context.Context, string) (*core.ProjectOrgMoveBlockers, error) {
			b := f.blockers
			return &b, nil
		},
		AssignUnassignedOrgFunc: func(_ context.Context, userID, orgID string) error {
			f.stamped = append(f.stamped, userID+"→"+orgID)
			return nil
		},
		FindProjectOrgMoveByRequestFunc: func(_ context.Context, actor, key string) (*core.ProjectOrgMove, error) {
			for _, m := range f.moves {
				if m.ActorUserID == actor && m.RequestKey == key {
					return m, nil
				}
			}
			return nil, core.ErrProjectOrgMoveNotFound
		},
		InsertProjectOrgMoveFunc: func(_ context.Context, m *core.ProjectOrgMove) (*core.ProjectOrgMove, bool, error) {
			stored := *m
			stored.State = core.ProjectOrgMoveStatePreparing
			f.moves[m.ID] = &stored
			return &stored, true, nil
		},
		GetProjectOrgMoveFunc: func(_ context.Context, id string) (*core.ProjectOrgMove, error) {
			m, ok := f.moves[id]
			if !ok {
				return nil, core.ErrProjectOrgMoveNotFound
			}
			return m, nil
		},
		ListProjectOrgMovesFunc: func(_ context.Context, filter core.ProjectOrgMoveFilters) ([]*core.ProjectOrgMove, error) {
			var out []*core.ProjectOrgMove
			for _, m := range f.moves {
				if (filter.OrgID != "" && (m.FromOrgID == filter.OrgID || m.ToOrgID == filter.OrgID)) ||
					(filter.OrgID == "" && (m.OwnerUserID == filter.UserID || m.ActorUserID == filter.UserID)) {
					out = append(out, m)
				}
			}
			return out, nil
		},
	}
	starter := &project.MockMoveStarter{StartProjectOrgMoveFunc: func(_ context.Context, id string) error {
		f.started = append(f.started, id)
		f.moves[id].State = f.settle
		return nil
	}}
	gates := &configv1.OrgsConfig{ProjectMovesEnabled: gateOn}
	return project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: repo, OrgRollout: gates, Moves: starter}))
}

func personIn(t *testing.T, m authz.Membership) context.Context {
	return authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), m), authz.CredentialKindPerson)
}

func moveReq(projectID, to, from, key string) *connect.Request[pb.MoveProjectToOrgRequest] {
	return connect.NewRequest(&pb.MoveProjectToOrgRequest{ProjectId: projectID, ToOrgId: to, ExpectedFromOrgId: from, RequestKey: key})
}

func reasonOf(err error) string {
	return svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader)
}

// MoveProjectToOrg records the move, starts its saga and answers it once it
// settles; a retried request key is the same move; every refusal is named.
func TestMoveProjectToOrg(t *testing.T) {
	project.SetMoveWait(t, time.Second, time.Millisecond)
	ctx := personIn(t, moveOrgs{})

	t.Run("the gate is off", func(t *testing.T) {
		f := newMoveFixture()
		_, err := f.service(t, false).MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "acme", "k"))
		require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Equal(t, project.ReasonProjectMovesDisabled, reasonOf(err))
		assert.Empty(t, f.moves)
	})

	t.Run("the owner moves it", func(t *testing.T) {
		f := newMoveFixture()
		svc := f.service(t, true)
		resp, err := svc.MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "acme", "k-1"))
		require.NoError(t, err)
		m := resp.Msg.GetMove()
		assert.Equal(t, "acme", m.GetFromOrgId())
		assert.Equal(t, "globex", m.GetToOrgId())
		assert.Equal(t, "test-user", m.GetOwnerUserId())
		assert.Equal(t, pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED, m.GetState())
		require.Len(t, f.started, 1)
		assert.Equal(t, m.GetId(), f.started[0])

		// The same request key again is the same move, not a second one.
		again, err := svc.MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "acme", "k-1"))
		require.NoError(t, err)
		assert.Equal(t, m.GetId(), again.Msg.GetMove().GetId())
		assert.Len(t, f.moves, 1)
		assert.Len(t, f.started, 1, "a settled move is not started again")
	})

	t.Run("a move still under way is answered as it is", func(t *testing.T) {
		f := newMoveFixture()
		f.settle = core.ProjectOrgMoveStatePreparing
		project.SetMoveWait(t, 20*time.Millisecond, time.Millisecond)
		resp, err := f.service(t, true).MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "acme", "k-2"))
		require.NoError(t, err)
		assert.Equal(t, pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_PREPARING, resp.Msg.GetMove().GetState())
	})

	t.Run("a project not yet assigned an org is stamped into the default first", func(t *testing.T) {
		f := newMoveFixture()
		_, err := f.service(t, true).MoveProjectToOrg(ctx, moveReq("p-legacy", "globex", "acme", "k-3"))
		require.NoError(t, err)
		assert.Equal(t, []string{"test-user→acme"}, f.stamped)
	})

	t.Run("a stale source org", func(t *testing.T) {
		f := newMoveFixture()
		_, err := f.service(t, true).MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "initech", "k"))
		require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Equal(t, project.ReasonOrgChanged, reasonOf(err))
		assert.Empty(t, f.moves)
	})

	t.Run("running work", func(t *testing.T) {
		f := newMoveFixture()
		f.blockers = core.ProjectOrgMoveBlockers{ChatsTotal: 1}
		_, err := f.service(t, true).MoveProjectToOrg(ctx, moveReq("p-mine", "globex", "acme", "k"))
		require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Equal(t, project.ReasonProjectBusy, reasonOf(err))
		assert.Empty(t, f.moves)
	})

	t.Run("another member's project", func(t *testing.T) {
		f := newMoveFixture()
		_, err := f.service(t, true).MoveProjectToOrg(ctx, moveReq("p-theirs", "globex", "acme", "k"))
		require.Equal(t, connect.CodeNotFound, svcerr.Code(err))
		assert.Empty(t, f.moves)
	})

	t.Run("an agent's credential", func(t *testing.T) {
		f := newMoveFixture()
		software := authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), moveOrgs{}), authz.CredentialKindSoftware)
		_, err := f.service(t, true).MoveProjectToOrg(software, moveReq("p-mine", "globex", "acme", "k"))
		require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
		assert.Empty(t, f.moves)
	})
}

// A move is read by its owner and by an admin or owner of either org; the
// ledger of an org by its admins and owners; anyone else is refused.
func TestProjectOrgMoveReads(t *testing.T) {
	f := newMoveFixture()
	f.moves["m-mine"] = &core.ProjectOrgMove{ID: "m-mine", OwnerUserID: "test-user", ActorUserID: "test-user", FromOrgID: "acme", ToOrgID: "globex", State: core.ProjectOrgMoveStateCompleted}
	f.moves["m-acme"] = &core.ProjectOrgMove{ID: "m-acme", OwnerUserID: "someone", ActorUserID: "someone", FromOrgID: "acme", ToOrgID: "umbrella", State: core.ProjectOrgMoveStateAborted}
	f.moves["m-far"] = &core.ProjectOrgMove{ID: "m-far", OwnerUserID: "someone", ActorUserID: "someone", FromOrgID: "initech", ToOrgID: "umbrella"}
	svc := f.service(t, false)
	ctx := personIn(t, moveOrgs{})
	get := func(id string) (*pb.ProjectOrgMove, error) {
		resp, err := svc.GetProjectOrgMove(ctx, connect.NewRequest(&pb.GetProjectOrgMoveRequest{MoveId: id}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetMove(), nil
	}

	m, err := get("m-mine")
	require.NoError(t, err)
	assert.Equal(t, pb.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED, m.GetState())
	_, err = get("m-acme")
	require.NoError(t, err, "test-user administers acme, the source")
	_, err = get("m-far")
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "neither org is test-user's to administer")
	_, err = get("m-missing")
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	list := func(org *string) ([]string, error) {
		resp, err := svc.ListProjectOrgMoves(ctx, connect.NewRequest(&pb.ListProjectOrgMovesRequest{OrgId: org}))
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, m := range resp.Msg.GetMoves() {
			ids = append(ids, m.GetId())
		}
		return ids, nil
	}
	acme, globex := "acme", "globex"
	ids, err := list(&acme)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"m-mine", "m-acme"}, ids, "acme's ledger: every move in or out of it")
	_, err = list(&globex)
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a plain member of globex reads no ledger of it")
	ids, err = list(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"m-mine"}, ids, "no org: the caller's own moves")

	// A credential bound to a third org sees none of the caller's own moves
	// between two others, read alone or listed.
	ctx = authz.WithCredentialOrg(ctx, "initech")
	_, err = get("m-mine")
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "bound to initech: a move between acme and globex")
	ids, err = list(nil)
	require.NoError(t, err)
	assert.Empty(t, ids, "bound to initech: no own move between acme and globex")
}
